package agent

import (
	"encoding/json"
	"io"
	"testing"
)

// TestIsBuiltinNetwork covers the filter that keeps built-in networks out of the
// snapshot: bridge/host/none/default and the "container:<id>" network mode are
// skipped (they aren't user-defined networks to recreate), while a compose
// project's network is kept.
func TestIsBuiltinNetwork(t *testing.T) {
	cases := map[string]bool{
		"bridge":           true,
		"host":             true,
		"none":             true,
		"default":          true,
		"container:abc123": true,
		"sub2api_default":  false,
		"npm_default":      false,
		"mynetwork":        false,
	}
	for name, want := range cases {
		if got := isBuiltinNetwork(name); got != want {
			t.Errorf("isBuiltinNetwork(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestBuildCreateBodyEmbedsNetworkAliases verifies that a snapshot's captured
// networks+aliases flow through buildCreateBody into the create payload's
// NetworkingConfig.EndpointsConfig — this is what lets a restored container
// rejoin "sub2api_default" and resolve the "postgres"/"redis" service names.
func TestBuildCreateBodyEmbedsNetworkAliases(t *testing.T) {
	cfg := map[string]json.RawMessage{
		"Image": json.RawMessage(`"weishaw/sub2api:latest"`),
	}
	host := json.RawMessage(`{"NetworkMode":"sub2api_default"}`)
	nets := map[string]json.RawMessage{
		"sub2api_default": json.RawMessage(`{"Aliases":["postgres","sub2api-postgres"]}`),
	}

	b, err := io.ReadAll(buildCreateBody(cfg, host, nets))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body struct {
		NetworkingConfig struct {
			EndpointsConfig map[string]struct {
				Aliases []string `json:"Aliases"`
			} `json:"EndpointsConfig"`
		} `json:"NetworkingConfig"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	ep, ok := body.NetworkingConfig.EndpointsConfig["sub2api_default"]
	if !ok {
		t.Fatalf("EndpointsConfig missing sub2api_default, body=%s", b)
	}
	want := map[string]bool{"postgres": false, "sub2api-postgres": false}
	for _, a := range ep.Aliases {
		want[a] = true
	}
	for alias, found := range want {
		if !found {
			t.Errorf("missing alias %q, aliases=%v body=%s", alias, ep.Aliases, b)
		}
	}
}

// TestBuildCreateBodyOmitsNetworkingConfigWhenEmpty is the backward-compat path:
// an old snapshot with no networks must not synthesize an empty NetworkingConfig,
// so legacy restores behave exactly as before.
func TestBuildCreateBodyOmitsNetworkingConfigWhenEmpty(t *testing.T) {
	cfg := map[string]json.RawMessage{"Image": json.RawMessage(`"busybox"`)}
	host := json.RawMessage(`{"NetworkMode":"bridge"}`)
	b, err := io.ReadAll(buildCreateBody(cfg, host, nil))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body["NetworkingConfig"]; ok {
		t.Errorf("NetworkingConfig should be absent for empty nets, body=%s", b)
	}
}
