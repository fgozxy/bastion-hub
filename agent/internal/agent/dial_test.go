package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDialContextPreferIPv4DialsLiteral(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialContextPreferIPv4(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestDialContextPreferIPv4ResolvesLocalhost(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialContextPreferIPv4(ctx, "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatal(err)
	}
	// Should have connected via 127.0.0.1 (IPv4), not ::1, when both exist.
	if !strings.HasPrefix(conn.RemoteAddr().String(), "127.0.0.1:") {
		t.Fatalf("remote = %s, want 127.0.0.1", conn.RemoteAddr())
	}
	_ = conn.Close()
}

func TestBackoffFirstReconnectIsFast(t *testing.T) {
	if got := backoff(1); got != time.Second {
		t.Fatalf("backoff(1) = %v, want 1s", got)
	}
	if got := backoff(2); got != 2*time.Second {
		t.Fatalf("backoff(2) = %v, want 2s", got)
	}
	if got := backoff(10); got != 30*time.Second {
		t.Fatalf("backoff(10) = %v, want 30s cap", got)
	}
}

func TestIsTransientRegistryErr(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"Get https://ghcr.io: context deadline exceeded", true},
		{"i/o timeout", true},
		{"registry status 429 Too Many Requests", true},
		{"registry status 503: unavailable", true},
		{"registry status 401: unauthorized", false},
		{"registry status 404: not found", false},
		{"registry returned an empty token", false},
	}
	for _, tc := range cases {
		if got := isTransientRegistryErr(errString(tc.err)); got != tc.want {
			t.Errorf("isTransient(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestRegistryManifestDigestRetriesTransient(t *testing.T) {
	attempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/token") || strings.Contains(r.URL.RawQuery, "scope=") {
			// Not used — custom registry returns no token path via parseRef host
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"token":"t"}`))
			return
		}
		attempts++
		if attempts < 3 {
			// Simulate transient failure via hijack close without response? Easier:
			// return 503 twice then success.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("try again"))
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Point the shared client at the test server with its TLS certs, and force
	// the image host to the test server so retries hit our handler thrice.
	old := registryHTTPClient
	registryHTTPClient = server.Client()
	registryHTTPClient.Timeout = 5 * time.Second
	defer func() { registryHTTPClient = old }()

	image := strings.TrimPrefix(server.URL, "https://") + "/team/app:latest"
	got, err := registryManifestDigest(image)
	if err != nil {
		t.Fatal(err)
	}
	if got != "deadbeef" {
		t.Fatalf("digest = %q", got)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}
