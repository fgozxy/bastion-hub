package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/nodes"
	"nodepanel/master/internal/store"
	"nodepanel/master/internal/telegram"
)

// netdataKickstartCmd installs Netdata WITHOUT claiming to Netdata Cloud, bound
// only to loopback. --dont-start-it lets us write the bind-to override before
// the first start, so 19999 is never exposed publicly (even briefly). The agent
// reads it later via MsgHTTPFetch to 127.0.0.1:19999.
const netdataKickstartCmd = `curl -Ss https://get.netdata.cloud/kickstart.sh -o /tmp/kickstart.sh && ` +
	`sh /tmp/kickstart.sh --non-interactive --stable-channel --disable-telemetry --dont-start-it && ` +
	`printf '[web]\nbind to = 127.0.0.1\n' > /etc/netdata/netdata.conf && ` +
	`systemctl restart netdata`

// netdataUninstallCmd removes Netdata via the official kickstart uninstaller,
// symmetric to the install command. --yes skips the confirmation prompt; `yes |`
// is a belt-and-suspenders guard in case the invoked uninstaller reads stdin
// before flags. This actually frees Netdata's memory/CPU on the host — the point
// of letting low-spec nodes turn monitoring off (a running-but-disabled Netdata
// still eats ~100MB). Upstream bug #13811 can leave a partial uninstall with a
// non-zero exit; uninstallOne only marks the node uninstalled on exit 0, so a
// botched cleanup stays visible as a failure rather than a silent mismatch.
const netdataUninstallCmd = `curl -Ss https://get.netdata.cloud/kickstart.sh -o /tmp/kickstart.sh && ` +
	`yes | sh /tmp/kickstart.sh --uninstall --yes`

// Service wires the health panel to its dependencies.
type Service struct {
	Store   *store.Store
	Hub     *agenthub.Hub
	Nodes   *nodes.Service
	TG      *telegram.Service
	cache   *Cache
	mounts  map[string]mountCacheEntry // nodeID → discovered disk mounts (TTL ~1h)
	mountMu sync.Mutex
}

// New builds a health service. Call Start to launch the poller.
func New(st *store.Store, hub *agenthub.Hub, nodesSvc *nodes.Service, tg *telegram.Service) *Service {
	return &Service{Store: st, Hub: hub, Nodes: nodesSvc, TG: tg, cache: newCache(), mounts: map[string]mountCacheEntry{}}
}

// Start launches the metric-poller / alert-evaluator goroutine.
func (s *Service) Start() { go s.loop() }

// nodeStatus is one row of GET /api/health.
type nodeStatus struct {
	NodeID            string  `json:"node_id"`
	Name              string  `json:"name"`
	Online            bool    `json:"online"`
	AgentVersion      string  `json:"agent_version"`
	SupportsHTTPFetch bool    `json:"supports_http_fetch"`
	Enabled           bool    `json:"enabled"`
	Installed         bool    `json:"installed"`
	Sample            *Sample `json:"sample,omitempty"`
}

// Status GET /api/health — per-node health view (online, netdata enabled, agent
// capability, latest cached sample).
func (s *Service) Status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all, err := s.Store.ListNodes(ctx)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	hnodes, _ := s.Store.ListHealthNodes(ctx)
	enabled := make(map[string]bool, len(hnodes))
	installed := make(map[string]bool, len(hnodes))
	for _, h := range hnodes {
		enabled[h.NodeID] = h.Enabled
		installed[h.NodeID] = h.InstalledAt > 0
	}
	out := make([]nodeStatus, 0, len(all))
	for _, n := range all {
		ns := nodeStatus{
			NodeID:            n.ID,
			Name:              n.Name,
			Online:            s.Hub.Online(n.ID),
			AgentVersion:      n.AgentVersion,
			SupportsHTTPFetch: supportsHTTPFetch(n.AgentVersion),
			Enabled:           enabled[n.ID],
			Installed:         installed[n.ID],
		}
		if sp, ok := s.cache.Latest(n.ID); ok {
			ns.Sample = &sp
		}
		out = append(out, ns)
	}
	httpx.OK(w, out)
}

// Install POST /api/health/install {node_ids:[...]} — batch-install Netdata on
// the selected nodes concurrently. Mirrors nodes.UpdateAgents' fan-out + per-node
// result shape.
func (s *Service) Install(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeIDs []string `json:"node_ids"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.NodeIDs) == 0 {
		httpx.Err(w, 400, "node_ids required")
		return
	}
	nameOf := map[string]string{}
	if nodes, err := s.Store.ListNodes(r.Context()); err == nil {
		for _, n := range nodes {
			nameOf[n.ID] = n.Name
		}
	}
	type res struct {
		NodeID string `json:"node_id"`
		Name   string `json:"name,omitempty"`
		OK     bool   `json:"ok"`
		Err    string `json:"err,omitempty"`
		Online bool   `json:"online"`
	}
	out := make([]res, len(body.NodeIDs))
	ctx := r.Context()
	var wg sync.WaitGroup
	for i, id := range body.NodeIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			rr := res{NodeID: id, Name: nameOf[id], Online: s.Hub.Online(id)}
			if !rr.Online {
				rr.Err = "节点离线"
				out[i] = rr
				return
			}
			cores, err := s.installOne(ctx, id)
			if err != nil {
				rr.Err = err.Error()
				out[i] = rr
				return
			}
			_ = s.Store.EnableHealthNode(ctx, id)
			if cores > 0 {
				_ = s.Store.SetHealthNodeCores(ctx, id, cores)
			}
			// Seed the template's default alert rules for this node (no-op if it
			// already has rules, so re-installs never pile up dupes).
			_ = s.Store.SeedDefaultAlerts(ctx, id, templateAlerts(s.loadTemplate()))
			rr.OK = true
			out[i] = rr
		}(i, id)
	}
	wg.Wait()
	s.Store.Audit(r.Context(), auth.UserID(r.Context()), "health.install_netdata", fmt.Sprintf("%d nodes", len(body.NodeIDs)))
	httpx.OK(w, out)
}

// Uninstall POST /api/health/uninstall {node_ids:[...]} — batch-REMOVE Netdata
// from the selected nodes concurrently, freeing its memory/CPU. Mirrors Install's
// fan-out + per-node result shape. Only a node whose uninstall script exits 0 is
// marked uninstalled; a failed/partial cleanup (upstream bug #13811) stays
// installed so the UI never claims a node is clean while Netdata still runs.
func (s *Service) Uninstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeIDs []string `json:"node_ids"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.NodeIDs) == 0 {
		httpx.Err(w, 400, "node_ids required")
		return
	}
	nameOf := map[string]string{}
	if nodes, err := s.Store.ListNodes(r.Context()); err == nil {
		for _, n := range nodes {
			nameOf[n.ID] = n.Name
		}
	}
	type res struct {
		NodeID string `json:"node_id"`
		Name   string `json:"name,omitempty"`
		OK     bool   `json:"ok"`
		Err    string `json:"err,omitempty"`
		Online bool   `json:"online"`
	}
	out := make([]res, len(body.NodeIDs))
	ctx := r.Context()
	var wg sync.WaitGroup
	for i, id := range body.NodeIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			rr := res{NodeID: id, Name: nameOf[id], Online: s.Hub.Online(id)}
			if !rr.Online {
				rr.Err = "节点离线"
				out[i] = rr
				return
			}
			if err := s.uninstallOne(ctx, id); err != nil {
				rr.Err = err.Error()
				out[i] = rr
				return
			}
			_ = s.Store.UninstallHealthNode(ctx, id)
			s.cache.Delete(id) // drop stale metrics so the card clears immediately
			rr.OK = true
			out[i] = rr
		}(i, id)
	}
	wg.Wait()
	s.Store.Audit(r.Context(), auth.UserID(r.Context()), "health.uninstall_netdata", fmt.Sprintf("%d nodes", len(body.NodeIDs)))
	httpx.OK(w, out)
}

// uninstallOne runs the kickstart uninstaller on a node. Returns nil only when
// the script exits 0, so the caller only marks the node uninstalled on a clean
// removal.
func (s *Service) uninstallOne(ctx context.Context, nodeID string) error {
	out, exit, err := s.Nodes.ExecSync(ctx, nodeID, netdataUninstallCmd, 200*time.Second)
	if err != nil {
		return fmt.Errorf("执行失败: %v", err)
	}
	if exit != 0 {
		msg := strings.TrimSpace(out)
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg == "" {
			msg = fmt.Sprintf("exit %d", exit)
		}
		return fmt.Errorf("卸载脚本失败: %s", msg)
	}
	return nil
}

// installOne runs the kickstart on a node and verifies Netdata answers on
// loopback. Returns the node's CPU core count (parsed from /api/v1/info) on
// success, for the per-node load alert.
func (s *Service) installOne(ctx context.Context, nodeID string) (int, error) {
	out, exit, err := s.Nodes.ExecSync(ctx, nodeID, netdataKickstartCmd, 200*time.Second)
	if err != nil {
		return 0, fmt.Errorf("执行失败: %v", err)
	}
	if exit != 0 {
		msg := strings.TrimSpace(out)
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg == "" {
			msg = fmt.Sprintf("exit %d", exit)
		}
		return 0, fmt.Errorf("安装脚本失败: %s", msg)
	}
	// Verify Netdata answers on loopback. On first boot Netdata spins up dozens
	// of collectors (ebpf, go.d, proc, cgroups, …) and returns 503 until ready,
	// which on small / slow nodes (1-2 core VPS) can take 10-30s. A single fixed
	// wait races and throws a false "Netdata 返回 503" failure even though the
	// install actually succeeded — so poll with backoff until it serves 200.
	return s.waitForNetdata(ctx, nodeID)
}

// waitForNetdata polls the node-local Netdata /api/v1/info until it answers 200,
// treating any non-200 status or transient fetch error as "still starting" and
// retrying every 3s up to a ~75s ceiling. Returns the parsed core count.
func (s *Service) waitForNetdata(ctx context.Context, nodeID string) (int, error) {
	const infoURL = "http://127.0.0.1:19999/api/v1/info"
	deadline := time.Now().Add(75 * time.Second)
	for {
		r, _ := s.HTTPFetch(ctx, nodeID, infoURL, 8*time.Second)
		if r.Err == "" && r.Status == 200 {
			return parseCores(r.Body), nil
		}
		if time.Now().After(deadline) {
			if r.Err != "" {
				return 0, fmt.Errorf("Netdata 未响应: %s", r.Err)
			}
			return 0, fmt.Errorf("Netdata 启动超时（持续返回 %d）", r.Status)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// Metrics GET /api/health/metrics?node_id=&window= — cached rolling history.
func (s *Service) Metrics(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		httpx.Err(w, 400, "node_id required")
		return
	}
	window := 180
	if v := r.URL.Query().Get("window"); v != "" {
		if n, err := parseIntDefault(v, 180); err == nil {
			window = n
		}
	}
	since := time.Now().Unix() - int64(window)
	httpx.OK(w, s.cache.History(nodeID, since))
}

// ListAlerts GET /api/health/alerts?node_id=
func (s *Service) ListAlerts(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		httpx.Err(w, 400, "node_id required")
		return
	}
	alerts, err := s.Store.ListHealthAlerts(r.Context(), nodeID)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, alerts)
}

// PutAlert PUT /api/health/alerts {id?,node_id,metric,threshold,window_sec,enabled}
func (s *Service) PutAlert(w http.ResponseWriter, r *http.Request) {
	var a store.HealthAlert
	if err := httpx.ReadJSON(r, &a); err != nil {
		httpx.Err(w, 400, "bad body: "+err.Error())
		return
	}
	if a.NodeID == "" || a.Metric == "" {
		httpx.Err(w, 400, "node_id and metric required")
		return
	}
	if _, ok := (Sample{}).Value(a.Metric); !ok {
		httpx.Err(w, 400, "unsupported metric: "+a.Metric)
		return
	}
	if err := s.Store.PutHealthAlert(r.Context(), &a); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, a)
}

// DeleteAlert DELETE /api/health/alerts/{id}
func (s *Service) DeleteAlert(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httpx.Err(w, 400, "id required")
		return
	}
	if err := s.Store.DeleteHealthAlert(r.Context(), id); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// TestFetch POST /api/health/test-fetch {node_id,url} — run HTTPFetch and return
// the raw result. Debug helper for the UI.
func (s *Service) TestFetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string `json:"node_id"`
		URL    string `json:"url"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "bad body")
		return
	}
	if body.URL == "" {
		body.URL = "http://127.0.0.1:19999/api/v1/info"
	}
	res, _ := s.HTTPFetch(r.Context(), body.NodeID, body.URL, 12*time.Second)
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(res)
	w.Write(b)
}

func parseIntDefault(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return def, err
	}
	return n, nil
}

// parseCores pulls cores_total out of a Netdata /api/v1/info body (a JSON string
// like "cores_total":"1"). 0 if unreadable. Used to scale the per-node default
// load alert (cores × 2) and for display.
func parseCores(infoBody string) int {
	var v struct {
		CoresTotal string `json:"cores_total"`
	}
	if json.Unmarshal([]byte(infoBody), &v) != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v.CoresTotal))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// templateAlerts converts the template's default alert defs into per-node
// HealthAlert rows (id/node_id are filled by the store on insert).
func templateAlerts(t Template) []store.HealthAlert {
	out := make([]store.HealthAlert, 0, len(t.Alerts))
	for _, a := range t.Alerts {
		out = append(out, store.HealthAlert{Metric: a.Metric, Threshold: a.Threshold, WindowSec: a.WindowSec, Enabled: true})
	}
	return out
}

// GetTemplate GET /api/health/template — current effective template, the metric
// catalog (labels/units/chart types for rendering), and the factory default.
func (s *Service) GetTemplate(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, map[string]any{
		"template": s.loadTemplate(),
		"catalog":  Catalog,
		"default":  DefaultTemplate(),
	})
}

// PutTemplate PUT /api/health/template — save a customized template. Unknown
// metric keys are filtered against the catalog; at least one metric is required.
func (s *Service) PutTemplate(w http.ResponseWriter, r *http.Request) {
	var t Template
	if err := httpx.ReadJSON(r, &t); err != nil {
		httpx.Err(w, 400, "bad body: "+err.Error())
		return
	}
	valid := map[string]bool{}
	for _, m := range Catalog {
		valid[m.Key] = true
	}
	var cleaned []string
	for _, k := range t.Enabled {
		if valid[k] {
			cleaned = append(cleaned, k)
		}
	}
	if len(cleaned) == 0 {
		httpx.Err(w, 400, "至少启用一个指标")
		return
	}
	t.Enabled = cleaned
	raw, _ := json.Marshal(t)
	if err := s.Store.SetSetting(r.Context(), "health_template", string(raw)); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), auth.UserID(r.Context()), "health.template_update", "enabled="+strings.Join(t.Enabled, ","))
	httpx.OK(w, map[string]any{"template": t, "catalog": Catalog, "default": DefaultTemplate()})
}

// ResetTemplate POST /api/health/template/reset — restore the default template
// AND re-seed every installed node's alerts to those defaults (clean slate).
func (s *Service) ResetTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	def := DefaultTemplate()
	_ = s.Store.SetSetting(ctx, "health_template", "") // clear customization → loadTemplate yields default
	defaults := templateAlerts(def)
	if hnodes, err := s.Store.ListHealthNodes(ctx); err == nil {
		for _, hn := range hnodes {
			if hn.InstalledAt > 0 {
				_ = s.Store.ResetNodeAlertsToDefault(ctx, hn.NodeID, defaults)
			}
		}
	}
	s.Store.Audit(ctx, auth.UserID(ctx), "health.template_reset", "reseeded default alerts")
	httpx.OK(w, map[string]any{"template": def, "catalog": Catalog, "default": def})
}
