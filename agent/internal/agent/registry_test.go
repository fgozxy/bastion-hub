package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryManifestDigestStrictResponses(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		digest     string
		body       string
		want       string
		wantErrSub string
	}{
		{name: "success", status: http.StatusOK, digest: "sha256:abc123", want: "abc123"},
		{name: "status", status: http.StatusTooManyRequests, body: "rate limited", wantErrSub: "429"},
		{name: "empty digest", status: http.StatusOK, wantErrSub: "empty Docker-Content-Digest"},
		{name: "invalid digest", status: http.StatusOK, digest: "not-a-digest", wantErrSub: "invalid Docker-Content-Digest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/team/app/manifests/latest" {
					t.Errorf("manifest path = %q", r.URL.Path)
				}
				if tc.digest != "" {
					w.Header().Set("Docker-Content-Digest", tc.digest)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			image := strings.TrimPrefix(server.URL, "https://") + "/team/app:latest"
			got, err := registryManifestDigestWithClient(server.Client(), image)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("digest = %q, err = %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRegistryManifestDigestRejectsEmptyImage(t *testing.T) {
	if _, err := registryManifestDigestWithClient(http.DefaultClient, "  "); err == nil {
		t.Fatal("empty image reference was accepted")
	}
}

func TestFetchRegistryTokenStrictResponses(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		want       string
		wantErrSub string
	}{
		{name: "token", status: http.StatusOK, body: `{"token":"abc"}`, want: "abc"},
		{name: "access token", status: http.StatusOK, body: `{"access_token":"xyz"}`, want: "xyz"},
		{name: "status", status: http.StatusUnauthorized, body: "denied", wantErrSub: "401"},
		{name: "invalid json", status: http.StatusOK, body: `{`, wantErrSub: "decode registry token"},
		{name: "empty token", status: http.StatusOK, body: `{}`, wantErrSub: "empty token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			got, err := fetchRegistryToken(server.Client(), server.URL)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("token = %q, err = %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestParseRefNormalizesDockerHubLibraryImages(t *testing.T) {
	for _, ref := range []string{"nginx", "nginx:latest", "docker.io/nginx:latest", "registry-1.docker.io/nginx:latest"} {
		reg, repo, tag := parseRef(ref)
		if reg != "docker.io" || repo != "library/nginx" || tag != "latest" {
			t.Errorf("parseRef(%q) = %q, %q, %q", ref, reg, repo, tag)
		}
	}
}

func TestRegistryListTagsFollowsPagination(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("n"); got != "100" {
			t.Errorf("page size = %q, want 100", got)
		}
		switch r.URL.Query().Get("last") {
		case "":
			w.Header().Set("Link", `</v2/team/app/tags/list?n=100&last=v2.0.46>; rel="next"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"latest", "v2.0.46"}})
		case "v2.0.46":
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"v2.0.46", "v3.0.5"}})
		default:
			t.Errorf("unexpected page URL %s", r.URL)
		}
	}))
	defer server.Close()

	image := strings.TrimPrefix(server.URL, "https://") + "/team/app:v3.0.2"
	got, err := registryListTagsWithClient(server.Client(), image)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"latest", "v2.0.46", "v3.0.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %#v, want %#v", got, want)
	}
}

func TestRegistryListTagsRejectsCrossOriginPagination(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://attacker.example/tags>; rel="next"`)
		_, _ = w.Write([]byte(`{"tags":["v1.0.0"]}`))
	}))
	defer server.Close()

	image := strings.TrimPrefix(server.URL, "https://") + "/team/app:v1.0.0"
	_, err := registryListTagsWithClient(server.Client(), image)
	if err == nil || !strings.Contains(err.Error(), "changed registry endpoint") {
		t.Fatalf("error = %v, want rejected cross-origin pagination", err)
	}
}

func TestRegistryListTagsFailsWhenLaterPageFails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("last") == "v1.0.0" {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		w.Header().Set("Link", `</v2/team/app/tags/list?n=100&last=v1.0.0>; rel="next"`)
		_, _ = w.Write([]byte(`{"tags":["v1.0.0"]}`))
	}))
	defer server.Close()

	image := strings.TrimPrefix(server.URL, "https://") + "/team/app:v1.0.0"
	_, err := registryListTagsWithClient(server.Client(), image)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v, want second-page failure", err)
	}
}
