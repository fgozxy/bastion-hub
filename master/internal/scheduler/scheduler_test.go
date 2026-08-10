package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/backup"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

type captureNotifier struct {
	messages []string
}

func (n *captureNotifier) Notify(_ context.Context, msg string) {
	n.messages = append(n.messages, msg)
}

func openSchedulerTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB.Close() })
	return st
}

func connectSchedulerTestAgent(t *testing.T, hub *agenthub.Hub, nodeID string) *websocket.Conn {
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
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
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

func TestContainerResultFailuresIncludesLegacyDetails(t *testing.T) {
	got := containerResultFailures(proto.ContainerResult{
		Failed: map[string]string{"new": "pull failed"},
		Details: map[string]string{
			"new":    "error: duplicate must not overwrite",
			"legacy": "error: recreate failed",
			"ok":     "already up to date",
		},
	})
	if len(got) != 2 || got["new"] != "pull failed" || got["legacy"] != "error: recreate failed" {
		t.Fatalf("failures = %#v", got)
	}
}

func TestRunContainerUpdateReportsOfflineNodesAsNotAttempted(t *testing.T) {
	st := openSchedulerTestStore(t)
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node One','tok-n1','offline','2.4.0',1),('n2','Node Two','tok-n2','offline','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	notify := &captureNotifier{}
	sc := New(st, nil, agenthub.New(agenthub.Handlers{}), notify)
	sc.runContainerUpdate(store.Schedule{
		ID:     "schedule-offline",
		Config: `{"node_ids":["n1","n2","n1"]}`,
	})
	if len(notify.messages) != 1 {
		t.Fatalf("notifications = %#v", notify.messages)
	}
	msg := notify.messages[0]
	want := "❌ 容器定时更新\n节点 成功 0 · 离线 2\n容器 更新 0 · 失败 0\n\n离线:\n· Node One\n· Node Two"
	if msg != want {
		t.Fatalf("notification = %q, want %q", msg, want)
	}
}

func TestRunContainerUpdateNodeRejectsPreReadOnlyScanAgent(t *testing.T) {
	st := openSchedulerTestStore(t)
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('old','Old Agent','tok-old','online','2.3.2',1)`); err != nil {
		t.Fatal(err)
	}
	sc := New(st, nil, agenthub.New(agenthub.Handlers{}), nil)
	out := sc.runContainerUpdateNode("schedule-old", "old", "", "stamp", nil)
	if out.succeeded || len(out.failures) != 1 || !strings.Contains(out.failures[0], "upgraded to 2.4.0") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunContainerUpdateNodeSkipsMissingDockerSocket(t *testing.T) {
	st := openSchedulerTestStore(t)
	hub := agenthub.New(agenthub.Handlers{})
	client := connectSchedulerTestAgent(t, hub, "storage")
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('storage','Storage','tok-storage','online','2.4.2',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateContainerScan(ctx, "storage", []proto.ContainerScanItem{{Name: "stale", HasUpdate: 1}}); err != nil {
		t.Fatal(err)
	}

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

	sc := New(st, nil, hub, nil)
	out := sc.runContainerUpdateNode("schedule-storage", "storage", "", "stamp", nil)
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if out.succeeded || !out.unavailable || len(out.failures) != 0 || out.failed != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	var cached int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM container_scan WHERE node_id='storage'`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Fatalf("scan cache rows = %d, want 0", cached)
	}

	report := formatContainerUpdateReport(containerUpdateStats{
		configured: 1, attempted: 1, unavailable: []string{"storage"},
		nodeNames: map[string]string{"storage": "Storage"},
	})
	want := "⚠️ 容器定时更新\n节点 成功 0 · 离线 0\n容器 更新 0 · 失败 0\n\n无 Docker:\n· Storage"
	if report != want {
		t.Fatalf("report = %q, want %q", report, want)
	}
}

func TestRunContainerUpdateCountsStructuredAndLegacyFailures(t *testing.T) {
	st := openSchedulerTestStore(t)
	hub := agenthub.New(agenthub.Handlers{})
	client := connectSchedulerTestAgent(t, hub, "n1")
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node 1','tok-n1','online','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	newID := strings.Repeat("a", 64)
	legacyID := strings.Repeat("b", 64)
	if err := st.ReplaceNodeContainers(ctx, "n1", []store.Container{
		{ContainerID: newID, Name: "new"},
		{ContainerID: legacyID, Name: "legacy"},
		{ContainerID: strings.Repeat("c", 64), Name: "current"},
		{ContainerID: strings.Repeat("d", 64), Name: "stopped"},
		{ContainerID: strings.Repeat("e", 64), Name: "mystery"},
		{ContainerID: strings.Repeat("f", 64), Name: "unmanaged"},
	}); err != nil {
		t.Fatal(err)
	}

	responseErr := make(chan error, 1)
	go func() {
		var scanReq proto.Envelope
		if err := client.ReadJSON(&scanReq); err != nil {
			responseErr <- err
			return
		}
		if scanReq.Type != proto.MsgContainerScan {
			responseErr <- fmt.Errorf("first request type = %q, want scan", scanReq.Type)
			return
		}
		scanData, err := json.Marshal(proto.ContainerScanResult{OK: true, Items: []proto.ContainerScanItem{
			{Name: "new", State: "running", UpdateType: "latest", HasUpdate: 1},
			{Name: "legacy", UpdateType: "tag", HasUpdate: 1}, // pre-2.4 state compatibility
			{Name: "current", State: "running", UpdateType: "latest", HasUpdate: 0},
			{Name: "stopped", State: "exited", UpdateType: "latest", HasUpdate: 1},
			{Name: "mystery", State: "running", UpdateType: "latest", HasUpdate: -1},
			{Name: "unmanaged", State: "running", UpdateType: "unmanaged", HasUpdate: 1},
		}})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerScanResult, ID: scanReq.ID, Data: scanData})
		}
		if err != nil {
			responseErr <- err
			return
		}

		var updateReq proto.Envelope
		if err := client.ReadJSON(&updateReq); err != nil {
			responseErr <- err
			return
		}
		if updateReq.Type != proto.MsgContainerOp {
			responseErr <- fmt.Errorf("second request type = %q, want container op", updateReq.Type)
			return
		}
		var op proto.ContainerOpRequest
		if err := json.Unmarshal(updateReq.Data, &op); err != nil {
			responseErr <- err
			return
		}
		if op.Action != "update" || len(op.IDs) != 2 || op.IDs[0] != newID || op.IDs[1] != legacyID {
			responseErr <- fmt.Errorf("update request = %+v", op)
			return
		}
		data, err := json.Marshal(proto.ContainerResult{
			OK:      false,
			Updated: []string{"updated"},
			Failed:  map[string]string{"new": "pull failed"},
			Details: map[string]string{
				"new":    "error: duplicate",
				"legacy": "error: recreate failed",
			},
		})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerResult, ID: updateReq.ID, Data: data})
		}
		responseErr <- err
	}()

	notify := &captureNotifier{}
	sc := New(st, nil, hub, notify)
	sc.runContainerUpdate(store.Schedule{ID: "schedule-partial", NodeID: "n1"})
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if len(notify.messages) != 1 {
		t.Fatalf("notifications = %#v", notify.messages)
	}
	msg := notify.messages[0]
	// Must name the updated container and each real failure. Registry-probe
	// unknowns (mystery has_update=-1) are skipped silently — not failures.
	if !strings.Contains(msg, "❌ 容器定时更新") ||
		!strings.Contains(msg, "容器 更新 1 · 失败 2") ||
		!strings.Contains(msg, "更新:\n· Node 1/updated") ||
		!strings.Contains(msg, "Node 1/legacy: error: recreate failed") ||
		!strings.Contains(msg, "Node 1/new: pull failed") ||
		strings.Contains(msg, "mystery") {
		t.Fatalf("notification = %q", msg)
	}
	var cached int
	if err := st.DB.QueryRow(`SELECT count(*) FROM container_scan WHERE node_id='n1'`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 4 {
		t.Fatalf("scan cache rows = %d, want 4 non-updated results retained", cached)
	}
}

func TestFormatContainerUpdateReportListsWhatChanged(t *testing.T) {
	got := formatContainerUpdateReport(containerUpdateStats{
		succeeded:    2,
		updated:      1,
		updatedNames: []string{"nid-a/cpa-manager-plus"},
		nodeNames:    map[string]string{"nid-a": "绿云日本软银", "nid-b": "绿云犹他"},
	})
	want := "✅ 容器定时更新\n节点 成功 2 · 离线 0\n容器 更新 1 · 失败 0\n\n更新:\n· 绿云日本软银/cpa-manager-plus"
	if got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
}

func TestContainerUpdateReportWorthSending(t *testing.T) {
	cases := []struct {
		name  string
		stats containerUpdateStats
		want  bool
	}{
		{
			name:  "quiet success no updates",
			stats: containerUpdateStats{configured: 2, attempted: 2, succeeded: 2},
			want:  false,
		},
		{
			name:  "containers updated",
			stats: containerUpdateStats{configured: 1, attempted: 1, succeeded: 1, updated: 3},
			want:  true,
		},
		{
			name:  "container failures",
			stats: containerUpdateStats{configured: 1, attempted: 1, failed: 1, failures: []string{"n1/app: pull failed"}},
			want:  true,
		},
		{
			name:  "offline nodes",
			stats: containerUpdateStats{configured: 2, offline: []string{"n1", "n2"}},
			want:  true,
		},
		{
			name:  "docker unavailable",
			stats: containerUpdateStats{configured: 1, attempted: 1, unavailable: []string{"storage"}},
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containerUpdateReportWorthSending(tc.stats); got != tc.want {
				t.Fatalf("worthSending(%+v) = %v, want %v", tc.stats, got, tc.want)
			}
		})
	}
}

// Quiet all-green runs (nodes online, scan ok, nothing to update) must not
// Telegram-spam on every cron fire.
func TestRunContainerUpdateSilentWhenNothingChanged(t *testing.T) {
	st := openSchedulerTestStore(t)
	hub := agenthub.New(agenthub.Handlers{})
	client := connectSchedulerTestAgent(t, hub, "n1")
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node 1','tok-n1','online','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceNodeContainers(ctx, "n1", []store.Container{
		{ContainerID: strings.Repeat("a", 64), Name: "current"},
	}); err != nil {
		t.Fatal(err)
	}

	responseErr := make(chan error, 1)
	go func() {
		var req proto.Envelope
		if err := client.ReadJSON(&req); err != nil {
			responseErr <- err
			return
		}
		data, err := json.Marshal(proto.ContainerScanResult{OK: true, Items: []proto.ContainerScanItem{
			{Name: "current", State: "running", UpdateType: "latest", HasUpdate: 0},
		}})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerScanResult, ID: req.ID, Data: data})
		}
		responseErr <- err
	}()

	notify := &captureNotifier{}
	sc := New(st, nil, hub, notify)
	sc.runContainerUpdate(store.Schedule{ID: "schedule-quiet", NodeID: "n1"})
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if len(notify.messages) != 0 {
		t.Fatalf("expected no notification for quiet success, got %#v", notify.messages)
	}
}

func TestRunContainerUpdateNodeNoCandidatesSucceedsWithoutUpdate(t *testing.T) {
	st := openSchedulerTestStore(t)
	hub := agenthub.New(agenthub.Handlers{})
	client := connectSchedulerTestAgent(t, hub, "n1")
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node 1','tok-n1','online','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceNodeContainers(ctx, "n1", []store.Container{
		{ContainerID: strings.Repeat("a", 64), Name: "current"},
	}); err != nil {
		t.Fatal(err)
	}

	responseErr := make(chan error, 1)
	go func() {
		var req proto.Envelope
		if err := client.ReadJSON(&req); err != nil {
			responseErr <- err
			return
		}
		data, err := json.Marshal(proto.ContainerScanResult{OK: true, Items: []proto.ContainerScanItem{
			{Name: "current", State: "running", UpdateType: "latest", HasUpdate: 0},
		}})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerScanResult, ID: req.ID, Data: data})
		}
		responseErr <- err
	}()

	sc := New(st, nil, hub, nil)
	out := sc.runContainerUpdateNode("schedule-current", "n1", "", "stamp", nil)
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if !out.succeeded || out.scanned != 1 || out.candidates != 0 || out.unchanged != 1 || len(out.failures) != 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

// Selective schedules only update the chosen container names, even when the
// node scan finds other eligible candidates.
func TestRunContainerUpdateSelectsContainersOnly(t *testing.T) {
	st := openSchedulerTestStore(t)
	hub := agenthub.New(agenthub.Handlers{})
	client := connectSchedulerTestAgent(t, hub, "n1")
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node 1','tok-n1','online','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	keepID := strings.Repeat("a", 64)
	if err := st.ReplaceNodeContainers(ctx, "n1", []store.Container{
		{ContainerID: keepID, Name: "keep"},
		{ContainerID: strings.Repeat("b", 64), Name: "other"},
	}); err != nil {
		t.Fatal(err)
	}

	responseErr := make(chan error, 1)
	go func() {
		var scanReq proto.Envelope
		if err := client.ReadJSON(&scanReq); err != nil {
			responseErr <- err
			return
		}
		if scanReq.Type != proto.MsgContainerScan {
			responseErr <- fmt.Errorf("first request type = %q, want scan", scanReq.Type)
			return
		}
		scanData, err := json.Marshal(proto.ContainerScanResult{OK: true, Items: []proto.ContainerScanItem{
			{Name: "keep", Image: "reg.example/app:1.0.0", State: "running", UpdateType: "tag", HasUpdate: 1, SuggestedImage: "reg.example/app:1.1.0"},
			{Name: "other", Image: "reg.example/svc:2.0.0", State: "running", UpdateType: "tag", HasUpdate: 1, SuggestedImage: "reg.example/svc:2.1.0"},
		}})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerScanResult, ID: scanReq.ID, Data: scanData})
		}
		if err != nil {
			responseErr <- err
			return
		}

		var updateReq proto.Envelope
		if err := client.ReadJSON(&updateReq); err != nil {
			responseErr <- err
			return
		}
		if updateReq.Type != proto.MsgContainerOp {
			responseErr <- fmt.Errorf("second request type = %q, want container op", updateReq.Type)
			return
		}
		var op proto.ContainerOpRequest
		if err := json.Unmarshal(updateReq.Data, &op); err != nil {
			responseErr <- err
			return
		}
		if op.Action != "upgrade" || len(op.IDs) != 1 || op.IDs[0] != keepID || op.NewImage != "reg.example/app:1.1.0" {
			responseErr <- fmt.Errorf("upgrade request = %+v", op)
			return
		}
		data, err := json.Marshal(proto.ContainerResult{OK: true, Updated: []string{"keep"}})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerResult, ID: updateReq.ID, Data: data})
		}
		responseErr <- err
	}()

	notify := &captureNotifier{}
	sc := New(st, nil, hub, notify)
	sc.runContainerUpdate(store.Schedule{
		ID:     "schedule-select",
		Config: `{"containers":[{"node_id":"n1","container_id":"` + keepID + `","name":"keep"}]}`,
	})
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if len(notify.messages) != 1 {
		t.Fatalf("notifications = %#v", notify.messages)
	}
	msg := notify.messages[0]
	if !strings.Contains(msg, "更新") || !strings.Contains(msg, "Node 1/keep") || strings.Contains(msg, "other") {
		t.Fatalf("notification = %q", msg)
	}
}

// A registry probe timeout (ghcr.io deadline exceeded, Docker Hub 429, etc.)
// returns HasUpdate=-1 for an otherwise eligible latest/tag image. That is a
// transient "we don't know", not a broken container — skip it and stay silent
// so every 2h cron fire does not Telegram-spam "update status unknown".
func TestRunContainerUpdateNodeSkipsRegistryProbeUnknown(t *testing.T) {
	st := openSchedulerTestStore(t)
	hub := agenthub.New(agenthub.Handlers{})
	client := connectSchedulerTestAgent(t, hub, "n1")
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node 1','tok-n1','online','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceNodeContainers(ctx, "n1", []store.Container{
		{ContainerID: strings.Repeat("a", 64), Name: "chatgpt2api"},
		{ContainerID: strings.Repeat("b", 64), Name: "current"},
	}); err != nil {
		t.Fatal(err)
	}

	responseErr := make(chan error, 1)
	go func() {
		var req proto.Envelope
		if err := client.ReadJSON(&req); err != nil {
			responseErr <- err
			return
		}
		data, err := json.Marshal(proto.ContainerScanResult{OK: true, Items: []proto.ContainerScanItem{
			{Name: "chatgpt2api", State: "running", UpdateType: "latest", HasUpdate: -1,
				Note: "无法检测 registry: context deadline exceeded"},
			{Name: "current", State: "running", UpdateType: "latest", HasUpdate: 0},
		}})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerScanResult, ID: req.ID, Data: data})
		}
		responseErr <- err
	}()

	notify := &captureNotifier{}
	sc := New(st, nil, hub, notify)
	sc.runContainerUpdate(store.Schedule{ID: "schedule-ghcr-blip", NodeID: "n1"})
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if len(notify.messages) != 0 {
		t.Fatalf("expected no notification for registry-unknown skip, got %#v", notify.messages)
	}
}

// Locally built (build), purely local, and digest-pinned images have no
// comparable registry tag, so the scan legitimately returns HasUpdate=-1. Such
// containers must be counted as skipped, not reported as a spurious "unknown"
// failure that turns the whole run ❌.
func TestRunContainerUpdateNodeSkipsNonRegistryImages(t *testing.T) {
	st := openSchedulerTestStore(t)
	hub := agenthub.New(agenthub.Handlers{})
	client := connectSchedulerTestAgent(t, hub, "n1")
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO nodes(id,name,enrollment_token,status,agent_version,created_at)
		VALUES('n1','Node 1','tok-n1','online','2.4.0',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceNodeContainers(ctx, "n1", []store.Container{
		{ContainerID: strings.Repeat("a", 64), Name: "selfbuilt"},
	}); err != nil {
		t.Fatal(err)
	}

	responseErr := make(chan error, 1)
	go func() {
		var req proto.Envelope
		if err := client.ReadJSON(&req); err != nil {
			responseErr <- err
			return
		}
		data, err := json.Marshal(proto.ContainerScanResult{OK: true, Items: []proto.ContainerScanItem{
			{Name: "selfbuilt", State: "running", UpdateType: "build", HasUpdate: -1},
			{Name: "localimg", State: "running", UpdateType: "local", HasUpdate: -1},
			{Name: "pinned", State: "running", UpdateType: "pinned", HasUpdate: -1},
		}})
		if err == nil {
			err = client.WriteJSON(proto.Envelope{Type: proto.MsgContainerScanResult, ID: req.ID, Data: data})
		}
		responseErr <- err
	}()

	sc := New(st, nil, hub, nil)
	out := sc.runContainerUpdateNode("schedule-local", "n1", "", "stamp", nil)
	if err := <-responseErr; err != nil {
		t.Fatal(err)
	}
	if out.scanned != 3 || out.skipped != 3 || out.unknown != 0 || out.candidates != 0 || len(out.failures) != 0 || !out.succeeded {
		t.Fatalf("outcome = %+v", out)
	}
}

// Two schedule batches finishing in the same window must merge into ONE push:
// nothing goes out while a batch is still in flight, and the flush combines
// both sections into a single node×target report with unioned targets.
func TestBackupReportsAggregateIntoOnePush(t *testing.T) {
	st := openSchedulerTestStore(t)
	notify := &captureNotifier{}
	sc := New(st, nil, agenthub.New(agenthub.Handlers{}), notify)

	sc.trackBackupStart()
	sc.trackBackupStart()
	sc.queueBackupReport(backupReportSection{
		results: []backup.UnitResult{{NodeID: "n1", NodeName: "华为云", TarOK: true,
			TargetOK: map[string]bool{"minio": true}, TargetIDs: []string{"minio"}}},
		targets: []backup.TargetInfo{{ID: "minio", Name: "绿云储存MinIO"}},
	})
	// First batch done but second still running: no message, no flush.
	sc.flushBackupReports()
	if len(notify.messages) != 0 {
		t.Fatalf("message pushed while a batch was still in flight: %#v", notify.messages)
	}

	sc.queueBackupReport(backupReportSection{
		results: []backup.UnitResult{{NodeID: "n2", NodeName: "绿云日本软银", TarOK: true,
			TargetOK: map[string]bool{"vps": true}, TargetIDs: []string{"vps"}}},
		targets: []backup.TargetInfo{{ID: "vps", Name: "绿云储存vps"}},
	})
	sc.flushBackupReports()

	if len(notify.messages) != 1 {
		t.Fatalf("notifications = %#v, want exactly one combined push", notify.messages)
	}
	msg := notify.messages[0]
	if !strings.Contains(msg, "计划备份报告") ||
		!strings.Contains(msg, "华为云") ||
		!strings.Contains(msg, "绿云日本软银") {
		t.Fatalf("combined report = %q", msg)
	}

	// Queue drained: a later flush with nothing pending stays silent.
	sc.flushBackupReports()
	if len(notify.messages) != 1 {
		t.Fatalf("flush with empty queue pushed again: %#v", notify.messages)
	}
}

// The debounce timer (not an explicit flush) is what actually fires the
// combined push in production; verify it with a shortened delay.
func TestBackupReportFlushTimer(t *testing.T) {
	old := reportFlushDelay
	reportFlushDelay = 30 * time.Millisecond
	t.Cleanup(func() { reportFlushDelay = old })

	st := openSchedulerTestStore(t)
	ch := make(chan string, 1)
	sc := New(st, nil, agenthub.New(agenthub.Handlers{}), notifierFunc(func(_ context.Context, msg string) {
		ch <- msg
	}))

	sc.trackBackupStart()
	sc.queueBackupReport(backupReportSection{
		results: []backup.UnitResult{{NodeID: "n1", NodeName: "华为云", TarOK: true,
			TargetOK: map[string]bool{"minio": true}, TargetIDs: []string{"minio"}}},
		targets: []backup.TargetInfo{{ID: "minio", Name: "绿云储存MinIO"}},
	})

	select {
	case msg := <-ch:
		if !strings.Contains(msg, "华为云") {
			t.Fatalf("timer-flushed report = %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("combined report was not pushed after the debounce delay")
	}
}

type notifierFunc func(context.Context, string)

func (f notifierFunc) Notify(ctx context.Context, msg string) { f(ctx, msg) }

func TestRetryOfflineUnitsRetriesAgentDisconnect(t *testing.T) {
	hub := agenthub.New(agenthub.Handlers{})
	connectSchedulerTestAgent(t, hub, "n1")

	sc := &Scheduler{hub: hub}
	units := []backupUnit{{nodeID: "n1"}}
	results := make([]backup.UnitResult, 1)
	errs := []error{backup.ErrAgentDisconnected}

	runs := 0
	run := func(i int, u backupUnit) {
		runs++
		results[i] = backup.UnitResult{NodeID: u.nodeID, TarOK: true}
		errs[i] = nil
	}
	sc.retryOfflineUnits(store.Schedule{ID: "s1"}, units, results, errs, run)
	if runs != 1 {
		t.Fatalf("run called %d times, want 1 (agent-disconnected unit retried once back online)", runs)
	}
	if errs[0] != nil {
		t.Fatalf("err after retry = %v, want nil", errs[0])
	}
}

func TestRetryOfflineUnitsLeavesOtherErrorsAlone(t *testing.T) {
	hub := agenthub.New(agenthub.Handlers{})
	connectSchedulerTestAgent(t, hub, "n1")

	sc := &Scheduler{hub: hub}
	units := []backupUnit{{nodeID: "n1"}}
	results := make([]backup.UnitResult, 1)
	errs := []error{fmt.Errorf("target push failed")}

	runs := 0
	run := func(i int, u backupUnit) { runs++ }
	sc.retryOfflineUnits(store.Schedule{ID: "s1"}, units, results, errs, run)
	if runs != 0 {
		t.Fatalf("run called %d times, want 0 (non-connection errors are not retried)", runs)
	}
}
