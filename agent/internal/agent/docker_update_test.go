package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nodepanel/shared/proto"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func dockerJSONResponse(status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func TestScanContainersIsReadOnlyDeduplicatedAndBounded(t *testing.T) {
	const uniqueImages = 10
	type listedContainer struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		ImageID string            `json:"ImageID"`
		State   string            `json:"State"`
		Labels  map[string]string `json:"Labels"`
	}

	containers := make([]listedContainer, 0, uniqueImages+2)
	digestsByID := make(map[string][]string)
	remoteByRef := make(map[string]string)
	configuredRefByID := make(map[string]string)
	for i := 0; i < uniqueImages; i++ {
		ref := fmt.Sprintf("registry.example/team/image-%d:v1", i)
		id := fmt.Sprintf("sha256:image-id-%d", i)
		containerID := fmt.Sprintf("container-id-%d", i)
		remote := fmt.Sprintf("remote-%d", i)
		local := remote
		if i == 1 {
			local = "old-1"
		}
		containers = append(containers, listedContainer{
			ID: containerID, Names: []string{fmt.Sprintf("/container-%d", i)}, Image: ref, ImageID: id, State: "running",
		})
		configuredRefByID[containerID] = ref
		if i != 2 { // i=2 gets no RepoDigest → treated as locally built → reclassified local (HasUpdate stays -1, not up-to-date).
			digestsByID[id] = []string{
				"registry.example/other/image@sha256:wrong-repository",
				fmt.Sprintf("registry.example/team/image-%d@sha256:%s", i, local),
			}
		}
		remoteByRef[ref] = remote
	}
	// A second container running the same ref must reuse the registry lookup,
	// and the same running image ID must reuse the local image inspection.
	containers = append(containers, listedContainer{
		ID: "same-image-container", Names: []string{"/same-image-id"}, Image: containers[0].Image, ImageID: containers[0].ImageID, State: "running",
	})
	configuredRefByID["same-image-container"] = containers[0].Image
	// A different running image ID under that same ref still needs its own local
	// digest lookup, but not another registry request.
	containers = append(containers, listedContainer{
		ID: "duplicate-container", Names: []string{"/duplicate"}, Image: containers[0].Image, ImageID: "sha256:duplicate-id", State: "exited",
	})
	configuredRefByID["duplicate-container"] = containers[0].Image
	digestsByID["sha256:duplicate-id"] = []string{"registry.example/team/image-0@sha256:remote-0"}

	var dockerMu sync.Mutex
	var dockerMethods []string
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		dockerMu.Lock()
		dockerMethods = append(dockerMethods, req.Method+" "+req.URL.Path)
		dockerMu.Unlock()
		if req.Method != http.MethodGet {
			return dockerJSONResponse(http.StatusMethodNotAllowed, map[string]string{"error": "write attempted"}), nil
		}
		if req.URL.Path == "/containers/json" {
			return dockerJSONResponse(http.StatusOK, containers), nil
		}
		if strings.HasPrefix(req.URL.Path, "/containers/") && strings.HasSuffix(req.URL.Path, "/json") {
			containerID := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/containers/"), "/json")
			ref, ok := configuredRefByID[containerID]
			if !ok {
				return dockerJSONResponse(http.StatusNotFound, map[string]string{"message": "unknown container"}), nil
			}
			return dockerJSONResponse(http.StatusOK, map[string]any{
				"Name": "/" + containerID, "Config": map[string]string{"Image": ref},
			}), nil
		}
		if strings.HasPrefix(req.URL.Path, "/images/") && strings.HasSuffix(req.URL.Path, "/json") {
			id := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/images/"), "/json")
			digests, ok := digestsByID[id]
			if !ok && id != "sha256:image-id-2" {
				return dockerJSONResponse(http.StatusNotFound, map[string]string{"message": "unknown image id"}), nil
			}
			return dockerJSONResponse(http.StatusOK, map[string]any{"RepoDigests": digests}), nil
		}
		return dockerJSONResponse(http.StatusNotFound, map[string]string{"message": "unexpected path"}), nil
	})}}

	// No semver tags → fall back to same-tag digest compare (legacy path).
	oldList := listTagsFn
	listTagsFn = func(string) ([]string, error) { return nil, nil }
	t.Cleanup(func() { listTagsFn = oldList })

	var active, maxActive, calls atomic.Int32
	registryLookup := func(ref string) (string, error) {
		calls.Add(1)
		now := active.Add(1)
		for {
			old := maxActive.Load()
			if now <= old || maxActive.CompareAndSwap(old, now) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(10 * time.Millisecond)
		if strings.HasSuffix(ref, "image-3:v1") {
			return "", fmt.Errorf("registry unavailable")
		}
		return remoteByRef[ref], nil
	}

	result := scanContainers(dc, registryLookup)
	if !result.OK || result.Err != "" {
		t.Fatalf("scan result = OK %v, err %q", result.OK, result.Err)
	}
	if got := calls.Load(); got != uniqueImages {
		t.Fatalf("registry calls = %d, want %d unique image refs", got, uniqueImages)
	}
	if got := maxActive.Load(); got > registryScanConcurrency {
		t.Fatalf("registry concurrency = %d, limit %d", got, registryScanConcurrency)
	} else if got < 2 {
		t.Fatalf("registry checks did not run concurrently; max = %d", got)
	}
	if result.Items[0].HasUpdate != 0 || result.Items[1].HasUpdate != 1 || result.Items[2].HasUpdate != -1 {
		t.Fatalf("unexpected digest comparisons: %#v", result.Items[:3])
	}
	if result.Items[len(result.Items)-1].HasUpdate != 0 {
		t.Fatalf("duplicate image result = %#v", result.Items[len(result.Items)-1])
	}
	if result.Items[3].HasUpdate != -1 || result.Items[3].LocalDigest != "remote-3" || !strings.Contains(result.Items[3].Note, "registry unavailable") {
		t.Fatalf("registry failure lost the running image digest: %#v", result.Items[3])
	}
	if result.Items[0].State != "running" || result.Items[len(result.Items)-1].State != "exited" {
		t.Fatalf("container states were not preserved in scan results")
	}

	dockerMu.Lock()
	defer dockerMu.Unlock()
	for _, call := range dockerMethods {
		if !strings.HasPrefix(call, http.MethodGet+" ") {
			t.Fatalf("scan performed a Docker write: %s", call)
		}
		if strings.Contains(call, "/images/registry.example") {
			t.Fatalf("scan inspected repo:tag instead of running ImageID: %s", call)
		}
	}
	imageIDZeroCalls := 0
	for _, call := range dockerMethods {
		if call == "GET /images/sha256:image-id-0/json" {
			imageIDZeroCalls++
		}
	}
	if imageIDZeroCalls != 1 {
		t.Fatalf("same running image ID was inspected %d times, want 1", imageIDZeroCalls)
	}
}

func TestScanContainersReportsTopLevelDockerFailure(t *testing.T) {
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return dockerJSONResponse(http.StatusInternalServerError, map[string]string{"message": "daemon failed"}), nil
	})}}
	result := scanContainers(dc, func(string) (string, error) {
		t.Fatal("registry must not be queried when Docker listing fails")
		return "", nil
	})
	if result.OK || !strings.Contains(result.Err, "500") {
		t.Fatalf("result = %#v, want explicit top-level failure", result)
	}
}

func TestScanPinnedImageDoesNotQueryRegistry(t *testing.T) {
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/containers/json":
			return dockerJSONResponse(http.StatusOK, []map[string]any{{
				"Id": "pinned-id", "Names": []string{"/pinned"}, "Image": "registry.example/team/app@sha256:abc", "ImageID": "sha256:local", "State": "running",
			}}), nil
		case "/containers/pinned-id/json":
			return dockerJSONResponse(http.StatusOK, map[string]any{
				"Name": "/pinned", "Config": map[string]string{"Image": "registry.example/team/app@sha256:abc"},
			}), nil
		default:
			t.Fatalf("pinned scan made unexpected Docker request: %s", req.URL.Path)
			return nil, nil
		}
	})}}
	result := scanContainers(dc, func(string) (string, error) {
		t.Fatal("pinned image must not be queried on the registry")
		return "", nil
	})
	if !result.OK || len(result.Items) != 1 || result.Items[0].UpdateType != "pinned" || result.Items[0].HasUpdate != -1 {
		t.Fatalf("pinned scan result = %#v", result)
	}
}

func TestScanUsesConfiguredImageRefAndRejectsImageIDFallback(t *testing.T) {
	oldList := listTagsFn
	// No semver tags are needed for a floating channel: it compares the digest
	// of the configured tag and never rewrites Compose to a guessed version.
	listTagsFn = func(string) ([]string, error) { return nil, nil }
	t.Cleanup(func() { listTagsFn = oldList })

	var registryRefs []string
	var dockerCalls []string
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		dockerCalls = append(dockerCalls, req.Method+" "+req.URL.Path)
		if req.Method != http.MethodGet {
			return dockerJSONResponse(http.StatusMethodNotAllowed, nil), nil
		}
		switch req.URL.Path {
		case "/containers/json":
			return dockerJSONResponse(http.StatusOK, []map[string]any{
				{"Id": "cloudflared", "Names": []string{"/cloudflared"}, "Image": "sha256:running-cloudflared", "ImageID": "sha256:running-cloudflared", "State": "running"},
				{"Id": "broken", "Names": []string{"/broken"}, "Image": "sha256:running-broken", "ImageID": "sha256:running-broken", "State": "running"},
			}), nil
		case "/containers/cloudflared/json":
			return dockerJSONResponse(http.StatusOK, map[string]any{
				"Name": "/cloudflared", "Config": map[string]string{"Image": "cloudflare/cloudflared:latest"},
			}), nil
		case "/containers/broken/json":
			return dockerJSONResponse(http.StatusInternalServerError, map[string]string{"message": "inspect unavailable"}), nil
		case "/images/sha256:running-cloudflared/json":
			return dockerJSONResponse(http.StatusOK, map[string]any{
				"RepoDigests": []string{"cloudflare/cloudflared@sha256:current"},
			}), nil
		default:
			return dockerJSONResponse(http.StatusNotFound, map[string]string{"message": "unexpected path"}), nil
		}
	})}}

	result := scanContainers(dc, func(ref string) (string, error) {
		registryRefs = append(registryRefs, ref)
		if ref != "cloudflare/cloudflared:latest" {
			return "", fmt.Errorf("unexpected registry ref %q", ref)
		}
		return "new-content", nil
	})
	if !result.OK || len(result.Items) != 2 {
		t.Fatalf("scan result = %#v", result)
	}
	cloudflared := result.Items[0]
	// Floating :latest follows same-tag digest changes without being rewritten
	// to an unrelated semver tag. It remains unmanaged, so it is scan-only.
	if cloudflared.Image != "cloudflare/cloudflared:latest" || cloudflared.UpdateType != "unmanaged" || cloudflared.HasUpdate != 1 || cloudflared.LocalDigest != "current" {
		t.Fatalf("configured image ref was not recovered / latest policy wrong: %#v", cloudflared)
	}
	if !strings.Contains(cloudflared.Note, "浮动标签") {
		t.Fatalf("expected note about floating-tag refresh, got %#v", cloudflared)
	}
	broken := result.Items[1]
	if broken.UpdateType != "local" || broken.HasUpdate != -1 || !strings.Contains(broken.Note, "列表仅有镜像 ID") {
		t.Fatalf("unsafe image-ID fallback was not rejected: %#v", broken)
	}
	// Digest of :latest is still probed once for the floating/no-semver path.
	if len(registryRefs) != 1 || registryRefs[0] != "cloudflare/cloudflared:latest" {
		t.Fatalf("registry refs = %v, want only configured cloudflared ref", registryRefs)
	}
	for _, call := range dockerCalls {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatalf("scan performed a Docker write: %s", call)
		}
		if strings.Contains(call, "running-broken") && strings.Contains(call, "/images/") {
			t.Fatalf("image-ID fallback was inspected as a registry image: %s", call)
		}
	}
}

// TestApplyRegistryScanGroupRefusesCrossMajor locks in the major-version guard.
// A stateful image must never be auto-suggested across a major version: upgrades
// need explicit migration (pg_upgrade) and downgrades are fatal (the real
// postgres:18-alpine → 9.6.24 crash-loop). Both cross-major directions are
// refused (SuggestedImage stays empty); a same-major suggestion is still allowed.
func TestApplyRegistryScanGroupRefusesCrossMajor(t *testing.T) {
	cases := []struct {
		name     string
		ref      string
		tags     []string
		wantNone bool // true => cross-major blocked, SuggestedImage must be empty
		wantMaj  int  // when allowed, the suggested tag's major
	}{
		{name: "downgrade-blocked", ref: "myapp:5-alpine", tags: []string{"4.0.0", "3.0.0", "latest"}, wantNone: true},
		{name: "upgrade-blocked", ref: "myapp:3-alpine", tags: []string{"5.0.0", "4.2.0", "latest"}, wantNone: true},
		{name: "same-major-allowed", ref: "myapp:3-alpine", tags: []string{"3.9.0-alpine", "3.9.0-windowsservercore", "3.8.0-alpine", "latest"}, wantMaj: 3},
		{name: "postgres-realistic", ref: "postgres:18-alpine", tags: []string{"latest", "18", "18.1-alpine", "18-alpine", "17.5", "16.4", "9.6.24", "9.6.24-alpine"}, wantMaj: 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := splitImage(tc.ref)
			dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/json") {
					return dockerJSONResponse(http.StatusNotFound, nil), nil
				}
				return dockerJSONResponse(http.StatusOK, map[string]any{
					"RepoDigests": []string{repo + "@sha256:local-running-digest"},
				}), nil
			})}}
			items := []proto.ContainerScanItem{{Image: tc.ref}}
			applyRegistryScanGroup(dc,
				func(string) (string, error) { return "remote-best-digest", nil }, // best tag digest differs from local
				func(string) ([]string, error) { return tc.tags, nil },
				tc.ref, []int{0}, items, []string{"sha256:running"})

			it := items[0]
			if it.LocalDigest == "" {
				t.Fatalf("local digest not populated, item=%#v", it)
			}
			if tc.wantNone {
				if strings.TrimSpace(it.SuggestedImage) != "" {
					t.Fatalf("cross-major must NOT set SuggestedImage, got %q (item=%#v)", it.SuggestedImage, it)
				}
				if it.HasUpdate != 0 {
					t.Fatalf("cross-major HasUpdate=%d want 0 (item=%#v)", it.HasUpdate, it)
				}
				return
			}
			_, suggTag := splitImage(it.SuggestedImage)
			sv, ok := parseSemverTag(suggTag)
			if !ok {
				t.Fatalf("suggested %q is not semver (item=%#v)", it.SuggestedImage, it)
			}
			if sv.Major != tc.wantMaj {
				t.Fatalf("suggested %q major=%d want %d (item=%#v)", it.SuggestedImage, sv.Major, tc.wantMaj, it)
			}
			if strings.Contains(it.SuggestedImage, "9.6") {
				t.Fatalf("suggested %q must never be the ancient 9.6.x downgrade target", it.SuggestedImage)
			}
		})
	}
}

func TestApplyRegistryScanGroupDoesNotClaimCurrentWhenTagListFails(t *testing.T) {
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/json") {
			return dockerJSONResponse(http.StatusNotFound, nil), nil
		}
		return dockerJSONResponse(http.StatusOK, map[string]any{
			"RepoDigests": []string{"registry.example/team/app@sha256:local"},
		}), nil
	})}}
	items := []proto.ContainerScanItem{{Image: "registry.example/team/app:v3.0.2", HasUpdate: -1}}
	applyRegistryScanGroup(dc,
		func(string) (string, error) { return "local", nil },
		func(string) ([]string, error) { return nil, errors.New("second page failed") },
		items[0].Image, []int{0}, items, []string{"sha256:running"})

	if items[0].HasUpdate != -1 || !strings.Contains(items[0].Note, "无法列出 registry 标签") {
		t.Fatalf("incomplete tag scan was treated as conclusive: %#v", items[0])
	}
}

// TestRewriteComposeImageRefusesCrossMajor is the file-level belt-and-suspenders
// for the same incident: a cross-major image change is refused and the compose
// file (and absence of a .bak) is left untouched; a same-major change applies.
func TestRewriteComposeImageRefusesCrossMajor(t *testing.T) {
	dir := t.TempDir()
	composeFile := dir + "/compose.yml"
	orig := []byte("services:\n  postgres:\n    image: postgres:18-alpine\n")
	if err := os.WriteFile(composeFile, orig, 0o600); err != nil {
		t.Fatal(err)
	}

	// Cross-major downgrade is refused; file and .bak both untouched.
	if err := rewriteComposeImage(composeFile, "postgres", "postgres:9.6.24"); err == nil || !strings.Contains(err.Error(), "跨大版本") {
		t.Fatalf("downgrade error = %v, want 跨大版本 refusal", err)
	}
	if got, _ := os.ReadFile(composeFile); string(got) != string(orig) {
		t.Fatalf("file mutated after refused rewrite:\n got %s\n want %s", got, orig)
	}
	if _, err := os.Stat(composeFile + ".bak"); err == nil {
		t.Fatal("a .bak was created even though the rewrite was refused")
	}

	// Same-major change is allowed and applied.
	if err := rewriteComposeImage(composeFile, "postgres", "postgres:18.4"); err != nil {
		t.Fatalf("same-major rewrite failed: %v", err)
	}
	if got, _ := os.ReadFile(composeFile); !strings.Contains(string(got), "postgres:18.4") {
		t.Fatalf("same-major rewrite not applied, file=\n%s", got)
	}
}

func TestValidateContainerOp(t *testing.T) {
	for _, action := range []string{"update", "restart", "start", "stop", "rebuild", "upgrade", "delete"} {
		req := proto.ContainerOpRequest{Action: action, IDs: []string{"container-id"}}
		if err := validateContainerOp(req); err != nil {
			t.Errorf("valid action %q rejected: %v", action, err)
		}
	}
	if err := validateContainerOp(proto.ContainerOpRequest{Action: ""}); err == nil {
		t.Error("empty action was accepted")
	}
	if err := validateContainerOp(proto.ContainerOpRequest{Action: "destroy", IDs: []string{"id"}}); err == nil {
		t.Error("unknown action was accepted")
	}
	if err := validateContainerOp(proto.ContainerOpRequest{Action: "delete"}); err == nil {
		t.Error("delete without IDs was accepted")
	}
	if err := validateContainerOp(proto.ContainerOpRequest{Action: "update"}); err != nil {
		t.Errorf("bulk update was rejected: %v", err)
	}
}

func TestDigestPinnedImagesAreNotTagUpdates(t *testing.T) {
	const ref = "registry.example/team/app@sha256:0123456789abcdef"
	if got := classifyUpdate(nil, ref); got != "pinned" {
		t.Fatalf("classifyUpdate(%q) = %q, want pinned", ref, got)
	}
	repo, digest := splitImage(ref)
	if repo != "registry.example/team/app" || digest != "sha256:0123456789abcdef" {
		t.Fatalf("splitImage(%q) = %q, %q", ref, repo, digest)
	}
	if got := formatImageRef(repo, digest); got != ref {
		t.Fatalf("formatImageRef() = %q, want %q", got, ref)
	}
}

func TestUnmanagedRegistryImagesAreScannableButNotBulkEligible(t *testing.T) {
	if got := classifyUpdate(nil, "cloudflare/cloudflared:latest"); got != "unmanaged" {
		t.Fatalf("plain registry image classified as %q, want unmanaged", got)
	}
	if bulkUpdateEligible("unmanaged") {
		t.Fatal("unmanaged image was accepted for bulk update")
	}
	if !registryScanEligible("unmanaged") {
		t.Fatal("unmanaged image was excluded from registry scan")
	}
	if got := classifyUpdate(nil, "sha256:0123456789abcdef"); got != "local" {
		t.Fatalf("image ID classified as %q, want local", got)
	}
	if got := classifyUpdate(nil, "custom-app:local"); got != "local" {
		t.Fatalf(":local image classified as %q, want local", got)
	}
}

func TestManagedRegistryImageRemainsBulkEligible(t *testing.T) {
	dir := t.TempDir()
	composeFile := dir + "/compose.yml"
	if err := os.WriteFile(composeFile, []byte("services:\n  tunnel:\n    image: cloudflare/cloudflared:latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{
		"com.docker.compose.project.config_files": composeFile,
		"com.docker.compose.project.working_dir":  dir,
		"com.docker.compose.service":              "tunnel",
	}
	updateType := classifyUpdate(labels, "cloudflare/cloudflared:latest")
	if updateType != "latest" || !bulkUpdateEligible(updateType) {
		t.Fatalf("managed registry image type = %q, want bulk-eligible latest", updateType)
	}
}

func TestActRejectsUnknownActionWithoutDockerRequest(t *testing.T) {
	called := false
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, fmt.Errorf("unexpected request")
	})}}
	if _, err := dc.act("destroy", "id", "image:tag", "", ""); err == nil {
		t.Fatal("unknown action was accepted")
	}
	if called {
		t.Fatal("unknown action reached Docker")
	}
}

func TestNonComposeUpdateIsUnsupportedAndReadOnly(t *testing.T) {
	var methods []string
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		methods = append(methods, req.Method+" "+req.URL.Path)
		if req.Method != http.MethodGet || req.URL.Path != "/containers/plain/json" {
			return dockerJSONResponse(http.StatusMethodNotAllowed, nil), nil
		}
		return dockerJSONResponse(http.StatusOK, map[string]any{
			"Image": "sha256:old", "Name": "/plain",
			"State":  map[string]any{"Running": true, "Status": "running"},
			"Config": map[string]any{"Image": "registry.example/team/plain:v1", "Labels": map[string]string{}},
		}), nil
	})}}

	if _, err := dc.updateOne("plain", "registry.example/team/plain:v1", ""); err == nil || !strings.Contains(err.Error(), "non-Compose") {
		t.Fatalf("update error = %v, want explicit unsupported error", err)
	}
	if len(methods) != 1 || methods[0] != "GET /containers/plain/json" {
		t.Fatalf("non-Compose update made unsafe Docker calls: %v", methods)
	}
}

func TestUpdateStoppedContainerIsSkippedWithoutMutation(t *testing.T) {
	var methods []string
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		methods = append(methods, req.Method+" "+req.URL.Path)
		return dockerJSONResponse(http.StatusOK, map[string]any{
			"Image": "sha256:old", "Name": "/stopped",
			"State":  map[string]any{"Running": false, "Status": "exited"},
			"Config": map[string]any{"Image": "registry.example/team/app:latest", "Labels": map[string]string{}},
		}), nil
	})}}

	detail, err := dc.updateOne("stopped", "registry.example/team/app:latest", "")
	if !errors.Is(err, errSkip) || !strings.Contains(detail, "exited") {
		t.Fatalf("update = (%q, %v), want an exited-container skip", detail, err)
	}
	if len(methods) != 1 || methods[0] != "GET /containers/stopped/json" {
		t.Fatalf("stopped update made Docker mutations: %v", methods)
	}
}

func TestWaitContainerReadyRejectsUnhealthy(t *testing.T) {
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return dockerJSONResponse(http.StatusOK, map[string]any{
			"Image": "sha256:new",
			"State": map[string]any{"Running": true, "Status": "running", "Health": map[string]string{"Status": "unhealthy"}},
		}), nil
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := dc.waitContainerReady(ctx, "service"); err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("readiness error = %v", err)
	}
}

func TestWaitContainerReadyRequiresStabilityWithoutHealth(t *testing.T) {
	var calls atomic.Int32
	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return dockerJSONResponse(http.StatusOK, map[string]any{
				"Image": "sha256:new",
				"State": map[string]any{"Running": true, "Status": "running"},
			}), nil
		}
		return dockerJSONResponse(http.StatusOK, map[string]any{
			"Image": "sha256:new",
			"State": map[string]any{"Running": false, "Status": "exited", "Error": "startup failed"},
		}), nil
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := dc.waitContainerReadyStable(ctx, "service", 10*time.Millisecond); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("readiness error = %v, want an early-exit failure", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("container was inspected %d times, want stability recheck", calls.Load())
	}
}

// A locally built image (no RepoDigest) whose name has no registry host — e.g.
// compose `image: myapp:static` that was built, not pulled — resolves to a
// nonexistent Docker Hub library repo, so the registry lookup 401s. The scan
// must reclassify it as local (so the scheduler skips it) instead of surfacing
// a 401 "unknown" failure. A real pulled image that 401s (it has a RepoDigest)
// must stay unknown so genuine registry outages are still visible.
func TestScanContainersReclassifiesLocallyBuiltImagesAsLocal(t *testing.T) {
	type listedContainer struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		ImageID string            `json:"ImageID"`
		State   string            `json:"State"`
		Labels  map[string]string `json:"Labels"`
	}
	containers := []listedContainer{
		{ID: "c-local", Names: []string{"/myapp"}, Image: "myapp:static", ImageID: "sha256:local-build", State: "running"},
		{ID: "c-pulled", Names: []string{"/realapp"}, Image: "real/app:latest", ImageID: "sha256:pulled", State: "running"},
	}
	digestsByID := map[string][]string{
		"sha256:local-build": {}, // locally built: no RepoDigest
		"sha256:pulled":      {"real/app@sha256:pulled-digest"},
	}
	configuredRefByID := map[string]string{"c-local": "myapp:static", "c-pulled": "real/app:latest"}

	dc := &dockerClient{c: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return dockerJSONResponse(http.StatusMethodNotAllowed, map[string]string{"error": "write attempted"}), nil
		}
		if req.URL.Path == "/containers/json" {
			return dockerJSONResponse(http.StatusOK, containers), nil
		}
		if strings.HasPrefix(req.URL.Path, "/containers/") && strings.HasSuffix(req.URL.Path, "/json") {
			cid := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/containers/"), "/json")
			ref, ok := configuredRefByID[cid]
			if !ok {
				return dockerJSONResponse(http.StatusNotFound, map[string]string{"message": "unknown container"}), nil
			}
			return dockerJSONResponse(http.StatusOK, map[string]any{"Name": "/" + cid, "Config": map[string]string{"Image": ref}}), nil
		}
		if strings.HasPrefix(req.URL.Path, "/images/") && strings.HasSuffix(req.URL.Path, "/json") {
			id := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/images/"), "/json")
			d, ok := digestsByID[id]
			if !ok {
				return dockerJSONResponse(http.StatusNotFound, map[string]string{"message": "unknown image id"}), nil
			}
			return dockerJSONResponse(http.StatusOK, map[string]any{"RepoDigests": d}), nil
		}
		return dockerJSONResponse(http.StatusNotFound, map[string]string{"message": "unexpected path"}), nil
	})}}

	// Both lookups 401, as Docker Hub does for nonexistent/private repos.
	registryLookup := func(ref string) (string, error) {
		return "", fmt.Errorf("registry status 401 Unauthorized")
	}

	result := scanContainers(dc, registryLookup)
	if !result.OK || result.Err != "" {
		t.Fatalf("scan result = OK %v, err %q", result.OK, result.Err)
	}
	byName := make(map[string]proto.ContainerScanItem, len(result.Items))
	for _, it := range result.Items {
		byName[it.Name] = it
	}

	local := byName["myapp"]
	if local.UpdateType != "local" {
		t.Fatalf("locally built image UpdateType = %q, want local", local.UpdateType)
	}
	if local.HasUpdate != -1 {
		t.Fatalf("locally built image HasUpdate = %d, want -1", local.HasUpdate)
	}
	if strings.Contains(local.Note, "401") || strings.Contains(local.Note, "无法检测 registry") {
		t.Fatalf("locally built image must not surface the 401, note = %q", local.Note)
	}
	if !strings.Contains(local.Note, "本地构建") {
		t.Fatalf("locally built image note should mention local build, note = %q", local.Note)
	}

	pulled := byName["realapp"]
	if pulled.UpdateType == "local" {
		t.Fatalf("pulled image with RepoDigest must not be reclassified, UpdateType = local")
	}
	if pulled.HasUpdate != -1 || !strings.Contains(pulled.Note, "401") {
		t.Fatalf("pulled image with RepoDigest should stay unknown on 401, item = %#v", pulled)
	}
}
