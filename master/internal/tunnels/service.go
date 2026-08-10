// Package tunnels powers the 隧道 (Tunnels) panel: create a Cloudflare Tunnel on
// a chosen node (the master creates the tunnel via the Cloudflare API and the
// node runs cloudflared), monitor each tunnel's CF-side health + node-side
// cloudflared process, and start/stop/delete them.
//
// Tunnels created here are remotely-managed (config_src=cloudflare): the master
// holds the connector token (persisted in tunnel_tokens) and the node runs
// `cloudflared tunnel run --token <token>` under a systemd unit. Ingress rules
// for the same tunnel are managed by the domains panel. node↔tunnel linkage uses
// the tunnel_tokens table, NOT nodes.tunnel_id — the latter is an agent-reported
// cache and unreliable on NPM/external-ingress nodes (see nodepanel-ingress-type).
package tunnels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"nodepanel/master/internal/cloudflare"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/nodes"
	"nodepanel/master/internal/store"
)

const noTokenMsg = "未配置 Cloudflare 令牌，请先到「设置 → Cloudflare」配置 API token"

// Service implements the /api/tunnels endpoints.
type Service struct {
	Store *store.Store
	Nodes *nodes.Service // for ExecSync (node provisioning/monitoring) + Hub.Online
}

// cfClient builds a Cloudflare client from the saved 'cloudflare' setting, or
// nil when none is configured. Mirrors domains.Service.cfClient.
func (s *Service) cfClient(ctx context.Context) *cloudflare.Client {
	raw, _ := s.Store.GetSetting(ctx, "cloudflare")
	if raw == "" {
		return nil
	}
	var c struct {
		APIToken string `json:"api_token"`
	}
	if json.Unmarshal([]byte(raw), &c) != nil || strings.TrimSpace(c.APIToken) == "" {
		return nil
	}
	return cloudflare.New(c.APIToken)
}

// --- response shapes ---

type nodeRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type tunnelOut struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Status  string   `json:"status,omitempty"`  // CF-side: healthy/degraded/down/inactive
	Node    *nodeRef `json:"node,omitempty"`    // linked node
	Process string   `json:"process,omitempty"` // node systemd state: active/inactive/failed/activating
	Version string   `json:"version,omitempty"` // cloudflared version on node
	Online  bool     `json:"online"`            // node reachable for probing
	Managed bool     `json:"managed"`           // created from panel → start/stop/delete enabled
}

// List GET /api/tunnels — every tunnel's CF status, its linked node, and (for
// online linked nodes) the node-side cloudflared process state + version. The
// per-node status probes run concurrently.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	ctx := r.Context()
	tuns, err := cf.ListTunnels(ctx)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取 tunnel 列表失败: "+err.Error())
		return
	}
	tokMap, _ := s.Store.ListTunnelTokens(ctx)
	nodeList, _ := s.Store.ListNodes(ctx)
	nodeByID := map[string]store.Node{}
	for _, n := range nodeList {
		nodeByID[n.ID] = n
	}

	// Resolve each tunnel's linked node + runtime model. Panel token map first
	// (reliable → systemd unit); else a node whose agent-reported tunnel_id cache
	// matches (best-effort → cloudflared runs in a Docker container).
	type link struct {
		nodeID  string
		managed bool
		runtime string // "systemd" (panel) or "docker" (hand-built)
	}
	links := make([]link, len(tuns))
	for i, t := range tuns {
		nid, rt := resolveTunnelLink(tokMap, nodeList, t.ID)
		links[i] = link{nodeID: nid, runtime: rt, managed: rt == "systemd"}
	}

	// Probe node-side state concurrently for online linked tunnels.
	type probe struct {
		online  bool
		process string
		version string
	}
	probes := make([]probe, len(tuns))
	var wg sync.WaitGroup
	for i := range tuns {
		lk := links[i]
		if lk.nodeID == "" || !s.Nodes.Hub.Online(lk.nodeID) {
			continue
		}
		// Pick the right status probe for how this tunnel's cloudflared runs.
		var cmd string
		if lk.runtime == "systemd" {
			safe, err := safeName(tuns[i].Name)
			if err != nil {
				continue // odd name → can't map to a systemd unit
			}
			cmd = statusCmd(safe)
		} else {
			cmd = dockerStatusCmd(tuns[i].ID)
		}
		wg.Add(1)
		go func(i int, nodeID, cmd string) {
			defer wg.Done()
			probes[i].online = true
			out, _, e := s.Nodes.ExecSync(ctx, nodeID, cmd, 15*time.Second)
			if e != nil {
				return
			}
			for _, line := range strings.Split(out, "\n") {
				switch {
				case strings.HasPrefix(line, "PROC="):
					probes[i].process = strings.TrimSpace(strings.TrimPrefix(line, "PROC="))
				case strings.HasPrefix(line, "VER="):
					probes[i].version = strings.TrimSpace(strings.TrimPrefix(line, "VER="))
				}
			}
		}(i, lk.nodeID, cmd)
	}
	wg.Wait()

	out := make([]tunnelOut, 0, len(tuns))
	for i, t := range tuns {
		lk := links[i]
		p := probes[i]
		to := tunnelOut{
			ID:      t.ID,
			Name:    t.Name,
			Status:  t.Status,
			Managed: lk.managed,
			Online:  p.online,
			Process: p.process,
			Version: p.version,
		}
		if lk.nodeID != "" {
			if n, ok := nodeByID[lk.nodeID]; ok {
				to.Node = &nodeRef{ID: n.ID, Name: n.Name}
			}
		}
		out = append(out, to)
	}
	httpx.OK(w, map[string]any{"tunnels": out})
}

// Create POST /api/tunnels {node_id, name} — creates a remotely-managed tunnel
// at Cloudflare, then installs cloudflared + a systemd unit on the node. Rolls
// the CF tunnel back on any provisioning failure. Persists the connector token.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string `json:"node_id"`
		Name   string `json:"name"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求无效")
		return
	}
	body.NodeID = strings.TrimSpace(body.NodeID)
	body.Name = strings.TrimSpace(body.Name)
	safe, err := safeName(body.Name)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.NodeID == "" {
		httpx.Err(w, http.StatusBadRequest, "请选择节点")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	ctx := r.Context()

	n, err := s.Store.GetNode(ctx, body.NodeID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "节点不存在")
		return
	}
	if !s.Nodes.Hub.Online(body.NodeID) {
		httpx.Err(w, http.StatusServiceUnavailable, "节点离线，无法安装 cloudflared")
		return
	}

	// v1: one panel tunnel per node.
	tokMap, _ := s.Store.ListTunnelTokens(ctx)
	for _, tt := range tokMap {
		if tt.NodeID == body.NodeID {
			httpx.Err(w, http.StatusConflict, "该节点已有关联隧道，请先删除旧隧道（每节点限一个面板隧道）")
			return
		}
	}

	// 1. Create the tunnel at Cloudflare.
	tunnelID, token, err := cf.CreateTunnel(ctx, body.Name)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "创建 tunnel 失败: "+err.Error())
		return
	}

	// 2. Provision on the node: install cloudflared, then write unit + enable.
	// Roll the CF tunnel back on failure so we never leave an orphan.
	if _, _, err := s.Nodes.ExecSync(ctx, body.NodeID, installCmd(), 180*time.Second); err != nil {
		_ = cf.DeleteTunnel(ctx, tunnelID)
		httpx.Err(w, http.StatusBadGateway, "节点安装 cloudflared 失败: "+err.Error()+"（已回滚 CF tunnel）")
		return
	}
	if _, _, err := s.Nodes.ExecSync(ctx, body.NodeID, unitCmd(safe, token), 60*time.Second); err != nil {
		_, _, _ = s.Nodes.ExecSync(ctx, body.NodeID, cleanupCmd(safe), 20*time.Second)
		_ = cf.DeleteTunnel(ctx, tunnelID)
		httpx.Err(w, http.StatusBadGateway, "节点启动 cloudflared 服务失败: "+err.Error()+"（已回滚）")
		return
	}

	// 3. Persist linkage + token.
	_ = s.Store.SetNodeTunnelID(ctx, body.NodeID, tunnelID)
	_ = s.Store.SetTunnelToken(ctx, tunnelID, token, body.NodeID)
	s.Store.Audit(ctx, "admin", "tunnel.create", fmt.Sprintf("%s on %s (%s)", body.Name, n.Name, tunnelID))

	httpx.OK(w, map[string]any{
		"id":     tunnelID,
		"name":   body.Name,
		"node":   nodeRef{ID: n.ID, Name: n.Name},
		"status": "已创建并启动，CF 状态将在约 10 秒内变为 healthy",
	})
}

// Start POST /api/tunnels/{id}/start
func (s *Service) Start(w http.ResponseWriter, r *http.Request) { s.svcCtl(w, r, true) }

// Stop POST /api/tunnels/{id}/stop
func (s *Service) Stop(w http.ResponseWriter, r *http.Request) { s.svcCtl(w, r, false) }

// svcCtl starts or stops the cloudflared that runs a tunnel, regardless of who
// created it. It resolves the linked node + how cloudflared runs there:
//   - "systemd" (panel-created, token row)  → systemctl start/stop cloudflared-<name>
//   - "docker"  (hand-built, node cache)    → docker start/stop <container>
//
// A tunnel with no linked node can't be controlled from here (its cloudflared is
// not on a managed node); delete/rename still work via the Cloudflare API.
func (s *Service) svcCtl(w http.ResponseWriter, r *http.Request, start bool) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httpx.Err(w, http.StatusBadRequest, "缺少 tunnel id")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	ctx := r.Context()
	nodeID, runtime := s.resolveLink(ctx, id)
	if nodeID == "" {
		httpx.Err(w, http.StatusBadRequest, "未关联节点，无法启停（该隧道的 cloudflared 不在受管节点上，请到 Cloudflare 控制台操作）")
		return
	}
	if !s.Nodes.Hub.Online(nodeID) {
		httpx.Err(w, http.StatusServiceUnavailable, "节点离线")
		return
	}
	tun, _, err := cf.GetTunnel(ctx, id)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取 tunnel 失败: "+err.Error())
		return
	}
	action := "stop"
	var cmd string
	if runtime == "systemd" {
		safe, err := safeName(tun.Name)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, "隧道名无法对应 systemd 单元: "+tun.Name)
			return
		}
		if start {
			action = "start"
			cmd = startCmd(safe)
		} else {
			cmd = stopCmd(safe)
		}
	} else {
		if start {
			action = "start"
			cmd = dockerCtlCmd(id, "start")
		} else {
			cmd = dockerCtlCmd(id, "stop")
		}
	}
	if _, _, err := s.Nodes.ExecSync(ctx, nodeID, cmd, 30*time.Second); err != nil {
		httpx.Err(w, http.StatusBadGateway, fmt.Sprintf("%s 失败: %s", action, err.Error()))
		return
	}
	s.Store.Audit(ctx, "admin", "tunnel."+action, fmt.Sprintf("%s (%s)", tun.Name, id))
	httpx.OK(w, map[string]any{"id": id, "action": action})
}

// Delete DELETE /api/tunnels/{id} — stops the node cloudflared (so CF has no
// active connections to refuse the delete), deletes the CF tunnel, then cleans
// the node (systemd unit or Docker container), removes the DNS CNAMEs that
// pointed ingress hostnames at it, and clears DB linkage. Works for any tunnel;
// node cleanup is best-effort when offline, DNS cleanup is CF-side and still runs.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httpx.Err(w, http.StatusBadRequest, "缺少 tunnel id")
		return
	}
	ctx := r.Context()
	cf := s.cfClient(ctx)
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}

	nodeID, runtime := s.resolveLink(ctx, id)

	// Capture name + ingress hostnames BEFORE deleting (for node + DNS cleanup).
	var name string
	var hostnames []string
	if cfg, err := cf.GetConfig(ctx, id); err == nil {
		for _, rule := range cloudflare.IngressFromConfig(cfg) {
			if h := rule.Hostname(); h != "" {
				hostnames = append(hostnames, h)
			}
		}
	}
	if tun, _, err := cf.GetTunnel(ctx, id); err == nil {
		name = tun.Name
	}

	var notes []string
	online := nodeID != "" && s.Nodes.Hub.Online(nodeID)
	// Stop the node's cloudflared first so CF will accept the delete.
	if online {
		if runtime == "systemd" && name != "" {
			if safe, e := safeName(name); e == nil {
				_, _, _ = s.Nodes.ExecSync(ctx, nodeID, stopCmd(safe), 20*time.Second)
			}
		} else if runtime == "docker" {
			_, _, _ = s.Nodes.ExecSync(ctx, nodeID, dockerCtlCmd(id, "stop"), 20*time.Second)
		}
	} else if nodeID != "" {
		notes = append(notes, "节点离线，未能停止节点 cloudflared；若 CF 报「活跃连接」请将节点上线后重试，或到 CF 控制台强删。")
	}

	if err := cf.DeleteTunnel(ctx, id); err != nil {
		httpx.Err(w, http.StatusBadGateway, "删除 CF tunnel 失败: "+err.Error())
		return
	}

	// Success → remove the node container/unit.
	if online {
		if runtime == "systemd" && name != "" {
			if safe, e := safeName(name); e == nil {
				_, _, _ = s.Nodes.ExecSync(ctx, nodeID, cleanupCmd(safe), 30*time.Second)
			}
		} else if runtime == "docker" {
			_, _, _ = s.Nodes.ExecSync(ctx, nodeID, dockerCtlCmd(id, "rm"), 30*time.Second)
		}
	}
	// DNS: drop the CNAME records that routed these hostnames through the tunnel.
	for _, h := range hostnames {
		if err := cf.DeleteCNAME(ctx, h); err != nil {
			notes = append(notes, "部分 DNS 记录删除失败，请到 Cloudflare 控制台核对 "+h)
			break
		}
	}
	if nodeID != "" {
		_ = s.Store.SetNodeTunnelID(ctx, nodeID, "")
	}
	_ = s.Store.DeleteTunnelToken(ctx, id)
	s.Store.Audit(ctx, "admin", "tunnel.delete", fmt.Sprintf("%s (%s)", name, id))
	httpx.OK(w, map[string]any{"deleted": true, "id": id, "note": strings.Join(notes, "；")})
}

// Rename PATCH /api/tunnels/{id} {name} — renames a tunnel at Cloudflare. Works
// for any tunnel; the id is unchanged so a running connector is unaffected.
func (s *Service) Rename(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httpx.Err(w, http.StatusBadRequest, "缺少 tunnel id")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求无效")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if _, err := safeName(body.Name); err != nil {
		httpx.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	if err := cf.RenameTunnel(r.Context(), id, body.Name); err != nil {
		httpx.Err(w, http.StatusBadGateway, "重命名 tunnel 失败: "+err.Error())
		return
	}
	s.Store.Audit(r.Context(), "admin", "tunnel.rename", fmt.Sprintf("%s → %s", id, body.Name))
	httpx.OK(w, map[string]any{"id": id, "name": body.Name})
}

// resolveLink returns the node id running a tunnel's cloudflared + how it runs:
// "systemd" (panel-created, from tunnel_tokens) or "docker" (hand-built, from
// the agent-reported nodes.tunnel_id cache). Both empty when no node is linked.
func (s *Service) resolveLink(ctx context.Context, id string) (nodeID, runtime string) {
	tokMap, _ := s.Store.ListTunnelTokens(ctx)
	nodeList, _ := s.Store.ListNodes(ctx)
	return resolveTunnelLink(tokMap, nodeList, id)
}

// resolveTunnelLink is the pure (preloaded-maps) core of resolveLink, also used
// by List which already has both maps in hand.
func resolveTunnelLink(tokMap map[string]store.TunnelToken, nodeList []store.Node, tunnelID string) (nodeID, runtime string) {
	if tt, ok := tokMap[tunnelID]; ok {
		return tt.NodeID, "systemd"
	}
	for _, n := range nodeList {
		if n.TunnelID == tunnelID {
			return n.ID, "docker"
		}
	}
	return "", ""
}
