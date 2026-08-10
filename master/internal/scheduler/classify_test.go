package scheduler

import (
	"testing"

	"nodepanel/shared/proto"
)

// TestClassifyUpdateCandidatesDriftSkipped is the core of the version-driven
// policy: an item that reports HasUpdate==1 but has no SuggestedImage is same-tag
// content drift (force-push / legacy fixed tag). It must NOT become a candidate —
// otherwise the scheduler would apply it every cron tick and spam Telegram.
func TestClassifyUpdateCandidatesDriftSkipped(t *testing.T) {
	items := []proto.ContainerScanItem{
		{
			Name:           "komari",
			Image:          "glz/komari:1.2.5",
			UpdateType:     "tag",
			HasUpdate:      1, // content drifted...
			SuggestedImage: "", // ...but no version to upgrade to
		},
	}
	descs, out := classifyUpdateCandidates(items)
	if len(descs) != 0 {
		t.Fatalf("drift item must not be a candidate; got %d descs: %+v", len(descs), descs)
	}
	if out.candidates != 0 {
		t.Fatalf("out.candidates = %d, want 0 (drift skipped)", out.candidates)
	}
	if out.skipped != 1 {
		t.Fatalf("out.skipped = %d, want 1 (drift counted as skipped)", out.skipped)
	}
	if out.updated != 0 {
		t.Fatalf("out.updated = %d, want 0", out.updated)
	}
}

func TestClassifyUpdateCandidatesVersionUpgrade(t *testing.T) {
	items := []proto.ContainerScanItem{
		{
			Name:           "grok2api",
			Image:          "ghcr.io/chenyme/grok2api:3.0.0",
			State:          "running",
			UpdateType:     "tag",
			HasUpdate:      1,
			SuggestedImage: "ghcr.io/chenyme/grok2api:3.0.1",
		},
	}
	descs, out := classifyUpdateCandidates(items)
	if len(descs) != 1 {
		t.Fatalf("version bump must be a candidate; got %d descs", len(descs))
	}
	d := descs[0]
	if d.name != "grok2api" {
		t.Errorf("name = %q, want grok2api", d.name)
	}
	if d.suggested != "ghcr.io/chenyme/grok2api:3.0.1" {
		t.Errorf("suggested = %q", d.suggested)
	}
	if d.from != "3.0.0" || d.to != "3.0.1" {
		t.Errorf("from/to = %q → %q, want 3.0.0 → 3.0.1", d.from, d.to)
	}
	if out.candidates != 1 {
		t.Errorf("out.candidates = %d, want 1", out.candidates)
	}
}

func TestClassifyUpdateCandidatesUpToDate(t *testing.T) {
	items := []proto.ContainerScanItem{
		{Name: "chatgpt2api", Image: "ghcr.io/basketikun/chatgpt2api:latest", UpdateType: "latest", HasUpdate: 0},
	}
	descs, out := classifyUpdateCandidates(items)
	if len(descs) != 0 {
		t.Fatalf("up-to-date item must not be a candidate; got %d", len(descs))
	}
	if out.unchanged != 1 {
		t.Errorf("out.unchanged = %d, want 1", out.unchanged)
	}
}

func TestClassifyUpdateCandidatesNotRunningSkipped(t *testing.T) {
	items := []proto.ContainerScanItem{
		{Name: "x", Image: "r/x:1.0.0", State: "exited", UpdateType: "tag", HasUpdate: 1, SuggestedImage: "r/x:1.0.1"},
	}
	descs, out := classifyUpdateCandidates(items)
	if len(descs) != 0 {
		t.Fatalf("non-running candidate must be skipped; got %d", len(descs))
	}
	if out.skipped != 1 {
		t.Errorf("out.skipped = %d, want 1", out.skipped)
	}
}

func TestClassifyUpdateCandidatesNonEligibleTypeSkipped(t *testing.T) {
	for _, ut := range []string{"build", "local", "pinned", "unmanaged"} {
		items := []proto.ContainerScanItem{
			{Name: "locallybuilt", Image: "x:latest", UpdateType: ut, HasUpdate: 1, SuggestedImage: "x:9.9.9"},
		}
		descs, out := classifyUpdateCandidates(items)
		if len(descs) != 0 {
			t.Errorf("update_type=%q must be skipped, got candidate", ut)
		}
		if out.skipped != 1 {
			t.Errorf("update_type=%q: out.skipped = %d, want 1", ut, out.skipped)
		}
	}
}

func TestImageTagOf(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ghcr.io/chenyme/grok2api:3.0.1", "3.0.1"},
		{"ghcr.io/chenyme/grok2api:3.0.0", "3.0.0"},
		{"library/nginx:1.27-alpine", "1.27-alpine"},
		{"localhost:5000/repo/img:nightly", "nightly"}, // port-bearing registry
		{"nginx", ""},             // no tag
		{"nginx:latest", "latest"},
		{"", ""},
	}
	for _, c := range cases {
		if got := imageTagOf(c.in); got != c.want {
			t.Errorf("imageTagOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
