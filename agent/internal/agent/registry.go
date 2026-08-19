package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// registryHTTPClient is shared for manifest/token probes. Prefer IPv4 (same
// reason as the panel dialer) and allow a bit longer than a hard 8s cut —
// ghcr.io from some regions regularly needs a second try under load.
var registryHTTPClient = &http.Client{
	Timeout: 12 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialContextPreferIPv4,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   4,
	},
}

const registryProbeAttempts = 3

// registryManifestDigest fetches the current manifest digest of an image from
// its registry (docker.io / ghcr.io / custom host) WITHOUT downloading layers.
// Returns the digest with the "sha256:" prefix stripped. An error means the
// registry could not be queried (private/unknown/unreachable).
// Transient network / 5xx / 429 errors are retried a few times before giving up.
func registryManifestDigest(image string) (string, error) {
	var last error
	for attempt := 0; attempt < registryProbeAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
		dig, err := registryManifestDigestWithClient(registryHTTPClient, image)
		if err == nil {
			return dig, nil
		}
		last = err
		if !isTransientRegistryErr(err) {
			return "", err
		}
	}
	return "", last
}

func isTransientRegistryErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "timeout"),
		strings.Contains(s, "deadline exceeded"),
		strings.Contains(s, "i/o timeout"),
		strings.Contains(s, "connection reset"),
		strings.Contains(s, "connection refused"),
		strings.Contains(s, "tls handshake"),
		strings.Contains(s, "temporary"),
		strings.Contains(s, "broken pipe"),
		strings.Contains(s, "eof"),
		strings.Contains(s, "status 429"),
		strings.Contains(s, "status 502"),
		strings.Contains(s, "status 503"),
		strings.Contains(s, "status 504"),
		strings.Contains(s, "429 too many"),
		strings.Contains(s, "bad gateway"),
		strings.Contains(s, "service unavailable"),
		strings.Contains(s, "gateway timeout"):
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

func registryManifestDigestWithClient(client *http.Client, image string) (string, error) {
	if strings.TrimSpace(image) == "" {
		return "", fmt.Errorf("invalid empty image reference")
	}
	reg, repo, tag := parseRef(image)
	if strings.Trim(strings.TrimSpace(repo), "/") == "" || strings.TrimSpace(tag) == "" {
		return "", fmt.Errorf("invalid image reference %q", image)
	}
	token, err := registryTokenWithClient(client, reg, repo)
	if err != nil {
		return "", err
	}
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registryAPIHost(reg), repo, tag)
	req, err := http.NewRequest(http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return "", fmt.Errorf("registry status %s: %s", resp.Status, detail)
		}
		return "", fmt.Errorf("registry status %s", resp.Status)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return "", fmt.Errorf("registry returned an empty Docker-Content-Digest")
	}
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || strings.TrimSpace(algorithm) == "" || strings.TrimSpace(encoded) == "" {
		return "", fmt.Errorf("registry returned an invalid Docker-Content-Digest %q", digest)
	}
	if algorithm == "sha256" {
		return encoded, nil
	}
	return digest, nil
}

func registryToken(reg, repo string) (string, error) {
	return registryTokenWithClient(registryHTTPClient, reg, repo)
}

func registryTokenWithClient(client *http.Client, reg, repo string) (string, error) {
	var tokenURL string
	q := url.Values{"scope": {"repository:" + repo + ":pull"}}
	switch reg {
	case "docker.io":
		q.Set("service", "registry.docker.io")
		tokenURL = "https://auth.docker.io/token?" + q.Encode()
	case "ghcr.io":
		q.Set("service", "ghcr.io")
		tokenURL = "https://ghcr.io/token?" + q.Encode()
	default:
		return "", nil
	}
	return fetchRegistryToken(client, tokenURL)
}

func fetchRegistryToken(client *http.Client, tokenURL string) (string, error) {
	resp, err := client.Get(tokenURL)
	if err != nil {
		return "", fmt.Errorf("registry token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return "", fmt.Errorf("registry token status %s: %s", resp.Status, detail)
		}
		return "", fmt.Errorf("registry token status %s", resp.Status)
	}
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode registry token: %w", err)
	}
	token := strings.TrimSpace(out.Token)
	if token == "" {
		token = strings.TrimSpace(out.AccessToken)
	}
	if token == "" {
		return "", fmt.Errorf("registry returned an empty token")
	}
	return token, nil
}

// stripDigest removes a trailing @sha256:… so tag parsing sees repo:tag.
func stripDigest(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, "@"); i > 0 {
		return ref[:i]
	}
	return ref
}

// parseRef splits an image ref into (registry, repo, tag).
func parseRef(ref string) (reg, repo, tag string) {
	ref = stripDigest(ref)
	tag = "latest"
	if i := strings.LastIndex(ref, ":"); i > 0 && !strings.Contains(ref[i+1:], "/") {
		ref, tag = ref[:i], ref[i+1:]
	}
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		reg, repo = parts[0], parts[1]
		switch reg {
		case "index.docker.io", "registry-1.docker.io":
			reg = "docker.io"
		}
		if reg == "docker.io" && !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
		return reg, repo, tag
	}
	if len(parts) == 2 {
		return "docker.io", ref, tag
	}
	return "docker.io", "library/" + ref, tag
}

func registryAPIHost(reg string) string {
	if reg == "docker.io" || reg == "index.docker.io" || reg == "registry-1.docker.io" {
		return "registry-1.docker.io"
	}
	return reg
}

func registryListTags(image string) ([]string, error) {
	return registryListTagsWithClient(registryHTTPClient, image)
}

func registryListTagsWithClient(client *http.Client, image string) ([]string, error) {
	reg, repo, _ := parseRef(image)
	if strings.Trim(repo, "/") == "" {
		return nil, fmt.Errorf("invalid image reference %q", image)
	}
	token, err := registryTokenWithClient(client, reg, repo)
	if err != nil {
		return nil, err
	}
	tagsURL, err := url.Parse(fmt.Sprintf("https://%s/v2/%s/tags/list", registryAPIHost(reg), repo))
	if err != nil {
		return nil, err
	}
	// Registries are allowed to paginate this endpoint. GHCR, for example,
	// returns only 100 tags by default and places newer tags on later pages.
	// Missing the Link header used to make v3.0.2 look newer than the truncated
	// first-page maximum v2.0.46, so the scheduler incorrectly suppressed a real
	// upgrade. Ask for a bounded page size and follow rel=next until exhausted.
	q := tagsURL.Query()
	q.Set("n", strconv.Itoa(registryTagsPageSize))
	tagsURL.RawQuery = q.Encode()

	var tags []string
	seenTags := make(map[string]struct{})
	seenPages := make(map[string]struct{})
	for page := 0; tagsURL != nil; page++ {
		if page >= registryTagsMaxPages {
			return nil, fmt.Errorf("list tags: pagination exceeded %d pages", registryTagsMaxPages)
		}
		pageURL := tagsURL.String()
		if _, exists := seenPages[pageURL]; exists {
			return nil, fmt.Errorf("list tags: pagination loop at %s", pageURL)
		}
		seenPages[pageURL] = struct{}{}

		req, err := http.NewRequest(http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list tags: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, registryTagsPageBytes+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read tags: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			detail := strings.TrimSpace(string(body))
			if detail != "" {
				return nil, fmt.Errorf("list tags status %s: %s", resp.Status, tailString(detail, 4096))
			}
			return nil, fmt.Errorf("list tags status %s", resp.Status)
		}
		if len(body) > registryTagsPageBytes {
			return nil, fmt.Errorf("list tags: page exceeds %d bytes", registryTagsPageBytes)
		}
		var out struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode tags: %w", err)
		}
		for _, tag := range out.Tags {
			if _, exists := seenTags[tag]; exists {
				continue
			}
			seenTags[tag] = struct{}{}
			tags = append(tags, tag)
			if len(tags) > registryTagsMaxCount {
				return nil, fmt.Errorf("list tags: exceeds %d tags", registryTagsMaxCount)
			}
		}

		tagsURL, err = registryNextLink(tagsURL, resp.Header.Values("Link"))
		if err != nil {
			return nil, fmt.Errorf("list tags: %w", err)
		}
	}
	return tags, nil
}

const (
	registryTagsPageSize  = 100
	registryTagsMaxPages  = 100
	registryTagsMaxCount  = 10000
	registryTagsPageBytes = 2 << 20
)

// registryNextLink returns a validated rel=next URL. Credentials must never be
// forwarded to another origin even if a registry sends a malicious Link value.
func registryNextLink(current *url.URL, headers []string) (*url.URL, error) {
	for _, header := range headers {
		for _, part := range strings.Split(header, ",") {
			fields := strings.Split(part, ";")
			isNext := false
			for _, field := range fields[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
				if ok && strings.EqualFold(key, "rel") && strings.EqualFold(strings.Trim(value, `"`), "next") {
					isNext = true
					break
				}
			}
			if len(fields) < 2 || !isNext {
				continue
			}
			raw := strings.TrimSpace(fields[0])
			if len(raw) < 3 || raw[0] != '<' || raw[len(raw)-1] != '>' {
				return nil, fmt.Errorf("invalid pagination Link %q", part)
			}
			next, err := current.Parse(raw[1 : len(raw)-1])
			if err != nil {
				return nil, fmt.Errorf("invalid pagination URL: %w", err)
			}
			if next.Scheme != current.Scheme || next.Host != current.Host || next.Path != current.Path {
				return nil, fmt.Errorf("pagination URL changed registry endpoint")
			}
			return next, nil
		}
	}
	return nil, nil
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// Minor and Patch are optional so registries that ship short tags are ranked
// correctly: postgres publishes 2-part tags for modern releases (18, 17.5) and
// 1-part (18); requiring a full MAJOR.MINOR.PATCH made highestSemverTag pick an
// ancient 3-part tag (9.6.24) as the "newest", causing catastrophic major-version
// downgrades. Missing segments parse as 0 (parseSemverTag ignores Atoi errors).
var semverRe = regexp.MustCompile(`(?i)^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:[-.]?(.*))?$`)

var floatingTags = map[string]bool{
	"latest": true, "edge": true, "main": true, "master": true, "dev": true,
	"develop": true, "nightly": true, "stable": true, "alpine": true,
	"chromium-bundled": true,
}

type semver struct {
	Major, Minor, Patch int
	Pre                 string
	Raw                 string
}

func parseSemverTag(tag string) (semver, bool) {
	tag = strings.TrimSpace(tag)
	if tag == "" || floatingTags[strings.ToLower(tag)] {
		return semver{}, false
	}
	lower := strings.ToLower(tag)
	if strings.HasPrefix(lower, "sha-") || strings.HasPrefix(lower, "sha256") {
		return semver{}, false
	}
	if strings.HasSuffix(lower, "-amd64") || strings.HasSuffix(lower, "-arm64") || strings.HasSuffix(lower, "-arm") {
		return semver{}, false
	}
	m := semverRe.FindStringSubmatch(tag)
	if m == nil {
		return semver{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	pre := ""
	if len(m) > 4 {
		pre = m[4]
	}
	return semver{Major: maj, Minor: min, Patch: pat, Pre: pre, Raw: tag}, true
}

func (a semver) less(b semver) bool {
	if a.Major != b.Major {
		return a.Major < b.Major
	}
	if a.Minor != b.Minor {
		return a.Minor < b.Minor
	}
	if a.Patch != b.Patch {
		return a.Patch < b.Patch
	}
	if a.Pre == "" && b.Pre != "" {
		return false
	}
	if a.Pre != "" && b.Pre == "" {
		return true
	}
	return a.Pre < b.Pre
}

func highestSemverTag(tags []string) string {
	var best semver
	found := false
	for _, t := range tags {
		sv, ok := parseSemverTag(t)
		if !ok {
			continue
		}
		if !found || best.less(sv) {
			best = sv
			found = true
		}
	}
	if !found {
		return ""
	}
	return best.Raw
}

// highestCompatibleSemverTag keeps automatic upgrades inside the configured
// major version and image flavour. Tags such as 3.12-slim and
// 3.15-windowsservercore are versions of different runtime variants and must
// never be treated as interchangeable merely because both parse as numbers.
func highestCompatibleSemverTag(current string, tags []string) string {
	cur, ok := parseSemverTag(current)
	if !ok {
		return ""
	}
	var best semver
	found := false
	for _, tag := range tags {
		sv, ok := parseSemverTag(tag)
		if !ok || sv.Major != cur.Major || !sameImageTagVariant(current, tag) ||
			(!isPrereleaseTag(current) && isPrereleaseTag(tag)) {
			continue
		}
		if !found || best.less(sv) {
			best = sv
			found = true
		}
	}
	if !found {
		return ""
	}
	return best.Raw
}

var imageVariantTokens = []string{
	"alpine", "slim", "bookworm", "bullseye", "buster", "trixie",
	"jammy", "noble", "debian", "ubuntu", "ubi", "fips",
	"windowsservercore", "nanoserver", "windows", "amd64", "arm64", "armv7", "arm",
}

var (
	prereleaseTagRe       = regexp.MustCompile(`(?i)(?:^|[-._])(?:alpha|beta|rc|pre|preview|dev|nightly)[-._]?\d*`)
	joinedPrereleaseTagRe = regexp.MustCompile(`(?i)\d(?:alpha|beta|rc|pre|preview|dev)\d*`)
)

func isPrereleaseTag(tag string) bool {
	return prereleaseTagRe.MatchString(strings.TrimSpace(tag)) ||
		joinedPrereleaseTagRe.MatchString(strings.TrimSpace(tag))
}

func sameImageTagVariant(a, b string) bool {
	return strings.Join(imageTagVariants(a), ",") == strings.Join(imageTagVariants(b), ",")
}

func imageTagVariants(tag string) []string {
	lower := strings.ToLower(strings.TrimSpace(tag))
	var out []string
	for _, token := range imageVariantTokens {
		if tagHasToken(lower, token) {
			out = append(out, token)
		}
	}
	// Numeric suffixes after a complete x.y.z core are commonly distro/package
	// revisions (for example mailcow's 3.10.12-1). Keep that release line
	// instead of silently switching to a tag that drops the packaging variant.
	if sv, ok := parseSemverTag(lower); ok && sv.Pre != "" {
		for _, field := range strings.FieldsFunc(sv.Pre, func(r rune) bool { return r == '-' || r == '.' || r == '_' }) {
			if _, err := strconv.Atoi(field); err == nil {
				out = append(out, "revision")
				break
			}
		}
	}
	return out
}

func tagHasToken(tag, token string) bool {
	for _, field := range strings.FieldsFunc(tag, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '+'
	}) {
		if field == token {
			return true
		}
	}
	return false
}

func imageRepoTag(image, newTag string) string {
	reg, repo, _ := parseRef(image)
	base := repo
	if reg != "docker.io" {
		base = reg + "/" + repo
	} else if strings.HasPrefix(repo, "library/") {
		base = strings.TrimPrefix(repo, "library/")
	}
	return base + ":" + newTag
}

func floatingOrLatest(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if floatingTags[tag] {
		return true
	}
	// A versioned flavour such as 18-alpine is pinned to a release line; only
	// the bare flavour tag alpine is floating.
	if _, ok := parseSemverTag(tag); ok {
		return false
	}
	if tag == "alpine" || tag == "slim" || strings.HasSuffix(tag, "-alpine") {
		return true
	}
	return false
}

func sortTagsForStable(tags []string) []string {
	out := append([]string(nil), tags...)
	sort.Strings(out)
	return out
}
