package agent

import "testing"

func TestParseSemverTag(t *testing.T) {
	cases := []struct {
		in  string
		ok  bool
		maj int
		pre string
	}{
		{"v3.0.1", true, 3, ""},
		{"3.0.0", true, 3, ""},
		{"v2.0.4.rc3", true, 2, "rc3"},
		{"latest", false, 0, ""},
		{"edge", false, 0, ""},
		{"sha-abc", false, 0, ""},
		{"v3.0.1-amd64", false, 0, ""},
		{"main", false, 0, ""},
	}
	for _, tc := range cases {
		sv, ok := parseSemverTag(tc.in)
		if ok != tc.ok {
			t.Fatalf("%s: ok=%v want %v", tc.in, ok, tc.ok)
		}
		if ok && sv.Major != tc.maj {
			t.Fatalf("%s: major=%d want %d", tc.in, sv.Major, tc.maj)
		}
		if ok && sv.Pre != tc.pre {
			t.Fatalf("%s: pre=%q want %q", tc.in, sv.Pre, tc.pre)
		}
	}
}

func TestHighestSemverTag(t *testing.T) {
	tags := []string{"latest", "main", "v2.0.4", "v3.0.0", "v3.0.1", "v3.0.1-amd64", "sha-dead", "v2.0.4.rc9"}
	got := highestSemverTag(tags)
	if got != "v3.0.1" {
		t.Fatalf("highest=%q want v3.0.1", got)
	}
	// pre-release loses to release of same version
	got = highestSemverTag([]string{"v1.0.0-rc1", "v1.0.0"})
	if got != "v1.0.0" {
		t.Fatalf("highest=%q want v1.0.0", got)
	}
}

func TestHighestCompatibleSemverTagPreservesMajorAndVariant(t *testing.T) {
	tests := []struct {
		current string
		tags    []string
		want    string
	}{
		{current: "v3.0.2", tags: []string{"v2.0.46", "v3.0.3", "v3.0.5", "v4.0.0"}, want: "v3.0.5"},
		{current: "3.12-slim", tags: []string{"3.15.0rc1-slim", "3.14-windowsservercore", "3.13-slim", "4.0-slim"}, want: "3.13-slim"},
		{current: "18-alpine", tags: []string{"18.2", "18.1-alpine", "18.3-windowsservercore"}, want: "18.1-alpine"},
		{current: "3.10.12-1", tags: []string{"3.10.13", "3.10.13-1", "3.10.12-2"}, want: "3.10.13-1"},
	}
	for _, tc := range tests {
		if got := highestCompatibleSemverTag(tc.current, tc.tags); got != tc.want {
			t.Errorf("highestCompatibleSemverTag(%q) = %q, want %q", tc.current, got, tc.want)
		}
	}
}

// TestParseSemverTagShortTags locks in the relaxed regex: 1- and 2-part numeric
// tags must parse (missing segments = 0) so registries like postgres — which ship
// modern releases as 2-part tags (18, 17.5) — are ranked instead of falling back
// to ancient 3-part tags (9.6.24) as the "newest".
func TestParseSemverTagShortTags(t *testing.T) {
	cases := []struct {
		in  string
		ok  bool
		maj int
		min int
		pre string
	}{
		{"18", true, 18, 0, ""},
		{"17.5", true, 17, 5, ""},
		{"16.4-alpine", true, 16, 4, "alpine"},
		{"18-alpine", true, 18, 0, "alpine"},
		{"9.6.24", true, 9, 6, ""},
		// Full 3-part tags still work unchanged.
		{"3.0.1", true, 3, 0, ""},
	}
	for _, tc := range cases {
		sv, ok := parseSemverTag(tc.in)
		if ok != tc.ok {
			t.Fatalf("%s: ok=%v want %v", tc.in, ok, tc.ok)
		}
		if ok && (sv.Major != tc.maj || sv.Minor != tc.min || sv.Pre != tc.pre) {
			t.Fatalf("%s: %+v want maj=%d min=%d pre=%q", tc.in, sv, tc.maj, tc.min, tc.pre)
		}
	}
}

// TestHighestSemverTagPrefersModernShortTag is the regression test for the
// postgres:18-alpine → 9.6.24 incident: with a realistic postgres tag list, the
// newest must be a major-18 tag, never 9.6.24.
func TestHighestSemverTagPrefersModernShortTag(t *testing.T) {
	tags := []string{"latest", "bookworm", "18", "18-alpine", "17.5", "17.5-alpine", "16.4", "9.6.24", "9.6.24-alpine"}
	got := highestSemverTag(tags)
	if got == "" {
		t.Fatal("highestSemverTag returned empty for a populated tag list")
	}
	sv, ok := parseSemverTag(got)
	if !ok || sv.Major != 18 {
		t.Fatalf("highest=%q (major %d), want a major-18 tag — not the ancient 9.6.24", got, sv.Major)
	}
	if got == "9.6.24" {
		t.Fatalf("highest=%q, must not be the ancient 3-part downgrade target", got)
	}
}

func TestImageRepoTag(t *testing.T) {
	if got := imageRepoTag("ghcr.io/chenyme/grok2api:latest", "v3.0.1"); got != "ghcr.io/chenyme/grok2api:v3.0.1" {
		t.Fatal(got)
	}
	if got := imageRepoTag("nginx:alpine", "1.27.0"); got != "nginx:1.27.0" {
		t.Fatal(got)
	}
}

func TestStripDigest(t *testing.T) {
	if got := stripDigest("foo:latest@sha256:abc"); got != "foo:latest" {
		t.Fatal(got)
	}
	reg, repo, tag := parseRef("ghcr.io/x/y:latest@sha256:deadbeef")
	if reg != "ghcr.io" || repo != "x/y" || tag != "latest" {
		t.Fatalf("%s %s %s", reg, repo, tag)
	}
}
