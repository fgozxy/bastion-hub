package targets

import (
	"encoding/json"
	"strings"
	"testing"

	"nodepanel/master/internal/store"
)

func TestS3ObjectPrefixJoin(t *testing.T) {
	cases := []struct {
		prefix, remote, want string
	}{
		{"", "node1/abc.tar.gz", "node1/abc.tar.gz"},
		{"nodepanel", "node1/abc.tar.gz", "nodepanel/node1/abc.tar.gz"},
		{"nodepanel/", "node1/abc.tar.gz", "nodepanel/node1/abc.tar.gz"},
		{"a/b", "c/d.tar.gz", "a/b/c/d.tar.gz"},
	}
	for _, c := range cases {
		s := &S3{cfg: S3Config{Prefix: c.prefix}}
		if got := s.object(c.remote); got != c.want {
			t.Errorf("prefix=%q remote=%q: got %q want %q", c.prefix, c.remote, got, c.want)
		}
	}
}

func TestS3ClientValidation(t *testing.T) {
	// Missing endpoint or bucket must fail fast rather than building a client
	// that only errors on first use.
	for _, cfg := range []S3Config{
		{Bucket: "b"},
		{Endpoint: "https://x"},
		{},
	} {
		s := &S3{cfg: cfg}
		if _, err := s.client(); err == nil {
			t.Fatalf(" %+v: expected validation error, got nil", cfg)
		}
	}
	// A complete config builds a client without touching the network.
	s := &S3{cfg: S3Config{Endpoint: "https://minio.example", AccessKey: "ak", SecretKey: "sk", Bucket: "b"}}
	if _, err := s.client(); err != nil {
		t.Fatalf("client build: %v", err)
	}
}

func TestS3EndpointSchemeStripsAndSetsSecure(t *testing.T) {
	// Both scheme forms must construct a client without error — the scheme is
	// stripped before minio.New and drives Secure. (minio.Client doesn't expose
	// the secure flag, so we assert only that construction succeeds.)
	for _, endpoint := range []string{"https://minio.example", "http://1.2.3.4:9000"} {
		s := &S3{cfg: S3Config{Endpoint: endpoint, AccessKey: "ak", SecretKey: "sk", Bucket: "b"}}
		if _, err := s.client(); err != nil {
			t.Errorf("endpoint %q: client build failed: %v", endpoint, err)
		}
	}
}

// TestNewS3Factory confirms the factory parses an s3 target's config JSON into an
// S3 Uploader.
func TestNewS3Factory(t *testing.T) {
	cfg := S3Config{Endpoint: "https://minio.example", AccessKey: "ak", SecretKey: "sk", Bucket: "bk", Prefix: "np"}
	raw, _ := json.Marshal(cfg)
	up, err := New(&store.BackupTarget{ID: "t1", Type: "s3", Name: "minio", Config: string(raw)}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s, ok := up.(*S3)
	if !ok {
		t.Fatalf("expected *S3, got %T", up)
	}
	if s.cfg.Bucket != "bk" || !strings.HasPrefix(s.cfg.Endpoint, "https://") {
		t.Fatalf("config not parsed: %+v", s.cfg)
	}
}
