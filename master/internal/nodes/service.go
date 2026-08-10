// Package nodes implements the admin node-management HTTP endpoints.
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/browserhub"
	"nodepanel/master/internal/config"
	"nodepanel/master/internal/geoip"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/komari"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

// Service wires node endpoints to their dependencies.
type Service struct {
	Store   *store.Store
	Hub     *agenthub.Hub
	Browser *browserhub.Hub
	Geo     *geoip.Resolver
	Cfg     config.Config
}

func (s *Service) baseURL() string {
	if u, _ := s.Store.GetSetting(context.Background(), "public_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	if s.Cfg.Domain != "" {
		return "https://" + s.Cfg.Domain
	}
	return "http://localhost" + s.Cfg.DevAddr
}

// InstallCommand builds the one-liner shown in the UI. The token goes into the
// URL query so the install.sh endpoint can validate it and render a script with
// the token baked in.
func (s *Service) InstallCommand(token string) string {
	return `curl -fsSL "` + s.baseURL() + `/install.sh?token=` + token + `" | bash`
}

type nodeView struct {
	store.Node
	Online     bool   `json:"online"`
	InstallCmd string `json:"install_cmd,omitempty"`
}

func (s *Service) view(n *store.Node, includeCmd bool) nodeView {
	v := nodeView{Node: *n, Online: s.Hub.Online(n.ID)}
	v.EnrollmentToken = "" // never leak in bulk
	if includeCmd && n.EnrollmentToken != "" {
		v.InstallCmd = s.InstallCommand(n.EnrollmentToken)
	}
	return v
}

// List GET /api/nodes
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListNodes(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	out := make([]nodeView, 0, len(list))
	for i := range list {
		out = append(out, s.view(&list[i], false))
	}
	httpx.OK(w, out)
}

// Create POST /api/nodes {name}
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = "New Node"
	}
	n, err := s.Store.CreateNode(r.Context(), body.Name, uuid.NewString())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.create", n.Name)
	httpx.OK(w, s.view(n, true))
}

// Rename PATCH /api/nodes/{id} {name, ssh_port}
func (s *Service) Rename(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Name    string `json:"name"`
		SshPort string `json:"ssh_port"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	if err := s.Store.RenameNode(r.Context(), id, body.Name, body.SshPort); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.rename", id+": "+body.Name)
	n, _ := s.Store.GetNode(r.Context(), id)
	if n != nil {
		s.Browser.Broadcast(browserhub.NewOut("node.update", s.view(n, false)))
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// SetBaseDomain PUT /api/nodes/{id}/base-domain {base_domain}
// Sets the node's primary base domain, used when migrating containers in to
// rewrite their public hostname (a.a.com → a.<base_domain>). Empty clears it.
func (s *Service) SetBaseDomain(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		BaseDomain string `json:"base_domain"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	base := strings.TrimSpace(body.BaseDomain)
	if err := s.Store.SetNodeBaseDomain(r.Context(), id, base); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.base_domain", id+": "+base)
	if n, _ := s.Store.GetNode(r.Context(), id); n != nil {
		s.Browser.Broadcast(browserhub.NewOut("node.update", s.view(n, false)))
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// SetIngressType PUT /api/nodes/{id}/ingress-type {ingress_type}
// Sets the node's declared public-entry method: "cftunnel" (default/empty) or
// "external" (NPM/manual, no tunnel). Admin-set policy that drives feature
// gating (see Node.SupportsCFDomain) — runtime safety still keys off TunnelID.
func (s *Service) SetIngressType(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		IngressType string `json:"ingress_type"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	t := strings.TrimSpace(body.IngressType)
	switch t {
	case "", "cftunnel", "external":
	default:
		httpx.Err(w, 400, "ingress_type 必须是 cftunnel 或 external")
		return
	}
	if err := s.Store.SetNodeIngressType(r.Context(), id, t); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.ingress_type", id+": "+t)
	if n, _ := s.Store.GetNode(r.Context(), id); n != nil {
		s.Browser.Broadcast(browserhub.NewOut("node.update", s.view(n, false)))
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// Regenerate POST /api/nodes/{id}/regenerate
func (s *Service) Regenerate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tok := uuid.NewString()
	if err := s.Store.RegenerateEnrollment(r.Context(), id, tok); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.regenerate", id)
	httpx.OK(w, map[string]string{"install_cmd": s.InstallCommand(tok), "token": tok})
}

// Delete DELETE /api/nodes/{id}
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = s.Store.DeleteNode(r.Context(), id)
	s.Store.Audit(r.Context(), "admin", "node.delete", id)
	httpx.OK(w, map[string]string{"ok": "1"})
}

// UpdateAgent POST /api/nodes/{id}/update-agent — in-place upgrade of the node's
// agent to the latest binary served at /dl/. The agent downloads + replaces its
// own binary and restarts via systemd.
func (s *Service) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.Hub.Online(id) {
		httpx.Err(w, 409, "node offline")
		return
	}
	res, err := s.updateOneAgent(r.Context(), id)
	if err != nil {
		httpx.Err(w, 504, err.Error())
		return
	}
	if !res.OK {
		httpx.Err(w, 502, res.Err)
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.update_agent", id)
	httpx.OK(w, res)
}

// updateOneAgent sends an agent_update request to one node and waits for the
// result. Shared by the single-node and batch endpoints.
func (s *Service) updateOneAgent(ctx context.Context, id string) (proto.AgentUpdateResult, error) {
	reqID := "agentupd:" + id + ":" + time.Now().Format("150405.000000")
	ch := s.Hub.Subscribe(reqID)
	env, _ := proto.Encode(proto.MsgAgentUpdate, reqID, nil)
	if err := s.Hub.Send(id, env); err != nil {
		s.Hub.Unsubscribe(reqID)
		return proto.AgentUpdateResult{Err: err.Error()}, nil
	}
	select {
	case msg, ok := <-ch:
		s.Hub.Unsubscribe(reqID)
		if !ok {
			return proto.AgentUpdateResult{}, fmt.Errorf("agent disconnected during update")
		}
		var res proto.AgentUpdateResult
		if len(msg.Data) > 0 {
			_ = json.Unmarshal(msg.Data, &res)
		}
		return res, nil
	case <-ctx.Done():
		s.Hub.Unsubscribe(reqID)
		return proto.AgentUpdateResult{}, ctx.Err()
	case <-time.After(3 * time.Minute):
		s.Hub.Unsubscribe(reqID)
		return proto.AgentUpdateResult{}, fmt.Errorf("update timed out (the agent may still be restarting)")
	}
}

// UpdateAgents POST /api/nodes/update-agents {node_ids:[...]} — batch upgrade of
// several nodes' agents concurrently. Returns per-node results.
func (s *Service) UpdateAgents(w http.ResponseWriter, r *http.Request) {
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
		NodeID  string `json:"node_id"`
		Name    string `json:"name,omitempty"`
		OK      bool   `json:"ok"`
		Err     string `json:"err,omitempty"`
		Version string `json:"version,omitempty"`
		Online  bool   `json:"online"`
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
				rr.Err = "node offline"
				out[i] = rr
				return
			}
			ures, err := s.updateOneAgent(ctx, id)
			if err != nil {
				rr.Err = err.Error()
				out[i] = rr
				return
			}
			rr.OK = ures.OK
			rr.Err = ures.Err
			rr.Version = ures.Version
			out[i] = rr
		}(i, id)
	}
	wg.Wait()
	s.Store.Audit(r.Context(), "admin", "node.update_agent_batch", fmt.Sprintf("%d nodes", len(body.NodeIDs)))
	httpx.OK(w, out)
}

// ProbeCandidates GET /api/nodes/probe/candidates — online nodes that are NOT
// already in Komari (matched by name), i.e. the "加入探针" picker list.
func (s *Service) ProbeCandidates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg := komari.LoadConfig(ctx, s.Store)
	if !cfg.Client().Valid() {
		httpx.OK(w, map[string]any{"configured": false})
		return
	}
	existing, err := cfg.Client().ListClients(ctx)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取 Komari 节点列表失败: "+err.Error())
		return
	}
	// Match by public IPv4 first (komari's existing node names often carry a
	// region suffix like "华为云新加坡" that won't equal NodePanel's "华为云"), name
	// as a fallback.
	haveName := make(map[string]bool, len(existing))
	haveIP := make(map[string]bool, len(existing))
	for _, e := range existing {
		if e.Name != "" {
			haveName[e.Name] = true
		}
		if e.Ipv4 != "" {
			haveIP[e.Ipv4] = true
		}
	}
	nodes, _ := s.Store.ListNodes(ctx)
	cands := []nodeView{}
	exist := []nodeView{}
	for i := range nodes {
		if !s.Hub.Online(nodes[i].ID) {
			continue
		}
		v := s.view(&nodes[i], false) // full node fields for the NodeSelect picker
		v.Online = true
		if haveName[nodes[i].Name] || (nodes[i].IPv4 != "" && haveIP[nodes[i].IPv4]) {
			exist = append(exist, v)
		} else {
			cands = append(cands, v)
		}
	}
	httpx.OK(w, map[string]any{"configured": true, "komari_url": cfg.BaseURL, "candidates": cands, "existing": exist})
}

// ProbeJoin POST /api/nodes/probe/join {node_ids:[]} — add each node to Komari
// (by name) and install the komari-agent on it. Returns per-node results.
func (s *Service) ProbeJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeIDs []string `json:"node_ids"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.NodeIDs) == 0 {
		httpx.Err(w, 400, "node_ids required")
		return
	}
	ctx := r.Context()
	cfg := komari.LoadConfig(ctx, s.Store)
	if !cfg.Client().Valid() {
		httpx.Err(w, 400, "未配置 Komari，请先到「设置 → 探针 Komari」配置")
		return
	}
	kc := cfg.Client()
	nodes, _ := s.Store.ListNodes(ctx)
	nameOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nameOf[n.ID] = n.Name
	}

	type res struct {
		NodeID string `json:"node_id"`
		Name   string `json:"name,omitempty"`
		OK     bool   `json:"ok"`
		Err    string `json:"err,omitempty"`
	}
	out := make([]res, len(body.NodeIDs))
	var wg sync.WaitGroup
	for i, id := range body.NodeIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			name := nameOf[id]
			rr := res{NodeID: id, Name: name}
			if !s.Hub.Online(id) {
				rr.Err = "节点离线"
				out[i] = rr
				return
			}
			if name == "" {
				rr.Err = "节点不存在"
				out[i] = rr
				return
			}
			uuid, token, err := kc.AddClient(ctx, name)
			if err != nil {
				rr.Err = "Komari 创建节点失败: " + err.Error()
				out[i] = rr
				return
			}
			cmd := fmt.Sprintf(`curl -fsSL %q | bash -s -- --token %q --endpoint %q`,
				cfg.InstallURL, token, cfg.BaseURL)
			if _, _, err := s.ExecSync(ctx, id, cmd, 180*time.Second); err != nil {
				_ = kc.RemoveClient(ctx, uuid) // rollback the just-created Komari client
				rr.Err = "节点安装 komari-agent 失败: " + err.Error()
				out[i] = rr
				return
			}
			rr.OK = true
			out[i] = rr
		}(i, id)
	}
	wg.Wait()
	s.Store.Audit(ctx, "admin", "node.probe_join", fmt.Sprintf("%d nodes", len(body.NodeIDs)))
	httpx.OK(w, out)
}

// ExecSync runs a shell command on one node via the agent's MsgExec RPC and
// returns the combined stdout+stderr synchronously (draining ExecOutput until
// Done). It reuses the agent's existing exec capability — no agent change. Used
// by the firewall endpoints and the tunnels panel (provisioning/monitoring).
func (s *Service) ExecSync(ctx context.Context, nodeID, cmd string, timeout time.Duration) (string, int, error) {
	reqID := "fw:" + nodeID + ":" + uuid.NewString()
	ch := s.Hub.Subscribe(reqID)
	defer s.Hub.Unsubscribe(reqID)
	env, err := proto.Encode(proto.MsgExec, reqID, proto.ExecRequest{Cmd: cmd, Timeout: int(timeout.Seconds())})
	if err != nil {
		return "", -1, err
	}
	if err := s.Hub.Send(nodeID, env); err != nil {
		return "", -1, fmt.Errorf("节点不可达: %w", err)
	}
	timer := time.NewTimer(timeout + 8*time.Second)
	defer timer.Stop()
	var sb strings.Builder
	exit := 0
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return sb.String(), exit, nil
			}
			if msg.Type != proto.MsgExecOutput {
				continue
			}
			var out proto.ExecOutput
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &out)
			}
			if out.Data != "" {
				sb.WriteString(out.Data)
			}
			if out.Done {
				return sb.String(), out.Exit, nil
			}
		case <-timer.C:
			return sb.String(), exit, fmt.Errorf("执行超时")
		case <-ctx.Done():
			return sb.String(), exit, ctx.Err()
		}
	}
}

// firewallDetectCmd reports a node's firewall in parseable sections:
//   =FW=     type=ufw|firewalld|none  + active=yes|no
//   =RULES=  ufw status (or firewall-cmd --list-ports)
//   =LISTEN= ss -ltnH (public listening sockets)
func firewallDetectCmd() string {
	return `echo =FW=; ` +
		`if command -v ufw >/dev/null 2>&1; then echo type=ufw; ` +
		`if ufw status 2>/dev/null | head -1 | grep -q 'Status: active'; then echo active=yes; else echo active=no; fi; ` +
		`elif command -v firewall-cmd >/dev/null 2>&1; then echo type=firewalld; ` +
		`if [ "$(firewall-cmd --state 2>/dev/null)" = running ]; then echo active=yes; else echo active=no; fi; ` +
		`else echo type=none; echo active=; fi; ` +
		`echo =RULES=; ` +
		`if command -v ufw >/dev/null 2>&1; then ufw status 2>/dev/null; ` +
		`elif command -v firewall-cmd >/dev/null 2>&1; then firewall-cmd --list-ports 2>/dev/null; fi; ` +
		`echo =LISTEN=; ss -ltnH 2>/dev/null`
}

// firewallToggleCmd returns the enable/disable command for the whole firewall.
func firewallToggleCmd(action string) string {
	switch action {
	case "enable":
		return `if command -v ufw >/dev/null 2>&1; then ufw --force enable 2>&1; ` +
			`elif command -v firewall-cmd >/dev/null 2>&1; then systemctl enable --now firewalld 2>&1; fi`
	case "disable":
		return `if command -v ufw >/dev/null 2>&1; then ufw disable 2>&1; ` +
			`elif command -v firewall-cmd >/dev/null 2>&1; then systemctl disable --now firewalld 2>&1; fi`
	}
	return ""
}

// firewallPortCmd builds the allow/delete command for a set of port specs
// (e.g. ["80/tcp","443/tcp"]). action = "allow" (开放) | "deny" (关闭).
func firewallPortCmd(action string, ports []string) string {
	if len(ports) == 0 {
		return ""
	}
	list := strings.Join(ports, " ")
	ufwAct, fdAct := "allow", "add"
	if action == "deny" {
		ufwAct, fdAct = "delete allow", "remove"
	}
	return fmt.Sprintf(`if command -v ufw >/dev/null 2>&1; then for p in %s; do ufw %s $p 2>&1; done; `+
		`elif command -v firewall-cmd >/dev/null 2>&1; then for p in %s; do firewall-cmd --permanent --%s-port=$p 2>&1; firewall-cmd --%s-port=$p 2>&1; done; fi`,
		list, ufwAct, list, fdAct, fdAct)
}

// portInfo is one public listening port and whether the firewall allows it.
type portInfo struct {
	Port  string `json:"port"`  // "80"
	Proto string `json:"proto"` // "tcp"
	Open  bool   `json:"open"`  // allowed by an explicit rule or app profile
}

// firewallInfo is one node's firewall state shown in the modal.
type firewallInfo struct {
	NodeID string     `json:"node_id"`
	Name   string     `json:"name"`
	Type   string     `json:"type"`   // ufw | firewalld | none | unknown
	Active bool       `json:"active"` // whole firewall enabled/running
	Ports  []portInfo `json:"ports"`
	Detail string     `json:"detail,omitempty"` // raw rules section
	Error  string     `json:"error,omitempty"`
}

// isPortSpec reports whether s is an explicit port spec like "80", "80/tcp", "8000:8100/tcp".
func isPortSpec(s string) bool {
	core := strings.TrimSuffix(strings.TrimSuffix(s, "/tcp"), "/udp")
	if core == "" {
		return false
	}
	for _, r := range strings.ReplaceAll(core, "-", "") {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// leadingPort returns the leading port number of a spec ("22/tcp" -> "22").
func leadingPort(s string) string {
	for i, r := range s {
		if r < '0' || r > '9' {
			if i == 0 {
				return ""
			}
			return s[:i]
		}
	}
	return s
}

func isLoopbackAddr(addr string) bool {
	addr = strings.Trim(addr, "[]")
	return strings.HasPrefix(addr, "127.") || addr == "::1" || strings.HasPrefix(addr, "localhost")
}

// parseFirewall reads the detect output into type/active + listening ports, the
// set of explicitly-allowed port numbers, allowed ufw app-profile names (to be
// expanded), and the raw rules (detail). Listening ports come from `ss`; the
// "open" state is resolved later against allowed + expanded profile ports.
func parseFirewall(out string) (typ string, active bool, listen []string, allowed map[string]bool, profiles []string, detail string) {
	allowed = map[string]bool{}
	section := ""
	var rules strings.Builder
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimRight(ln, "\r")
		switch {
		case strings.HasPrefix(ln, "=FW="):
			section = "fw"
		case strings.HasPrefix(ln, "=RULES="):
			section = "rules"
		case strings.HasPrefix(ln, "=LISTEN="):
			section = "listen"
		default:
			switch section {
			case "fw":
				if strings.HasPrefix(ln, "type=") {
					typ = strings.TrimPrefix(ln, "type=")
				} else if strings.HasPrefix(ln, "active=") {
					active = strings.TrimPrefix(ln, "active=") == "yes"
				}
			case "rules":
				rules.WriteString(ln + "\n")
				switch typ {
				case "ufw":
					// "22/tcp          ALLOW       Anywhere" or "Nginx Full   ALLOW ..."
					idx := strings.Index(ln, "ALLOW")
					if idx <= 0 {
						continue
					}
					to := strings.TrimSpace(ln[:idx])
					to = strings.TrimSuffix(to, " (v6)")
					if isPortSpec(to) {
						if p := leadingPort(to); p != "" {
							allowed[p] = true
						}
					} else if to != "" && to != "--" {
						profiles = append(profiles, to)
					}
				case "firewalld":
					for _, f := range strings.Fields(ln) {
						if isPortSpec(f) {
							if p := leadingPort(f); p != "" {
								allowed[p] = true
							}
						}
					}
				}
			case "listen":
				fld := strings.Fields(ln)
				if len(fld) < 4 {
					continue
				}
				local := fld[3] // ss -ltnH: State Recv Send LocalAddress:Port Peer
				i := strings.LastIndex(local, ":")
				if i < 0 {
					continue
				}
				addr, port := local[:i], local[i+1:]
				if port == "" || port == "*" || isLoopbackAddr(addr) {
					continue
				}
				if p := leadingPort(port); p != "" {
					listen = append(listen, p)
				}
			}
		}
	}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(listen))
	for _, p := range listen {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	sort.Strings(uniq)
	return typ, active, uniq, allowed, profiles, strings.TrimSpace(rules.String())
}

// expandProfile runs `ufw app info <name>` and returns the port numbers it opens.
// (Ports are listed on the line after "Ports:".)
func (s *Service) expandProfile(ctx context.Context, nodeID, name string) []string {
	out, _, err := s.ExecSync(ctx, nodeID, fmt.Sprintf("ufw app info %q 2>/dev/null", name), 10*time.Second)
	if err != nil {
		return nil
	}
	var ports []string
	afterPorts := false
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if afterPorts {
			for _, spec := range strings.FieldsFunc(ln, func(r rune) bool { return r == ',' || r == ' ' }) {
				if isPortSpec(spec) {
					if p := leadingPort(spec); p != "" {
						ports = append(ports, p)
					}
				}
			}
			break
		}
		if strings.HasPrefix(ln, "Ports:") {
			afterPorts = true
		}
	}
	return ports
}

func dedupStr(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range ss {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// firewallOne applies an optional action (whole-firewall enable/disable or per-
// port allow/deny) then re-reads status for one node.
func (s *Service) firewallOne(ctx context.Context, nodeID, action string, ports []string) firewallInfo {
	fi := firewallInfo{NodeID: nodeID, Name: shortNodeName(s, nodeID)}
	switch action {
	case "enable", "disable":
		if cmd := firewallToggleCmd(action); cmd != "" {
			if _, _, err := s.ExecSync(ctx, nodeID, cmd, 25*time.Second); err != nil {
				fi.Error = "切换失败: " + err.Error()
			}
		}
	case "allow", "deny":
		if cmd := firewallPortCmd(action, ports); cmd != "" {
			if _, _, err := s.ExecSync(ctx, nodeID, cmd, 25*time.Second); err != nil {
				fi.Error = "端口操作失败: " + err.Error()
			}
		}
	}
	out, _, err := s.ExecSync(ctx, nodeID, firewallDetectCmd(), 15*time.Second)
	if err != nil {
		if fi.Error == "" {
			fi.Error = "读取状态失败: " + err.Error()
		}
		return fi
	}
	typ, active, listen, allowed, profiles, detail := parseFirewall(out)
	fi.Type, fi.Active, fi.Detail = typ, active, detail
	if fi.Type == "" {
		fi.Type = "unknown"
	}
	if typ == "ufw" { // expand allowed app profiles (e.g. "Nginx Full") into ports
		for _, prof := range dedupStr(profiles) {
			for _, p := range s.expandProfile(ctx, nodeID, prof) {
				allowed[p] = true
			}
		}
	}
	for _, p := range listen {
		fi.Ports = append(fi.Ports, portInfo{Port: p, Proto: "tcp", Open: allowed[p]})
	}
	return fi
}

// shortNodeName resolves a node's display name (fallback to short id).
func shortNodeName(s *Service, nodeID string) string {
	if n, err := s.Store.GetNode(context.Background(), nodeID); err == nil {
		return n.Name
	}
	if len(nodeID) > 8 {
		return nodeID[:8]
	}
	return nodeID
}

// FirewallStatus POST /api/nodes/firewall/status {node_id}
func (s *Service) FirewallStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string `json:"node_id"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" {
		httpx.Err(w, http.StatusBadRequest, "node_id 不能为空")
		return
	}
	httpx.OK(w, s.firewallOne(r.Context(), body.NodeID, "", nil))
}

// FirewallToggle POST /api/nodes/firewall/toggle {node_id, action}
// action = "enable" | "disable" — turns the whole firewall on/off.
func (s *Service) FirewallToggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string `json:"node_id"`
		Action string `json:"action"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" {
		httpx.Err(w, http.StatusBadRequest, "node_id 不能为空")
		return
	}
	if body.Action != "enable" && body.Action != "disable" {
		httpx.Err(w, http.StatusBadRequest, "action 必须是 enable 或 disable")
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.firewall", body.NodeID+": "+body.Action)
	httpx.OK(w, s.firewallOne(r.Context(), body.NodeID, body.Action, nil))
}

// FirewallPorts POST /api/nodes/firewall/ports {node_id, ports, action}
// action = "allow" (开放) | "deny" (关闭). Opens/closes the given ports then
// re-reads status.
func (s *Service) FirewallPorts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string   `json:"node_id"`
		Ports  []string `json:"ports"` // ["80/tcp", "443"]
		Action string   `json:"action"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" {
		httpx.Err(w, http.StatusBadRequest, "node_id 不能为空")
		return
	}
	if body.Action != "allow" && body.Action != "deny" {
		httpx.Err(w, http.StatusBadRequest, "action 必须是 allow 或 deny")
		return
	}
	var ports []string
	for _, p := range body.Ports {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			p += "/tcp"
		}
		ports = append(ports, p)
	}
	if len(ports) == 0 {
		httpx.Err(w, http.StatusBadRequest, "ports 不能为空")
		return
	}
	s.Store.Audit(r.Context(), "admin", "node.firewall_ports", fmt.Sprintf("%s %s %s", body.NodeID, body.Action, strings.Join(ports, " ")))
	httpx.OK(w, s.firewallOne(r.Context(), body.NodeID, body.Action, ports))
}
