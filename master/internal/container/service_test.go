package container

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

func connectContainerTestAgent(t *testing.T, hub *agenthub.Hub, nodeID string) *websocket.Conn {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.ServeAgent(conn, nodeID, r.RemoteAddr)
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	deadline := time.Now().Add(time.Second)
	for !hub.Online(nodeID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !hub.Online(nodeID) {
		t.Fatal("test agent did not become online")
	}
	return client
}

func TestValidateContainerAction(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		ids     []string
		wantErr bool
	}{
		{name: "update all", action: "update"},
		{name: "update selected", action: "update", ids: []string{"abc"}},
		{name: "restart selected", action: "restart", ids: []string{"abc"}},
		{name: "upgrade selected", action: "upgrade", ids: []string{"abc"}},
		{name: "delete selected", action: "delete", ids: []string{"abc"}},
		{name: "unknown", action: "destroy", ids: []string{"abc"}, wantErr: true},
		{name: "non update all", action: "restart", wantErr: true},
		{name: "blank id", action: "stop", ids: []string{" "}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateContainerAction(tt.action, tt.ids)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateContainerAction() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestActionRejectsUnknownAndUnsafeEmptyIDs(t *testing.T) {
	svc := &Service{Hub: agenthub.New(agenthub.Handlers{})}
	for _, body := range []string{
		`{"node_id":"n1","action":"destroy","ids":["abc"]}`,
		`{"node_id":"n1","action":"delete"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/containers/action", bytes.NewBufferString(body))
		res := httptest.NewRecorder()
		svc.Action(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400; response=%s", body, res.Code, res.Body.String())
		}
	}
}

func TestDecodeContainerResult(t *testing.T) {
	t.Run("partial failure remains structured", func(t *testing.T) {
		res, httpErr := decodeContainerResult(json.RawMessage(`{"ok":false,"updated":["a"],"failed":{"b":"pull failed"}}`))
		if httpErr != nil {
			t.Fatalf("unexpected HTTP error: %+v", httpErr)
		}
		if res.OK || len(res.Updated) != 1 || res.Failed["b"] != "pull failed" {
			t.Fatalf("unexpected result: %+v", res)
		}
	})
	t.Run("legacy details are normalized", func(t *testing.T) {
		res, httpErr := decodeContainerResult(json.RawMessage(`{"ok":true,"details":{"bad":"error: recreate failed","good":"already up to date"}}`))
		if httpErr != nil {
			t.Fatalf("unexpected HTTP error: %+v", httpErr)
		}
		if res.OK || len(res.Failed) != 1 || res.Failed["bad"] != "error: recreate failed" {
			t.Fatalf("legacy result was not normalized: %+v", res)
		}
	})

	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{name: "empty", want: "empty container result"},
		{name: "bad json", data: `{`, want: "invalid agent container result"},
		{name: "operation error", data: `{"ok":false,"err":"docker unavailable"}`, want: "docker unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, httpErr := decodeContainerResult(json.RawMessage(tt.data))
			if httpErr == nil || !strings.Contains(httpErr.msg, tt.want) {
				t.Fatalf("error = %+v, want containing %q", httpErr, tt.want)
			}
		})
	}
}

func TestContainerResultFailureCountIncludesLegacyDetails(t *testing.T) {
	got := containerResultFailureCount(structuredContainerResultForTest())
	if got != 2 {
		t.Fatalf("failure count = %d, want 2", got)
	}
}

func structuredContainerResultForTest() proto.ContainerResult {
	return proto.ContainerResult{
		OK:     false,
		Failed: map[string]string{"new": "pull failed"},
		Details: map[string]string{
			"new":    "error: duplicate",
			"legacy": "error: recreate failed",
		},
	}
}

func TestDecodeContainerScanResultVersionCompatibility(t *testing.T) {
	legacy := json.RawMessage(`{"items":[{"name":"app","has_update":1}]}`)
	items, httpErr := decodeContainerScanResult(legacy, "2.3.2")
	if httpErr != nil || len(items) != 1 {
		t.Fatalf("legacy response = (%+v, %+v), want success", items, httpErr)
	}
	if _, httpErr := decodeContainerScanResult(legacy, "2.4.0"); httpErr == nil || !strings.Contains(httpErr.msg, "missing ok") {
		t.Fatalf("new agent missing ok error = %+v", httpErr)
	}
	if _, httpErr := decodeContainerScanResult(json.RawMessage(`{"ok":false,"err":"registry unavailable","items":[]}`), "2.4.0"); httpErr == nil || httpErr.msg != "registry unavailable" {
		t.Fatalf("agent error = %+v", httpErr)
	}
	items, httpErr = decodeContainerScanResult(json.RawMessage(`{"ok":true,"items":[]}`), "2.4.0")
	if httpErr != nil || items == nil || len(items) != 0 {
		t.Fatalf("new response = (%+v, %+v), want non-nil empty items", items, httpErr)
	}
}

func TestMissingDockerSocketScanError(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "missing socket", message: `docker socket: dial unix /var/run/docker.sock: connect: no such file or directory`, want: true},
		{name: "permission failure", message: `docker socket: dial unix /var/run/docker.sock: connect: permission denied`},
		{name: "daemon stopped", message: `docker socket: dial unix /var/run/docker.sock: connect: connection refused`},
		{name: "unrelated missing file", message: `registry config: no such file or directory`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingDockerSocketScanError(tt.message); got != tt.want {
				t.Fatalf("isMissingDockerSocketScanError(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestScanUpdatesTreatsMissingDockerSocketAsSkipped(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB.Close() })
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('storage','Storage','tok-storage','online','2.4.2',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateContainerScan(ctx, "storage", []proto.ContainerScanItem{{Name: "stale", HasUpdate: 1}}); err != nil {
		t.Fatal(err)
	}

	hub := agenthub.New(agenthub.Handlers{})
	client := connectContainerTestAgent(t, hub, "storage")
	responseErr := make(chan error, 1)
	go func() {
		var req proto.Envelope
		if err := client.ReadJSON(&req); err != nil {
			responseErr <- err
			return
		}
		data, err := json.Marshal(proto.ContainerScanResult{
			OK: false, Err: `docker socket: dial unix /var/run/docker.sock: connect: no such file or directory`,
		})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerScanResult, ID: req.ID, Data: data})
		}
		responseErr <- err
	}()

	svc := &Service{Hub: hub, Store: st}
	req := httptest.NewRequest(http.MethodPost, "/api/containers/scan-updates", nil)
	res := httptest.NewRecorder()
	svc.ScanUpdates(res, req)
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d; response=%s", res.Code, res.Body.String())
	}
	var report scanUpdatesResponse
	if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Coverage.Attempted != 1 || report.Coverage.Succeeded != 0 || len(report.Coverage.Failed) != 0 || len(report.Coverage.Skipped) != 1 {
		t.Fatalf("coverage = %+v", report.Coverage)
	}
	if got := report.Coverage.Skipped[0]; got.NodeID != "storage" || got.Reason != missingDockerSocketReason {
		t.Fatalf("skip = %+v", got)
	}
	var cached int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM container_scan WHERE node_id='storage'`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Fatalf("scan cache rows = %d, want 0", cached)
	}
}

func TestScanUpdatesReportsSkippedCoverage(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB.Close() })
	ctx := context.Background()
	_, err = st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at) VALUES
		('old','Old Agent','tok-old','online','2.3.2',1),
		('off','Offline Agent','tok-off','offline','2.4.0',2)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateContainerScan(ctx, "old", []proto.ContainerScanItem{{Name: "old-cache", HasUpdate: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateContainerScan(ctx, "off", []proto.ContainerScanItem{{Name: "offline-cache", HasUpdate: 1}}); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Hub: agenthub.New(agenthub.Handlers{}), Store: st}
	req := httptest.NewRequest(http.MethodPost, "/api/containers/scan-updates", nil)
	res := httptest.NewRecorder()
	svc.ScanUpdates(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d; response=%s", res.Code, res.Body.String())
	}
	var got scanUpdatesResponse
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Items == nil || got.Coverage.TotalNodes != 2 || got.Coverage.Attempted != 0 || got.Coverage.Succeeded != 0 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if len(got.Coverage.Skipped) != 2 || len(got.Coverage.Failed) != 0 {
		t.Fatalf("unexpected coverage: %+v", got.Coverage)
	}
	if got.Coverage.Skipped[1].NodeID != "old" || !strings.Contains(got.Coverage.Skipped[1].Reason, "upgraded to 2.4.0") {
		t.Fatalf("old agent skip reason = %+v", got.Coverage.Skipped)
	}
	var oldCache, offlineCache int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM container_scan WHERE node_id='old'`).Scan(&oldCache); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM container_scan WHERE node_id='off'`).Scan(&offlineCache); err != nil {
		t.Fatal(err)
	}
	if oldCache != 0 || offlineCache != 1 {
		t.Fatalf("cache rows after skipped scan: old=%d offline=%d", oldCache, offlineCache)
	}
	var actor, action, detail string
	if err := st.DB.QueryRowContext(ctx, `SELECT actor,action,detail FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&actor, &action, &detail); err != nil {
		t.Fatal(err)
	}
	if actor != "admin" || action != "container.scan_updates" || !strings.Contains(detail, "attempted=0") || !strings.Contains(detail, "skipped=2") {
		t.Fatalf("scan audit = actor:%q action:%q detail:%q", actor, action, detail)
	}
}

func TestActionAuditsDispatchFailure(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB.Close() })
	svc := &Service{Hub: agenthub.New(agenthub.Handlers{}), Store: st}
	req := httptest.NewRequest(http.MethodPost, "/api/containers/action",
		bytes.NewBufferString(`{"node_id":"n1","action":"update"}`))
	res := httptest.NewRecorder()
	svc.Action(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; response=%s", res.Code, res.Body.String())
	}
	var actor, action, detail string
	if err := st.DB.QueryRow(`SELECT actor,action,detail FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&actor, &action, &detail); err != nil {
		t.Fatal(err)
	}
	if actor != "admin" || action != "container.update" || !strings.Contains(detail, "node=n1") || !strings.Contains(detail, "failed=1") {
		t.Fatalf("action audit = actor:%q action:%q detail:%q", actor, action, detail)
	}
}

func TestScanUpdatesClearsCacheAfterAttemptFailure(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB.Close() })
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node 1','tok-n1','online','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateContainerScan(ctx, "n1", []proto.ContainerScanItem{{Name: "stale", HasUpdate: 1}}); err != nil {
		t.Fatal(err)
	}
	hub := agenthub.New(agenthub.Handlers{})
	client := connectContainerTestAgent(t, hub, "n1")
	responseErr := make(chan error, 1)
	go func() {
		var req proto.Envelope
		if err := client.ReadJSON(&req); err != nil {
			responseErr <- err
			return
		}
		responseErr <- client.WriteJSON(proto.Envelope{
			Type: proto.MsgContainerScanResult,
			ID:   req.ID,
			Data: json.RawMessage(`"invalid scan result"`),
		})
	}()

	svc := &Service{Hub: hub, Store: st}
	req := httptest.NewRequest(http.MethodPost, "/api/containers/scan-updates", nil)
	res := httptest.NewRecorder()
	svc.ScanUpdates(res, req)
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d; response=%s", res.Code, res.Body.String())
	}
	var report scanUpdatesResponse
	if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Coverage.Attempted != 1 || report.Coverage.Succeeded != 0 || len(report.Coverage.Failed) != 1 {
		t.Fatalf("coverage = %+v", report.Coverage)
	}
	var cached int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM container_scan WHERE node_id='n1'`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Fatalf("scan cache rows = %d, want 0 after failed attempted scan", cached)
	}
}
