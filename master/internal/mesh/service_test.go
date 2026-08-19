package mesh

import (
	"reflect"
	"strings"
	"testing"

	"nodepanel/master/internal/store"
)

func TestNormalizeSourceCIDRs(t *testing.T) {
	got, err := normalizeSourceCIDRs([]string{
		"203.0.113.9, 198.51.100.99/24",
		"2001:db8::1 2001:db8:1::9/64",
		"203.0.113.9/32",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"198.51.100.0/24",
		"2001:db8:1::/64",
		"2001:db8::1/128",
		"203.0.113.9/32",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeSourceCIDRs() = %#v, want %#v", got, want)
	}
}

func TestNormalizeSourceCIDRsRejectsInvalid(t *testing.T) {
	if _, err := normalizeSourceCIDRs([]string{"203.0.113.1", "not-an-ip"}); err == nil {
		t.Fatal("expected invalid source error")
	}
}

func TestAccessConfigSourcesForNode(t *testing.T) {
	defaults := []string{"192.0.2.1/32"}
	custom := []string{"198.51.100.7/32"}
	cfg := AccessConfig{Enabled: true, NodeIDs: []string{"node-a"}, SourceCIDRs: custom}
	if got := cfg.sourcesForNode("node-a", defaults); !reflect.DeepEqual(got, custom) {
		t.Fatalf("selected node sources = %#v", got)
	}
	if got := cfg.sourcesForNode("node-b", defaults); !reflect.DeepEqual(got, defaults) {
		t.Fatalf("unselected node sources = %#v", got)
	}
	cfg.Enabled = false
	if got := cfg.sourcesForNode("node-a", defaults); !reflect.DeepEqual(got, defaults) {
		t.Fatalf("disabled config sources = %#v", got)
	}
}

func TestMeshAddressesIncludesDualStack(t *testing.T) {
	nodes := []store.Node{
		{IPv4: "203.0.113.10", IPv6: "2001:db8::10"},
		{IPv4: "203.0.113.10"},
	}
	want := []string{"2001:db8::10/128", "203.0.113.10/32"}
	if got := meshAddresses(nodes); !reflect.DeepEqual(got, want) {
		t.Fatalf("meshAddresses() = %#v, want %#v", got, want)
	}
}

func TestMeshFirewallScriptIsTransactionalAndDualStack(t *testing.T) {
	for _, fragment := range []string{
		"delete table inet nodepanel_mesh",
		"set allowed_v4",
		"set allowed_v6",
		"nft -f \"$tmp\"",
	} {
		if !strings.Contains(meshFirewallApplyScript, fragment) {
			t.Fatalf("firewall apply script missing %q", fragment)
		}
	}
	if !strings.Contains(meshFirewallUnit, "ExecStart=/usr/local/lib/nodepanel/mesh-firewall-apply") {
		t.Fatal("firewall unit does not invoke the apply script")
	}
}
