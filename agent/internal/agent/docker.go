package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"nodepanel/shared/proto"
)

type dockerClient struct{ c *http.Client }

// dockerShared is a process-wide singleton. newDocker() used to build a fresh
// *http.Transport on every call — including every 30s from containersLoop — and
// a hand-constructed Transport defaults to IdleConnTimeout=0 (never expire).
// Each abandoned Transport left an idle keep-alive persistConn (readLoop +
// writeLoop goroutines) retained forever, because persistConn holds a
// back-reference to its Transport, keeping it reachable so the GC finalizer
// never fires. On a busy docker node this leaked ~1 conn/tick → tens of
// thousands of goroutines / hundreds of MB over days. Reuse one Transport and
// bound its idle connections so they're pooled and reaped.
var (
	dockerOnce   sync.Once
	dockerShared *dockerClient
	// Docker mutations are serialized per agent. This prevents overlapping
	// update/rebuild/delete/start/stop requests from racing on the same Compose
	// project or container name. Read-only inventory and scans do not take it.
	containerOpMu sync.Mutex
)

func newDocker() *dockerClient {
	dockerOnce.Do(func() {
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", "/var/run/docker.sock", 5*time.Second)
			},
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 2,
		}
		dockerShared = &dockerClient{c: &http.Client{Transport: tr}}
	})
	return dockerShared
}

func (d *dockerClient) req(method, path string, body io.Reader) (*http.Response, error) {
	return d.reqContext(context.Background(), method, path, body)
}

func (d *dockerClient) reqContext(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	// Docker's POST endpoints (e.g. /containers/create) reject a body with no
	// Content-Type: 400 "malformed Content-Type header". Every body-POST in this
	// agent is JSON, so set it whenever a body is present.
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return d.c.Do(r)
}

// pruneDanglingImages removes unreferenced dangling image layers left behind
// after pulling an already-existing tag or recreating a compose service (the
// old layers become untagged). Only dangling=true layers are touched, so tagged
// rollback images and any image still referenced by a container are preserved.
// Best-effort: callers must treat a returned error as non-fatal housekeeping.
func (d *dockerClient) pruneDanglingImages() (reclaimed int64, layers int, err error) {
	resp, err := d.req(http.MethodPost, "/images/prune?dangling=true", nil)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return 0, 0, errDocker("image prune: " + resp.Status)
	}
	var out struct {
		ImagesDeleted  []map[string]string `json:"ImagesDeleted"`
		SpaceReclaimed int64               `json:"SpaceReclaimed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	for _, e := range out.ImagesDeleted {
		if _, ok := e["Deleted"]; ok {
			layers++
		}
	}
	return out.SpaceReclaimed, layers, nil
}

// formatBytes renders a byte count in a compact human-readable form for log
// lines. It uses binary units (KiB/MiB/GiB) to match `docker system df`.
func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func (a *Agent) handleContainerOp(id string, req proto.ContainerOpRequest) {
	if err := validateContainerOp(req); err != nil {
		a.sendEnv(proto.MsgContainerResult, id, proto.ContainerResult{OK: false, Err: err.Error()})
		return
	}
	containerOpMu.Lock()
	defer containerOpMu.Unlock()
	dc := newDocker()

	// Resolve the working set: either the explicit IDs, or (for update) all
	// running containers, optionally filtered by label.
	type listItem struct {
		ID     string            `json:"Id"`
		Image  string            `json:"Image"`
		Names  []string          `json:"Names"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	var targets []listItem
	result := proto.ContainerResult{
		OK:      true,
		Failed:  map[string]string{},
		Details: map[string]string{},
	}
	if len(req.IDs) > 0 {
		for _, cid := range req.IDs {
			img, nm := inspectRef(dc, cid)
			targets = append(targets, listItem{ID: cid, Image: img, Names: []string{"/" + nm}})
		}
	} else {
		resp, err := dc.req(http.MethodGet, "/containers/json", nil)
		if err != nil {
			a.sendEnv(proto.MsgContainerResult, id, proto.ContainerResult{Err: "docker socket: " + err.Error()})
			return
		}
		if resp.StatusCode >= http.StatusBadRequest {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			a.sendEnv(proto.MsgContainerResult, id, proto.ContainerResult{Err: "list containers: " + resp.Status})
			return
		}
		if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
			resp.Body.Close()
			a.sendEnv(proto.MsgContainerResult, id, proto.ContainerResult{Err: "decode containers: " + err.Error()})
			return
		}
		resp.Body.Close()

		eligible := targets[:0]
		for _, c := range targets {
			name := containerName(c.Names)
			if name == "" {
				name = c.ID
			}
			if c.State != "" && c.State != "running" {
				result.Skipped = append(result.Skipped, name)
				result.Details[name] = "skipped (container is not running)"
				continue
			}
			updateType := classifyUpdate(c.Labels, c.Image)
			if !bulkUpdateEligible(updateType) {
				result.Skipped = append(result.Skipped, name)
				result.Details[name] = "skipped (update type " + updateType + ")"
				continue
			}
			eligible = append(eligible, c)
		}
		targets = eligible
	}

	for _, c := range targets {
		name := containerName(c.Names)
		if name == "" {
			name = c.ID
		}
		detail, err := dc.act(req.Action, c.ID, c.Image, req.Label, req.NewImage)
		switch {
		case errors.Is(err, errSkip):
			result.Skipped = append(result.Skipped, name)
			result.Details[name] = detail
		case errors.Is(err, errUpToDate):
			result.Unchanged = append(result.Unchanged, name)
			result.Details[name] = detail
		case err != nil:
			result.OK = false
			result.Failed[name] = err.Error()
			result.Details[name] = "error: " + err.Error()
		default:
			result.Updated = append(result.Updated, name)
			result.Details[name] = detail
		}
	}
	if len(result.Failed) == 0 {
		result.Failed = nil
	}
	// 2.4.6+: after an image-changing op actually updated something, reclaim the
	// dangling layers left by pull/recreate so the node's disk does not grow
	// forever. Only dangling (untagged, unreferenced) images are removed — tagged
	// rollback images and images still in use are preserved. Best-effort: a prune
	// failure is logged and never turns a successful update into a failure.
	if len(result.Updated) > 0 && (req.Action == "update" || req.Action == "rebuild" || req.Action == "upgrade") {
		if reclaimed, pruned, err := dc.pruneDanglingImages(); err != nil {
			log.Printf("agent: post-%s image prune failed: %v", req.Action, err)
		} else if pruned > 0 {
			log.Printf("agent: post-%s pruned %d dangling layer(s), reclaimed %s", req.Action, pruned, formatBytes(reclaimed))
			result.Details["__prune__"] = fmt.Sprintf("pruned %d dangling layer(s), reclaimed %s", pruned, formatBytes(reclaimed))
		}
	}
	a.sendEnv(proto.MsgContainerResult, id, result)
}

func validateContainerOp(req proto.ContainerOpRequest) error {
	switch req.Action {
	case "update", "restart", "start", "stop", "rebuild", "upgrade", "delete":
	default:
		return errDocker("unsupported container action: " + req.Action)
	}
	if len(req.IDs) == 0 && req.Action != "update" {
		return errDocker("container IDs are required for action " + req.Action)
	}
	return nil
}

// containersLoop reports the container inventory to the panel every 30s. If the
// host has no docker socket, it stays silent (no containers to show).
func (a *Agent) containersLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	a.reportContainers()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reportContainers()
		}
	}
}

func (a *Agent) reportContainers() {
	dc := newDocker()
	resp, err := dc.req(http.MethodGet, "/containers/json?all=true", nil)
	if err != nil {
		return // no docker, or socket unavailable — nothing to report
	}
	defer resp.Body.Close()
	var raw []containerListItem
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return
	}
	out := make([]proto.ContainerInfo, 0, len(raw))
	for _, c := range raw {
		img := c.Image
		if strings.HasPrefix(img, "sha256:") {
			// /containers/json returns the image ID when the image is untagged
			// (e.g. after a re-pull retagged it); fall back to Config.Image which
			// keeps the original repo:tag the container was created with.
			if ref, _ := inspectRef(dc, c.ID); ref != "" {
				img = ref
			}
		}
		out = append(out, proto.ContainerInfo{
			ID: c.ID, Name: containerName(c.Names), Image: img, ImageID: c.ImageID,
			State: c.State, Status: c.Status, Created: c.Created,
			UpdateType: classifyUpdate(c.Labels, img),
			HostPorts:  uniquePorts(c.Ports),
		})
	}
	// Best-effort: discover this node's Cloudflare Tunnel id from the local
	// cloudflared container, so the master can move a migrated container's domain
	// between tunnels. Cheap (one extra inspect) and cached; failure is silent.
	tunnelID := ""
	if cid := findCloudflared(raw); cid != "" {
		tunnelID = detectTunnelID(dc, cid)
	}
	a.sendEnv(proto.MsgContainers, "", proto.ContainersData{Containers: out, TunnelID: tunnelID})
}

// containerListItem is the subset of docker's /containers/json entry we decode
// for the periodic inventory report.
type containerListItem struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	Ports   []dockerPort      `json:"Ports"`
}

// dockerPort is one entry of docker's /containers/json Ports array. Only
// published bindings carry PublicPort (>0); we report those as host ports.
type dockerPort struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort int    `json:"PrivatePort,omitempty"`
	PublicPort  int    `json:"PublicPort,omitempty"`
	Type        string `json:"Type,omitempty"`
}

// uniquePorts returns the deduped, sorted set of published host ports (>0).
func uniquePorts(ports []dockerPort) []int {
	if len(ports) == 0 {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, p := range ports {
		if p.PublicPort > 0 && !seen[p.PublicPort] {
			seen[p.PublicPort] = true
			out = append(out, p.PublicPort)
		}
	}
	for i := 1; i < len(out); i++ { // small slices — simple insertion sort
		j := i
		for j > 0 && out[j] < out[j-1] {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
	return out
}

// findCloudflared returns the id of a running cloudflared container in the
// /containers/json listing (matched by image name, then by container name), or "".
func findCloudflared(raw []containerListItem) string {
	for _, c := range raw {
		if strings.Contains(c.Image, "cloudflared") {
			return c.ID
		}
	}
	for _, c := range raw {
		for _, nm := range c.Names {
			if strings.Contains(strings.TrimPrefix(nm, "/"), "cloudflared") {
				return c.ID
			}
		}
	}
	return ""
}

// tunnelIDCache memoizes the decoded tunnel id per process so we don't re-inspect
// + re-decode the (constant) cloudflared token on every 30s inventory report.
var (
	tunnelIDMu    sync.Mutex
	tunnelIDCache string
)

// detectTunnelID inspects the cloudflared container, extracts its tunnel token
// (from Config.Cmd "--token <jwt>" or Config.Env "TUNNEL_TOKEN=<jwt>"), and
// decodes the base64url JSON payload {"a","t","s"} to return the tunnel id ("t").
// Returns "" if the container can't be inspected or the token doesn't decode.
// The token is a tunnel credential; we extract only the id locally and never
// transmit the token itself.
func detectTunnelID(dc *dockerClient, containerID string) string {
	tunnelIDMu.Lock()
	cached := tunnelIDCache
	tunnelIDMu.Unlock()
	if cached != "" {
		return cached
	}
	token := cloudflaredToken(dc, containerID)
	id := decodeTunnelToken(token)
	if id != "" {
		tunnelIDMu.Lock()
		tunnelIDCache = id
		tunnelIDMu.Unlock()
	}
	return id
}

// cloudflaredToken reads the cloudflared container's run token from its Config.
func cloudflaredToken(dc *dockerClient, containerID string) string {
	resp, err := dc.req(http.MethodGet, "/containers/"+containerID+"/json", nil)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var ins struct {
		Config struct {
			Cmd []string `json:"Cmd"`
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if json.NewDecoder(resp.Body).Decode(&ins) != nil {
		return ""
	}
	// `cloudflared tunnel run --token <JWT>` — the token follows --token.
	for i, a := range ins.Config.Cmd {
		if a == "--token" && i+1 < len(ins.Config.Cmd) {
			return ins.Config.Cmd[i+1]
		}
		if strings.HasPrefix(a, "--token=") {
			return strings.TrimPrefix(a, "--token=")
		}
	}
	for _, e := range ins.Config.Env {
		if strings.HasPrefix(e, "TUNNEL_TOKEN=") {
			return strings.TrimPrefix(e, "TUNNEL_TOKEN=")
		}
	}
	return ""
}

// decodeTunnelToken base64url-decodes a cloudflared tunnel token and returns the
// "t" (tunnel id) field. The token payload is JSON {"a":account,"t":tunnel,"s":...}.
func decodeTunnelToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	dec, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		// some tokens are standard base64 (with padding); try that as a fallback
		dec, err = base64.StdEncoding.DecodeString(token)
		if err != nil {
			return ""
		}
	}
	var p struct {
		T string `json:"t"`
	}
	if json.Unmarshal(dec, &p) != nil {
		return ""
	}
	return p.T
}

// handleContainerScan assesses every container's update readiness: update_type,
// whether a newer image exists on the registry (manifest-digest compare — read
// only, no pull), and whether a build container could be switched to pull.
// Changes nothing on the host.
func (a *Agent) handleContainerScan(id string) {
	a.sendEnv(proto.MsgContainerScanResult, id, scanContainers(newDocker(), registryManifestDigest))
}

const registryScanConcurrency = 4

// scanContainers performs a read-only scan. In particular, it never calls the
// Docker image-create (pull) endpoint: the local side of the comparison comes
// from the exact image ID used by each container.
func scanContainers(dc *dockerClient, registryDigest func(string) (string, error)) proto.ContainerScanResult {
	resp, err := dc.req(http.MethodGet, "/containers/json?all=true", nil)
	if err != nil {
		return proto.ContainerScanResult{OK: false, Err: "docker socket: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, resp.Body)
		return proto.ContainerScanResult{OK: false, Err: "list containers: " + resp.Status}
	}
	var raw []struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		ImageID string            `json:"ImageID"`
		State   string            `json:"State"`
		Labels  map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return proto.ContainerScanResult{OK: false, Err: "decode containers: " + err.Error()}
	}

	items := make([]proto.ContainerScanItem, 0, len(raw))
	imageIDs := make([]string, 0, len(raw))
	refWarnings := make([]string, 0, len(raw))
	for _, c := range raw {
		name := containerName(c.Names)
		if name == "" {
			continue
		}

		imageRef := strings.TrimSpace(c.Image)
		usedFallback := true
		inspectProblem := ""
		if c.ID == "" {
			inspectProblem = "容器列表缺少 ID"
		} else if configured, _, err := inspectRefStrict(dc, c.ID); err != nil {
			inspectProblem = err.Error()
		} else if configured = strings.TrimSpace(configured); configured == "" {
			inspectProblem = "容器 Config.Image 为空"
		} else {
			imageRef = configured
			usedFallback = false
		}

		ut := classifyUpdate(c.Labels, imageRef)
		item := proto.ContainerScanItem{Name: name, Image: imageRef, State: c.State, UpdateType: ut, HasUpdate: -1}
		warning := ""
		if imageRef == "" || isImageIDRef(imageRef) {
			item.UpdateType = "local"
			item.Convertible = false
			if usedFallback {
				item.Note = "无法读取容器配置镜像引用，列表仅有镜像 ID；已跳过 registry 检测"
				if inspectProblem != "" {
					item.Note += ": " + inspectProblem
				}
			} else {
				item.Note = "容器配置使用镜像 ID，无法检测 registry"
			}
		} else if usedFallback && inspectProblem != "" {
			warning = "容器 inspect 失败，使用列表镜像引用: " + inspectProblem
		}
		switch item.UpdateType {
		case "build":
			if strings.Contains(imageRef, "/") {
				item.Convertible = true
				item.Note = "可转纯 pull(确认非魔改后转)"
			} else {
				item.Note = "源码构建(自定义镜像)"
			}
		case "local":
			if item.Note == "" {
				item.Note = "本地镜像(需提供 registry 地址)"
			}
		case "pinned":
			item.Note = "固定 digest(不会自动更新)"
		}
		items = append(items, item)
		imageIDs = append(imageIDs, c.ImageID)
		refWarnings = append(refWarnings, warning)
	}

	// Group equivalent refs so multiple containers using the same tag issue one
	// registry request. Each group is handled by one of a bounded set of workers.
	type imageGroup struct {
		ref     string
		indices []int
	}
	groupsByKey := make(map[string]*imageGroup)
	var groups []*imageGroup
	for i := range items {
		if !registryScanEligible(items[i].UpdateType) {
			continue
		}
		key := canonicalImageRef(items[i].Image)
		group := groupsByKey[key]
		if group == nil {
			group = &imageGroup{ref: items[i].Image}
			groupsByKey[key] = group
			groups = append(groups, group)
		}
		group.indices = append(group.indices, i)
	}

	jobs := make(chan *imageGroup)
	var wg sync.WaitGroup
	workers := registryScanConcurrency
	if len(groups) < workers {
		workers = len(groups)
	}
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for group := range jobs {
				applyRegistryScanGroup(dc, registryDigest, listTagsFn, group.ref, group.indices, items, imageIDs)
			}
		}()
	}
	for _, group := range groups {
		jobs <- group
	}
	close(jobs)
	wg.Wait()
	for i, warning := range refWarnings {
		if warning == "" {
			continue
		}
		if items[i].Note != "" {
			items[i].Note += "; "
		}
		items[i].Note += warning
	}

	return proto.ContainerScanResult{OK: true, Items: items}
}

// applyRegistryScanGroup decides has_update for one image ref shared by several
// containers. Policy (2.4.5+): prefer **semver tag upgrades** over chasing
// :latest digest churn.
//
//  1. List registry tags; if a higher semver tag exists whose digest differs
//     from what is running → HasUpdate=1 + SuggestedImage=repo:newTag.
//  2. Else if the configured tag is a floating channel (latest/edge/…) and no
//     usable semver tags exist → fall back to same-tag digest compare (legacy).
//  3. Else if configured tag is already a semver and it is the highest → 0.
//  4. Floating tag whose digest matches the highest semver → 0 (ignore latest rebuilds).
// listTagsFn is the registry tag lister used by scans. Overridden in tests so
// unit tests do not hit the network and still exercise digest-compare paths.
var listTagsFn = registryListTags

func applyRegistryScanGroup(dc *dockerClient, registryDigest func(string) (string, error), listTags func(string) ([]string, error), ref string, indices []int, items []proto.ContainerScanItem, imageIDs []string) {
	type digestResult struct {
		digest string
		err    error
	}
	localByImageID := make(map[string]digestResult)
	if listTags == nil {
		listTags = registryListTags
	}

	// Shared remote probes for this ref.
	tags, tagsErr := listTags(ref)
	_, _, curTag := parseRef(ref)
	bestTag := ""
	if tagsErr == nil {
		bestTag = highestSemverTag(tags)
	}

	var bestDigest string
	var bestErr error
	if bestTag != "" {
		bestRef := imageRepoTag(ref, bestTag)
		bestDigest, bestErr = registryDigest(bestRef)
		if bestErr == nil && strings.TrimSpace(bestDigest) == "" {
			bestErr = errors.New("registry returned an empty digest")
		}
	}

	// Same-tag digest (legacy path for non-semver-only images).
	var sameDigest string
	var sameErr error
	needSame := bestTag == "" || floatingOrLatest(curTag)
	if needSame {
		sameDigest, sameErr = registryDigest(stripDigest(ref))
		if sameErr == nil && strings.TrimSpace(sameDigest) == "" {
			sameErr = errors.New("registry returned an empty digest")
		}
	}

	for _, idx := range indices {
		local, ok := localByImageID[imageIDs[idx]]
		if !ok {
			local.digest, local.err = dc.runningRepoDigest(imageIDs[idx], items[idx].Image)
			localByImageID[imageIDs[idx]] = local
		}
		if local.digest != "" {
			items[idx].LocalDigest = local.digest
		}
		// Locally built image with no RepoDigest.
		if local.err == nil && local.digest == "" {
			items[idx].UpdateType = "local"
			items[idx].Note = "本地构建镜像(无 RepoDigest),无法与 registry 对比"
			continue
		}
		if local.err != nil {
			items[idx].Note = "无法读取运行镜像 digest: " + local.err.Error()
			continue
		}

		// --- Semver path: only upgrade when a higher version tag exists ---
		if bestTag != "" {
			if bestErr != nil {
				// Fall through to same-tag if we can; else unknown.
				if !needSame || sameErr != nil {
					items[idx].Note = "无法检测版本标签: " + bestErr.Error()
					if tagsErr != nil {
						items[idx].Note = "无法列出 registry 标签: " + tagsErr.Error()
					}
					continue
				}
			} else {
				items[idx].RegistryDigest = bestDigest
				curSV, curIsSV := parseSemverTag(curTag)
				bestSV, bestIsSV := parseSemverTag(bestTag)
				// Guard: never auto-suggest across a major version. A major bump on a
				// stateful image (postgres/mysql/...) needs an explicit migration
				// (pg_upgrade / dump+restore); a major *downgrade* is unsupported and
				// fatal — this is exactly how postgres:18-alpine got rewritten to
				// 9.6.24 against a PG18 data dir and crash-looped. If both tags parse
				// and their majors differ, surface it as info but do not auto-apply.
				if curIsSV && bestIsSV && curSV.Major != bestSV.Major {
					items[idx].HasUpdate = 0
					if curSV.Major < bestSV.Major {
						items[idx].Note = "检测到新大版本 " + bestTag + "（当前 " + curTag + "），跨大版本不自动更新，请手动确认"
					} else {
						items[idx].Note = "当前大版本 " + curTag + " 高于远端最高 " + bestTag + "，不自动降级"
					}
					continue
				}
				// Running content already matches the highest semver tag.
				if bestDigest == local.digest {
					items[idx].HasUpdate = 0
					if floatingOrLatest(curTag) && curTag != bestTag {
						// Still on :latest but content == best tag — suggest pin note only.
						items[idx].Note = "已是最新版本 " + bestTag + "（忽略 latest 重建）"
					} else {
						items[idx].Note = "运行镜像与远端版本 " + bestTag + " 一致"
					}
					continue
				}
				// Higher semver than configured, or floating channel behind best tag.
				newer := floatingOrLatest(curTag) || !curIsSV || curSV.less(bestSV)
				if newer && bestDigest != local.digest {
					suggested := imageRepoTag(ref, bestTag)
					items[idx].HasUpdate = 1
					items[idx].SuggestedImage = suggested
					items[idx].Note = "可升级到版本 " + bestTag
					continue
				}
				// Configured semver is best but digest drift (force-push) — report
				// as update without tag change so pull refreshes same tag.
				if curIsSV && !curSV.less(bestSV) && bestDigest != local.digest && curTag == bestTag {
					items[idx].HasUpdate = 1
					items[idx].Note = "远端版本 " + bestTag + " 内容已变化"
					continue
				}
				items[idx].HasUpdate = 0
				items[idx].Note = "已是最新版本 " + bestTag
				continue
			}
		}

		// --- No semver tags: legacy same-tag digest compare (non-floating only
		//     would be rare; floating without semver still uses this) ---
		if tagsErr != nil && bestTag == "" {
			// Tag list failed — try same-tag digest only.
			sameDigest, sameErr = registryDigest(stripDigest(ref))
		}
		if sameErr != nil {
			items[idx].Note = "无法检测 registry: " + sameErr.Error()
			continue
		}
		if sameDigest == "" {
			// No semver and couldn't get same-tag digest.
			if floatingOrLatest(curTag) {
				items[idx].HasUpdate = 0
				items[idx].Note = "无可用版本 tag，跳过 latest 自动更新"
				continue
			}
			items[idx].Note = "无法检测 registry: empty digest"
			continue
		}
		items[idx].RegistryDigest = sameDigest
		if floatingOrLatest(curTag) && bestTag == "" {
			// Floating channel with zero semver tags in the repo: do NOT chase
			// digest churn (this was the old noisy behaviour for :latest).
			items[idx].HasUpdate = 0
			items[idx].Note = "无版本 tag 可升级（已忽略 latest 内容变化）"
			continue
		}
		if sameDigest == local.digest {
			items[idx].HasUpdate = 0
			items[idx].Note = "运行镜像与远端标签内容一致"
		} else {
			items[idx].HasUpdate = 1
			items[idx].Note = "远端标签内容已变化"
		}
	}
}

func isImageIDRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return strings.HasPrefix(ref, "sha256:") && len(ref) > len("sha256:") && !strings.Contains(strings.TrimPrefix(ref, "sha256:"), "/")
}

// runningRepoDigest returns the digest for the configured repository from the
// exact image ID used by a container. Looking up repo:tag here would compare the
// registry with a newer local tag rather than with what is actually running.
func (d *dockerClient) runningRepoDigest(imageID, imageRef string) (string, error) {
	if strings.TrimSpace(imageID) == "" {
		return "", nil
	}
	resp, err := d.req(http.MethodGet, "/images/"+url.PathEscape(imageID)+"/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", errDocker("inspect running image: " + resp.Status)
	}
	var ins struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ins); err != nil {
		return "", err
	}
	for _, rd := range ins.RepoDigests {
		repo, digest, ok := strings.Cut(rd, "@")
		if ok && canonicalRepository(repo) == canonicalRepository(imageRef) {
			return strings.TrimPrefix(strings.TrimSpace(digest), "sha256:"), nil
		}
	}
	return "", nil
}

func canonicalRepository(ref string) string {
	reg, repo, _ := parseRef(ref)
	switch reg {
	case "index.docker.io", "registry-1.docker.io":
		reg = "docker.io"
	}
	return strings.ToLower(reg + "/" + strings.TrimPrefix(repo, "/"))
}

func canonicalImageRef(ref string) string {
	_, _, tag := parseRef(ref)
	return canonicalRepository(ref) + ":" + tag
}

// act dispatches a single container action, returning a human-readable detail
// string and any error. For "update" the detail distinguishes rebuilt / already
// up to date (via the errUpToDate sentinel) so callers don't claim a no-op as an
// update.
func (d *dockerClient) act(action, containerID, imageRef, labelFilter, newImage string) (string, error) {
	switch action {
	case "update":
		return d.updateOne(containerID, imageRef, labelFilter)
	case "restart":
		return "restarted", d.simple(http.MethodPost, "/containers/"+containerID+"/restart")
	case "start":
		return "started", d.simple(http.MethodPost, "/containers/"+containerID+"/start")
	case "stop":
		return "stopped", d.simple(http.MethodPost, "/containers/"+containerID+"/stop")
	case "rebuild":
		return d.rebuildAction(containerID)
	case "upgrade":
		return d.upgradeAction(containerID, newImage)
	case "delete":
		return "deleted", d.simple(http.MethodDelete, "/containers/"+containerID+"?force=true&v=true")
	default:
		return "", errDocker("unsupported container action: " + action)
	}
}

func (d *dockerClient) simple(method, path string) error {
	r, err := d.req(method, path, nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode >= 400 {
		return errDocker(r.Status)
	}
	return nil
}

// inspectRef reads a container's configured image ref and name (used when acting
// on explicit IDs, where the name isn't already in hand).
func inspectRef(d *dockerClient, containerID string) (image, name string) {
	image, name, _ = inspectRefStrict(d, containerID)
	return image, name
}

func inspectRefStrict(d *dockerClient, containerID string) (image, name string, err error) {
	resp, err := d.req(http.MethodGet, "/containers/"+containerID+"/json", nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", "", errDocker("inspect container: " + resp.Status)
	}
	var ins struct {
		Name   string `json:"Name"`
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ins); err != nil {
		return "", "", err
	}
	return ins.Config.Image, strings.TrimPrefix(ins.Name, "/"), nil
}

func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// updateOne performs a real image update on a single container.
//
// Compose-managed containers are recreated from their source configuration.
// Registry services are pulled first; build services are rebuilt with --build.
// Recreating via Compose preserves the full config far more reliably than
// hand-rebuilding via the API.
//
// Plain `docker run` containers are intentionally unsupported. Reconstructing
// them safely requires an external source of truth and rollback plan; the old
// implementation attempted to create a replacement with the still-occupied
// name and ignored start/delete failures.
func (d *dockerClient) updateOne(containerID, imageRef, labelFilter string) (string, error) {
	// inspect
	resp, err := d.req(http.MethodGet, "/containers/"+containerID+"/json", nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return "", errDocker("inspect container: " + resp.Status)
	}
	var ins struct {
		Name   string                     `json:"Name"`
		Config map[string]json.RawMessage `json:"Config"`
		State  struct {
			Running bool   `json:"Running"`
			Status  string `json:"Status"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ins); err != nil {
		resp.Body.Close()
		return "", err
	}
	resp.Body.Close()
	if !ins.State.Running {
		status := strings.TrimSpace(ins.State.Status)
		if status == "" {
			status = "not running"
		}
		return "skipped (container is " + status + ")", errSkip
	}

	labels := decodeLabels(ins.Config["Labels"])
	if labelFilter != "" && !matchLabel(labels, labelFilter) {
		return "skipped (label mismatch)", errSkip
	}

	// Path A: compose rebuild.
	if cf, wd, svc, ok := composeMetaFromLabels(labels); ok {
		return d.runComposeUpdate(strings.TrimPrefix(ins.Name, "/"), cf, wd, svc)
	}
	_ = imageRef // retained in the signature for the existing action dispatcher
	if labels["com.docker.compose.project"] != "" || labels["com.docker.compose.service"] != "" {
		return "", errDocker("unsupported: Compose metadata or project files are unavailable")
	}
	return "", errDocker("unsupported: non-Compose container updates require manual recreation")
}

// runComposeUpdate brings one service up to date via docker compose. Registry
// images are pulled explicitly and pull errors abort the operation; build-based
// services are rebuilt without trying to pull their local output tag. After
// Compose exits successfully, the resulting container must be running and, when
// it defines a healthcheck, healthy.
// Label values go to exec as discrete arguments (never through a shell) and are
// validated, so a hostile label cannot inject commands.
func (d *dockerClient) runComposeUpdate(name, configFiles, workingDir, service string) (string, error) {
	bin, sub := composeBin()
	if bin == "" {
		return "", errDocker("docker compose not found on host (neither 'docker compose' nor 'docker-compose')")
	}
	oldState, err := d.inspectContainerRuntime(name)
	if err != nil {
		return "", err
	}
	oldID := oldState.ImageID

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute) // master dispatches with a 10m timeout
	defer cancel()

	serviceInfo := composeServiceInfo(configFiles, service)
	var rtRepo, rtTag, tagAfter string
	if !serviceInfo.build {
		repoTag, _, err := inspectRefStrict(d, name)
		if err != nil {
			return "", err
		}
		rtRepo, rtTag = splitImage(repoTag)
		if strings.TrimSpace(rtRepo) == "" {
			return "", errDocker("container has no registry image reference")
		}
		if err := d.pullImageContext(ctx, rtRepo, rtTag); err != nil {
			return "", err
		}
		tagAfter, err = d.imageID(rtRepo, rtTag)
		if err != nil {
			return "", err
		}
	}

	runCompose := func(extra ...string) error {
		args := make([]string, 0, len(sub)+12)
		args = append(args, sub...)
		args = append(args, "-f", configFiles, "--project-directory", workingDir, "up", "-d", "--no-deps")
		args = append(args, extra...)
		args = append(args, service)
		c := exec.CommandContext(ctx, bin, args...)
		var b bytes.Buffer
		c.Stdout = &b
		c.Stderr = &b
		if err := c.Run(); err != nil {
			output := tail(strings.TrimSpace(b.String()), 400)
			if ctx.Err() == context.DeadlineExceeded {
				return errDocker("compose timed out (>9m): " + output)
			}
			if output == "" {
				output = err.Error()
			}
			return errDocker("compose failed: " + output)
		}
		return nil
	}

	if err := runCompose("--build"); err != nil {
		return "", err
	}

	// Compose may leave the service on its old image when only repo:tag content
	// changed. Force a second recreation only if the first `up` did not switch to
	// the image ID that the freshly-pulled tag now references.
	afterUp, err := d.inspectContainerRuntime(name)
	if err != nil {
		return "", err
	}
	needForce := afterUp.ImageID != "" && tagAfter != "" && afterUp.ImageID != tagAfter
	if needForce {
		if err := runCompose("--force-recreate"); err != nil {
			return "", err
		}
	}

	readyCtx, readyCancel := context.WithTimeout(ctx, 45*time.Second)
	defer readyCancel()
	ready, err := d.waitContainerReady(readyCtx, name)
	if err != nil {
		return "", err
	}
	if oldID == ready.ImageID {
		return "already up to date", errUpToDate
	}
	return "rebuilt via compose", nil
}

// composeBin detects the docker compose invocation available on the host: v2
// ("docker compose", a docker subcommand) is preferred, falling back to the v1
// standalone binary ("docker-compose"). The probe runs once and is cached.
var (
	composeBinOnce sync.Once
	composeBinVal  struct {
		bin string
		sub []string
	}
)

func composeBin() (bin string, sub []string) {
	composeBinOnce.Do(func() {
		// "docker compose version" prints a real Compose version line on v2. On
		// hosts without the compose plugin the command just prints docker's help
		// and exits 0, so we must inspect the output rather than trust exit code.
		if out, err := exec.Command("docker", "compose", "version").CombinedOutput(); err == nil && bytes.Contains(out, []byte("Compose")) {
			composeBinVal.bin, composeBinVal.sub = "docker", []string{"compose"}
			return
		}
		if out, err := exec.Command("docker-compose", "version").CombinedOutput(); err == nil && bytes.Contains(out, []byte("compose")) {
			composeBinVal.bin, composeBinVal.sub = "docker-compose", nil
			return
		}
	})
	return composeBinVal.bin, composeBinVal.sub
}

// composeMetaFromLabels extracts the compose project metadata needed to rebuild
// the container, validating each piece: all three labels present, service name
// charset-safe, working_dir an existing dir, config_files an existing file.
// config_files is stored relative to working_dir by compose, so it is resolved
// against working_dir before checking / passing it on (returns an absolute path).
func composeMetaFromLabels(labels map[string]string) (configFiles, workingDir, service string, ok bool) {
	cf := labels["com.docker.compose.project.config_files"]
	wd := labels["com.docker.compose.project.working_dir"]
	svc := labels["com.docker.compose.service"]
	if cf == "" || wd == "" || svc == "" || !validServiceName(svc) {
		return "", "", "", false
	}
	if fi, err := os.Stat(wd); err != nil || !fi.IsDir() {
		return "", "", "", false
	}
	abs := cf
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(wd, abs)
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return "", "", "", false
	}
	return abs, wd, svc, true
}

// validServiceName restricts the compose service name to a charset safe to pass
// as a single exec argument (defense in depth — we never shell out anyway).
func validServiceName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// pullImage pulls repo:tag and surfaces registry errors instead of silently
// discarding them. /images/create streams NDJSON status objects; a failure
// (e.g. manifest unknown for a :local image that isn't on any registry) arrives
// in an "error" field even with HTTP 200.
func (d *dockerClient) pullImage(repo, tag string) error {
	return d.pullImageContext(context.Background(), repo, tag)
}

func (d *dockerClient) pullImageContext(ctx context.Context, repo, tag string) error {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(tag) == "" {
		return errDocker("pull image: empty repository or tag")
	}
	ref := formatImageRef(repo, tag)
	resp, err := d.reqContext(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(repo)+"&tag="+url.QueryEscape(tag), nil)
	if err != nil {
		return errDocker("pull " + ref + ": " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errDocker("pull " + ref + ": " + resp.Status)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return errDocker("pull " + ref + ": invalid Docker response: " + err.Error())
		}
		if msg.Error != "" {
			return errDocker("pull " + ref + ": " + msg.Error)
		}
	}
	return nil
}

func (d *dockerClient) imageID(repo, tag string) (string, error) {
	ref := formatImageRef(repo, tag)
	resp, err := d.req(http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", errDocker("inspect image " + ref + ": " + resp.Status)
	}
	var img struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&img); err != nil {
		return "", err
	}
	if strings.TrimSpace(img.ID) == "" {
		return "", errDocker("inspect image " + ref + ": empty image ID")
	}
	return img.ID, nil
}

type containerRuntime struct {
	ImageID string
	Running bool
	Status  string
	Health  string
	Error   string
}

const noHealthStabilityWindow = 5 * time.Second

func (d *dockerClient) inspectContainerRuntime(ref string) (containerRuntime, error) {
	resp, err := d.req(http.MethodGet, "/containers/"+url.PathEscape(ref)+"/json", nil)
	if err != nil {
		return containerRuntime{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, resp.Body)
		return containerRuntime{}, errDocker("inspect container " + ref + ": " + resp.Status)
	}
	var ins struct {
		Image string `json:"Image"`
		State struct {
			Running bool   `json:"Running"`
			Status  string `json:"Status"`
			Error   string `json:"Error"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ins); err != nil {
		return containerRuntime{}, err
	}
	state := containerRuntime{
		ImageID: ins.Image,
		Running: ins.State.Running,
		Status:  ins.State.Status,
		Error:   ins.State.Error,
	}
	if ins.State.Health != nil {
		state.Health = ins.State.Health.Status
	}
	if strings.TrimSpace(state.ImageID) == "" {
		return containerRuntime{}, errDocker("inspect container " + ref + ": empty running image ID")
	}
	return state, nil
}

func (d *dockerClient) waitContainerReady(ctx context.Context, ref string) (containerRuntime, error) {
	return d.waitContainerReadyStable(ctx, ref, noHealthStabilityWindow)
}

func (d *dockerClient) waitContainerReadyStable(ctx context.Context, ref string, stableFor time.Duration) (containerRuntime, error) {
	var stableSince time.Time
	for {
		state, err := d.inspectContainerRuntime(ref)
		if err != nil {
			return containerRuntime{}, err
		}
		if !state.Running {
			detail := state.Status
			if state.Error != "" {
				detail += ": " + state.Error
			}
			return containerRuntime{}, errDocker("container " + ref + " is not running (" + strings.Trim(detail, ": ") + ")")
		}
		switch state.Health {
		case "healthy":
			return state, nil
		case "unhealthy":
			return containerRuntime{}, errDocker("container " + ref + " is unhealthy after update")
		case "":
			if stableFor <= 0 {
				return state, nil
			}
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= stableFor {
				return state, nil
			}
		default:
			stableSince = time.Time{}
		}

		waitFor := time.Second
		if !stableSince.IsZero() {
			if remaining := stableFor - time.Since(stableSince); remaining < waitFor {
				waitFor = remaining
			}
		}
		if waitFor <= 0 {
			continue
		}
		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return containerRuntime{}, errDocker("container " + ref + " did not become healthy: " + ctx.Err().Error())
		case <-timer.C:
		}
	}
}

func splitImage(ref string) (repo, tag string) {
	if i := strings.LastIndex(ref, "@"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	tag = "latest"
	if i := strings.LastIndex(ref, ":"); i > 0 && !strings.Contains(ref[i+1:], "/") {
		repo, tag = ref[:i], ref[i+1:]
	} else {
		repo = ref
	}
	return
}

func formatImageRef(repo, tag string) string {
	if strings.Contains(tag, ":") {
		return repo + "@" + tag
	}
	return repo + ":" + tag
}

func decodeLabels(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func matchLabel(labels map[string]string, filter string) bool {
	// filter form: "key" or "key=value"
	if k, v, ok := strings.Cut(filter, "="); ok {
		return labels[k] == v
	}
	_, ok := labels[filter]
	return ok
}

func buildCreateBody(cfg map[string]json.RawMessage, host json.RawMessage, nets map[string]json.RawMessage) io.Reader {
	body := map[string]json.RawMessage{}
	copyFields := []string{
		"Cmd", "Env", "Entrypoint", "WorkingDir", "Labels", "ExposedPorts",
		"Volumes", "User", "Tty", "OpenStdin", "StdinOnce", "StopSignal",
		"AttachStdin", "AttachStdout", "AttachStderr", "Hostname", "Domainname",
	}
	for _, f := range copyFields {
		if v, ok := cfg[f]; ok {
			body[f] = v
		}
	}
	if v, ok := cfg["Image"]; ok {
		body["Image"] = v
	}
	if len(host) > 0 {
		body["HostConfig"] = host
	}
	if len(nets) > 0 {
		netJSON, _ := json.Marshal(map[string]any{"EndpointsConfig": nets})
		body["NetworkingConfig"] = netJSON
	}
	b, _ := json.Marshal(body)
	return strings.NewReader(string(b))
}

// ---- update-type classification & rebuild/upgrade operations ----

// composeService is the subset of a compose service relevant to update strategy.
type composeService struct {
	build      bool
	pullPolicy string
}

type composeCacheEntry struct {
	mtime    int64
	services map[string]composeService
}

var (
	composeCacheMu sync.Mutex
	composeCache   = map[string]composeCacheEntry{}
)

// composeServiceInfo reads the compose file (cached by mtime) and returns the
// build/pull_policy flags for one service.
func composeServiceInfo(configFiles, service string) composeService {
	fi, err := os.Stat(configFiles)
	if err != nil {
		return composeService{}
	}
	mt := fi.ModTime().UnixNano()
	composeCacheMu.Lock()
	if e, ok := composeCache[configFiles]; ok && e.mtime == mt {
		composeCacheMu.Unlock()
		return e.services[service]
	}
	composeCacheMu.Unlock()

	data, err := os.ReadFile(configFiles)
	if err != nil {
		return composeService{}
	}
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return composeService{}
	}
	svcs := make(map[string]composeService, len(doc.Services))
	for name, m := range doc.Services {
		var cs composeService
		if _, ok := m["build"]; ok {
			cs.build = true
		}
		if pp, ok := m["pull_policy"].(string); ok {
			cs.pullPolicy = pp
		}
		svcs[name] = cs
	}
	composeCacheMu.Lock()
	composeCache[configFiles] = composeCacheEntry{mtime: mt, services: svcs}
	composeCacheMu.Unlock()
	return svcs[service]
}

func bulkUpdateEligible(updateType string) bool {
	return updateType == "latest" || updateType == "tag"
}

func registryScanEligible(updateType string) bool {
	return bulkUpdateEligible(updateType) || updateType == "unmanaged"
}

// classifyUpdate decides how a container can be updated, from its compose labels
// + configured image. Returns one of: latest | tag | unmanaged | pinned | build
// | local. Only valid Compose metadata can produce latest/tag; unmanaged images
// remain registry-scannable but are never rebuilt automatically.
func classifyUpdate(labels map[string]string, imageRef string) string {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" || isImageIDRef(imageRef) {
		return "local"
	}
	if _, digest, ok := strings.Cut(imageRef, "@"); ok && strings.Contains(digest, ":") {
		return "pinned"
	}
	if cf, _, svc, ok := composeMetaFromLabels(labels); ok {
		info := composeServiceInfo(cf, svc)
		if info.build {
			return "build"
		}
		if info.pullPolicy == "never" {
			return "local"
		}
		_, tag := splitImage(imageRef)
		if tag == "latest" || tag == "" {
			return "latest"
		}
		return "tag"
	}
	_, tag := splitImage(imageRef)
	if strings.EqualFold(tag, "local") {
		return "local"
	}
	return "unmanaged"
}

// composeMetaOf inspects a container and returns its compose rebuild metadata.
func (d *dockerClient) composeMetaOf(containerID string) (name, configFiles, workingDir, service string, ok bool) {
	resp, err := d.req(http.MethodGet, "/containers/"+containerID+"/json", nil)
	if err != nil {
		return "", "", "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", "", "", "", false
	}
	var ins struct {
		Name   string `json:"Name"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ins); err != nil {
		return "", "", "", "", false
	}
	cf, wd, svc, ok2 := composeMetaFromLabels(ins.Config.Labels)
	if !ok2 {
		return "", "", "", "", false
	}
	return strings.TrimPrefix(ins.Name, "/"), cf, wd, svc, true
}

// rebuildAction runs git pull (if the project is a git repo) then a compose build.
func (d *dockerClient) rebuildAction(containerID string) (string, error) {
	name, cf, wd, svc, ok := d.composeMetaOf(containerID)
	if !ok {
		return "", errDocker("not a compose container (project dir unavailable); cannot rebuild")
	}
	gitNote := ""
	if _, err := os.Stat(filepath.Join(wd, ".git")); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		cmd := exec.CommandContext(ctx, "git", "-C", wd, "pull", "--ff-only")
		var gb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &gb, &gb
		gerr := cmd.Run()
		cancel()
		if gerr != nil {
			return "", errDocker("git pull failed: " + tail(strings.TrimSpace(gb.String()), 300))
		}
		gitNote = "git pulled; "
	}
	detail, err := d.runComposeUpdate(name, cf, wd, svc)
	if err != nil && !errors.Is(err, errUpToDate) {
		return "", err
	}
	return gitNote + detail, err
}

// upgradeAction rewrites the service's image in the compose file (backing up the
// original to .bak) and recreates the container with the new image.
func (d *dockerClient) upgradeAction(containerID, newImage string) (string, error) {
	if newImage == "" {
		return "", errDocker("no new image specified")
	}
	name, cf, wd, svc, ok := d.composeMetaOf(containerID)
	if !ok {
		return "", errDocker("not a compose container (project dir unavailable); cannot upgrade")
	}
	bin, sub := composeBin()
	if bin == "" {
		return "", errDocker("docker compose not found on host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	// Pull before rewriting the Compose file so a registry/auth failure leaves
	// the service definition untouched.
	repoN, tagN := splitImage(newImage)
	if err := d.pullImageContext(ctx, repoN, tagN); err != nil {
		return "", err
	}
	if err := rewriteComposeImage(cf, svc, newImage); err != nil {
		return "", err
	}
	composeCacheMu.Lock() // we just rewrote the file: invalidate its cache entry
	delete(composeCache, cf)
	composeCacheMu.Unlock()

	args := make([]string, 0, len(sub)+9)
	args = append(args, sub...)
	args = append(args, "-f", cf, "--project-directory", wd,
		"up", "-d", "--no-deps", "--force-recreate", svc)
	cmd := exec.CommandContext(ctx, bin, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		output := tail(strings.TrimSpace(buf.String()), 400)
		if ctx.Err() == context.DeadlineExceeded {
			return "", errDocker("compose timed out (>9m): " + output)
		}
		if output == "" {
			output = err.Error()
		}
		return "", errDocker("compose failed: " + output)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 45*time.Second)
	defer readyCancel()
	if _, err := d.waitContainerReady(readyCtx, name); err != nil {
		return "", err
	}
	return "upgraded to " + newImage, nil
}

// rewriteComposeImage replaces services[svc].image with newImage in the compose
// file, backing up the original to <file>.bak. If newImage is a registry ref
// (contains "/"), a conflicting pull_policy: never is removed so the new image
// can be pulled.
func rewriteComposeImage(configFiles, service, newImage string) error {
	data, err := os.ReadFile(configFiles)
	if err != nil {
		return errDocker("read compose: " + err.Error())
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return errDocker("parse compose: " + err.Error())
	}
	services, _ := doc["services"].(map[string]any)
	svc, _ := services[service].(map[string]any)
	if svc == nil {
		return errDocker("service " + service + " not found in compose")
	}
	// Belt-and-suspenders: refuse a cross-major image change at the file level so
	// no caller can silently downgrade/upgrade a stateful image's major version
	// (the postgres:18-alpine → 9.6.24 crash-loop class). Only enforced when both
	// the existing and new tags parse as semver; non-semver (floating) tags fall
	// through unchanged.
	if oldImg, _ := svc["image"].(string); oldImg != "" {
		_, oldTag := splitImage(oldImg)
		_, newTag := splitImage(newImage)
		oldSV, oldOK := parseSemverTag(oldTag)
		newSV, newOK := parseSemverTag(newTag)
		if oldOK && newOK && oldSV.Major != newSV.Major {
			return errDocker("拒绝跨大版本改写镜像 " + oldImg + " → " + newImage + "（请手动确认）")
		}
	}
	bak := configFiles + ".bak"
	if err := os.WriteFile(bak, data, 0644); err != nil {
		return errDocker("backup compose: " + err.Error())
	}
	svc["image"] = newImage
	if strings.Contains(newImage, "/") {
		delete(svc, "pull_policy")
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return errDocker("marshal compose: " + err.Error())
	}
	if err := os.WriteFile(configFiles, out, 0644); err != nil {
		return errDocker("write compose: " + err.Error())
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// sentinels for update outcomes that are not errors but must not be reported as
// "updated" either (a label-filtered skip, or an image that was already current).
var (
	errSkip     = errors.New("container skipped")
	errUpToDate = errors.New("already up to date")
)

type dockerErr string

func (e dockerErr) Error() string { return string(e) }
func errDocker(s string) error    { return dockerErr(s) }
