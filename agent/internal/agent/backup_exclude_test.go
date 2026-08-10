package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExcluded(t *testing.T) {
	cases := []struct {
		path     string
		excludes []string
		want     bool
	}{
		{"/var/lib/nodepanel", []string{"/var/lib/nodepanel/backups"}, false},
		{"/var/lib/nodepanel/backups", []string{"/var/lib/nodepanel/backups"}, true},
		{"/var/lib/nodepanel/backups/x.tar.gz", []string{"/var/lib/nodepanel/backups"}, true},
		{"/var/lib/nodepanel/db", []string{"/var/lib/nodepanel/backups"}, false},
		{"/var/lib/nodepanel/backups2", []string{"/var/lib/nodepanel/backups"}, false}, // sibling, not beneath
		{"/x", []string{}, false},
		{"/a/b", []string{"/a/b/"}, true},  // trailing slash cleaned
		{"/a/bc", []string{"/a/b"}, false}, // prefix-but-not-directory boundary
	}
	for _, c := range cases {
		if got := excluded(c.path, c.excludes); got != c.want {
			t.Errorf("excluded(%q, %v)=%v want %v", c.path, c.excludes, got, c.want)
		}
	}
}

// TestAddPathPrefixExcludesSubtree builds a real tree, tars it with an exclude,
// and confirms the excluded subtree's files are absent while kept files remain.
func TestAddPathPrefixExcludesSubtree(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "keep"), 0o755)
	os.MkdirAll(filepath.Join(root, "skip", "nested"), 0o755)
	os.WriteFile(filepath.Join(root, "keep", "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(root, "skip", "big.dat"), []byte("bbbb"), 0o644)
	os.WriteFile(filepath.Join(root, "skip", "nested", "c.dat"), []byte("c"), 0o644)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := addPathPrefix(tw, root, "vol", []string{filepath.Join(root, "skip")}); err != nil {
		t.Fatalf("addPathPrefix: %v", err)
	}
	tw.Close()
	gw.Close()

	gr, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gr)
	hasA, hasBig, hasNested := false, false, false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		switch {
		case strings.HasSuffix(h.Name, "a.txt"):
			hasA = true
		case strings.HasSuffix(h.Name, "big.dat"):
			hasBig = true
		case strings.HasSuffix(h.Name, "c.dat"):
			hasNested = true
		}
	}
	if !hasA {
		t.Error("keep/a.txt should be present")
	}
	if hasBig || hasNested {
		t.Errorf("excluded subtree leaked into archive (big=%v nested=%v)", hasBig, hasNested)
	}
}
