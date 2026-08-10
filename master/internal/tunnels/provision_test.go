package tunnels

import (
	"encoding/base64"
	"strings"
	"testing"
)

// realToken builds a base64url-encoded cloudflared connector token whose JSON
// payload carries the given tunnel id ("t" field), mirroring the shape cloudflared
// receives from the Cloudflare API.
func realToken(tunnelID string) string {
	payload := `{"a":"acct","t":"` + tunnelID + `","s":"secret"}`
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func TestDecodeTunnelToken(t *testing.T) {
	const id = "d1943d63-aaaa-bbbb-cccc-dddddddddddd"

	// base64url (no padding) — the common form.
	if got := decodeTunnelToken(realToken(id)); got != id {
		t.Errorf("base64url decode: got %q, want %q", got, id)
	}
	// standard base64 (with padding) fallback.
	std := base64.StdEncoding.EncodeToString([]byte(`{"a":"x","t":"` + id + `"}`))
	if got := decodeTunnelToken(std); got != id {
		t.Errorf("std base64 decode: got %q, want %q", got, id)
	}
	// degenerate inputs → "".
	for _, bad := range []string{"", "   ", "not-base64!@#", "aGVsbG8="} {
		if got := decodeTunnelToken(bad); got != "" {
			t.Errorf("decode(%q) = %q, want empty", bad, got)
		}
	}
}

func TestDockerCtlCmd(t *testing.T) {
	const id = "d1943d63-aaaa-bbbb-cccc-dddddddddddd"
	for _, act := range []string{"start", "stop", "rm"} {
		s := dockerCtlCmd(id, act)
		// The target tunnel id must be wired into the on-node match script.
		if !strings.Contains(s, id) {
			t.Errorf("action %q: script missing tunnel id", act)
		}
		// The docker verb for the action must be present.
		if !strings.Contains(s, "docker "+act) {
			t.Errorf("action %q: missing `docker %s`", act, act)
		}
		// All fmt directives must be consumed (no leftover %).
		if strings.Contains(s, "%") {
			t.Errorf("action %q: leftover %% in script: %s", act, s)
		}
	}
	// rm should stop before removing.
	if rm := dockerCtlCmd(id, "rm"); !strings.Contains(rm, "docker stop") {
		t.Error("rm should stop the container before removing it")
	}
}

func TestDockerCtlCmdBadActionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for unknown action")
		}
	}()
	_ = dockerCtlCmd("x", "bogus")
}

func TestDockerStatusCmdHonorsRunningMapping(t *testing.T) {
	const id = "abc123"
	s := dockerStatusCmd(id)
	if !strings.Contains(s, id) {
		t.Error("status script missing tunnel id")
	}
	// running state must be mapped to the systemd-style "active" token the panel
	// already understands (PROC=active ⇒ green 进程运行中).
	if !strings.Contains(s, `echo active`) {
		t.Error("status script must map running→active")
	}
	if strings.Contains(s, "%") {
		t.Errorf("leftover %% in status script: %s", s)
	}
}
