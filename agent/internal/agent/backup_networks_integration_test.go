//go:build integration

// End-to-end check of the compose-network recreate fix, against a real Docker
// socket. Built only under the "integration" tag so the normal `go test` run
// (e.g. in CI without a docker socket) is unaffected. Run with:
//
//	go test -tags integration -run TestRecreateContainerRebuildsUserNetwork -v ./agent/internal/agent/
package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// hasString reports whether v is in s (a slice membership helper local to tests,
// named to avoid colliding with anything in the package).
func hasString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestRecreateContainerRebuildsUserNetwork reproduces the sub2api failure in
// miniature: a container that lives on a user-defined network (with a service-
// name alias) is snapshotted, then both the container and its network are
// removed — emulating a fresh target node. recreateContainer must then rebuild
// the network and re-attach the alias on its own; if it falls back to the
// default bridge (the old bug), the alias is gone and the assertions fail.
func TestRecreateContainerRebuildsUserNetwork(t *testing.T) {
	dc := newDocker()
	// Bail out cleanly where there's no docker socket (CI without docker).
	if resp, err := dc.req(http.MethodGet, "/version", nil); err != nil {
		t.Skip("no docker socket available:", err)
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	const (
		netName     = "np-integ-default"
		dbContainer = "np-integ-db"
		image       = "busybox:latest"
	)

	// pull the image up front (tiny); skip if the host is offline.
	if err := dc.pullImage("busybox", "latest"); err != nil {
		t.Skip("cannot pull busybox (offline host?):", err)
	}

	cleanup := func() {
		for _, c := range []string{dbContainer} {
			if r, err := dc.req(http.MethodDelete, "/containers/"+c+"?force=true&v=true", nil); err == nil {
				_, _ = io.Copy(io.Discard, r.Body)
				r.Body.Close()
			}
		}
		if r, err := dc.req(http.MethodDelete, "/networks/"+netName, nil); err == nil {
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
	}
	cleanup() // sweep any leftover from a prior aborted run
	t.Cleanup(cleanup)

	// 1) Create the user-defined network.
	body, _ := json.Marshal(map[string]any{"Name": netName, "Driver": "bridge"})
	r, err := dc.req(http.MethodPost, "/networks/create", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	if r.StatusCode >= 400 {
		rb, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Fatalf("create network %s: %s %s", netName, r.Status, rb)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()

	// 2) Create a container on that network carrying the "db" service-name alias
	//    (mirrors how compose registers service names).
	createBody, _ := json.Marshal(map[string]any{
		"Image":      image,
		"Cmd":        []string{"sleep", "infinity"},
		"HostConfig": map[string]any{"NetworkMode": netName},
		"NetworkingConfig": map[string]any{
			"EndpointsConfig": map[string]any{
				netName: map[string]any{"Aliases": []string{"db"}},
			},
		},
	})
	r, err = dc.req(http.MethodPost, "/containers/create?name="+dbContainer, strings.NewReader(string(createBody)))
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	rb, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode >= 400 {
		t.Fatalf("create container %s: %s %s", dbContainer, r.Status, rb)
	}
	if r, err := dc.req(http.MethodPost, "/containers/"+dbContainer+"/start", nil); err == nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}

	// 3) Snapshot it — the new containerBackupMeta must capture the network +
	//    its aliases (this is the field the old code never recorded).
	_, meta, err := containerBackupMeta(dbContainer)
	if err != nil {
		t.Fatalf("containerBackupMeta: %v", err)
	}
	var snap struct {
		Name       string                     `json:"name"`
		Image      string                     `json:"image"`
		Config     json.RawMessage            `json:"config"`
		HostConfig json.RawMessage            `json:"host_config"`
		Networks   map[string]json.RawMessage `json:"networks"`
	}
	if err := json.Unmarshal(meta, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	epRaw, ok := snap.Networks[netName]
	if !ok {
		t.Fatalf("snapshot did not capture network %q (networks=%v) — aliases lost", netName, snap.Networks)
	}
	var ep struct {
		Aliases []string `json:"Aliases"`
	}
	_ = json.Unmarshal(epRaw, &ep)
	if !hasString(ep.Aliases, "db") {
		t.Fatalf("snapshot for %s missing 'db' alias: %v", netName, ep.Aliases)
	}
	t.Logf("captured network %s with aliases %v", netName, ep.Aliases)

	// 4) Emulate a fresh target node: remove the container AND its network.
	if r, err := dc.req(http.MethodDelete, "/containers/"+dbContainer+"?force=true&v=true", nil); err == nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	if r, err := dc.req(http.MethodDelete, "/networks/"+netName, nil); err == nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	if r, err := dc.req(http.MethodGet, "/networks/"+netName, nil); err == nil {
		if r.StatusCode == 200 {
			r.Body.Close()
			t.Fatalf("network %s should be gone before recreate", netName)
		}
		r.Body.Close()
	}

	// 5) Recreate with the NEW logic. ensureNetworks must rebuild np-integ-default
	//    and the container must come back on it with its "db" alias.
	detail, recreated, _, rerr := recreateContainer(snap.Name, snap.Image, snap.Config, snap.HostConfig, snap.Networks, true)
	if rerr != nil {
		t.Fatalf("recreateContainer failed: %v (detail=%s)", rerr, detail)
	}
	if !recreated {
		t.Fatalf("expected recreated=true, got detail=%s", detail)
	}
	t.Logf("recreate detail: %s", detail)

	// 6) Verify the network was recreated.
	r, err = dc.req(http.MethodGet, "/networks/"+netName, nil)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("network %s not recreated by ensureNetworks (err=%v)", netName, err)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()

	// 7) Verify the recreated container is running, on the network, with the alias.
	ir, err := dc.req(http.MethodGet, "/containers/"+dbContainer+"/json", nil)
	if err != nil {
		t.Fatalf("inspect recreated container: %v", err)
	}
	ib, _ := io.ReadAll(ir.Body)
	ir.Body.Close()
	var ins struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases []string `json:"Aliases"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(ib, &ins); err != nil {
		t.Fatalf("unmarshal inspect: %v", err)
	}
	if !ins.State.Running {
		t.Errorf("recreated container is not running")
	}
	netEP, attached := ins.NetworkSettings.Networks[netName]
	if !attached {
		t.Fatalf("recreated container not attached to %s (fell back to bridge? networks=%v)", netName, ins.NetworkSettings.Networks)
	}
	if !hasString(netEP.Aliases, "db") {
		t.Errorf("recreated container lost 'db' alias on %s, aliases=%v", netName, netEP.Aliases)
	}
	t.Logf("OK: recreated container running=%v on %s with aliases %v", ins.State.Running, netName, netEP.Aliases)
}
