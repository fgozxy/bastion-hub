package backup

import (
	"context"
	"os"
	"testing"

	"nodepanel/master/internal/cloudflare"
)

// TestSourceContainerDomains verifies the domain-detection helper the
// "tunnel-less destination" footgun fix relies on, against a live tunnel
// (read-only GET). Skipped when NP_CF_TOKEN or NP_CF_TUNNEL_ID is unset.
func TestSourceContainerDomains(t *testing.T) {
	token := os.Getenv("NP_CF_TOKEN")
	tunnelID := os.Getenv("NP_CF_TUNNEL_ID")
	panelDomain := os.Getenv("NP_PANEL_DOMAIN")
	if token == "" || tunnelID == "" || panelDomain == "" {
		t.Skip("NP_CF_TOKEN, NP_CF_TUNNEL_ID, NP_PANEL_DOMAIN not set")
	}
	cf := cloudflare.New(token)

	// 1) a container bound to the panel port → should be reported as having the panel domain.
	hosts, err := sourceContainerDomains(context.Background(), cf, tunnelID, map[string]string{"8088/tcp": "8088"})
	if err != nil {
		t.Fatalf("sourceContainerDomains: %v", err)
	}
	t.Logf("port 8088 -> %v", hosts)
	if !contains(hosts, panelDomain) {
		t.Fatalf("expected %s in %v", panelDomain, hosts)
	}

	// 2) a container bound to a port nothing fronts → no domain (safe to migrate
	//    to a tunnel-less node).
	hosts2, err := sourceContainerDomains(context.Background(), cf, tunnelID, map[string]string{"9999/tcp": "9999"})
	if err != nil {
		t.Fatalf("sourceContainerDomains(9999): %v", err)
	}
	if len(hosts2) != 0 {
		t.Fatalf("expected no domain for port 9999, got %v", hosts2)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
