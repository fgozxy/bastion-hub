// Package mesh provisions key-based (Ed25519) bidirectional trust between
// NodePanel-managed nodes — the "nodes can SSH each other via the panel"
// capability. It runs entirely on the master using the agent's existing
// MsgExec / MsgScanSSH primitives, so no agent rebuild is required.
//
// Model:
//   - every mesh member keeps an Ed25519 keypair at /root/.ssh/nodepanel_mesh
//     (created idempotently, same shape as the hardening-preset root key)
//   - the master pushes each member's public key into every other member's
//     /root/.ssh/authorized_keys (idempotent append of a validated key line)
//
// Result: from any member you can `ssh root@<other>` using its own mesh key,
// because every other member has authorized that public key.
package mesh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/credutil"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

const (
	meshKeyPath       = "/root/.ssh/nodepanel_mesh"
	meshComment       = "nodepanel-mesh"
	execTimeout       = 40 // seconds per node-side command
	meshSSHPort       = 22022
	meshAccessSetting = "mesh_ssh_access"
)

// AccessConfig persists the custom source allowlist for the dedicated mesh SSH
// port. Nodes not listed here keep the automatic managed-node allowlist.
type AccessConfig struct {
	Enabled     bool     `json:"enabled"`
	NodeIDs     []string `json:"node_ids"`
	SourceCIDRs []string `json:"source_cidrs"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
}

type Service struct {
	Store *store.Store
	Hub   *agenthub.Hub
}

// StartAutoProvision keeps the managed SSH mesh converged.  It deliberately
// always manages the dedicated mesh port (22022). When a node is selected for
// a custom source allowlist, its configured administrator SSH port is covered
// by the same rule as well; sshd listener configuration itself is untouched.
//
// Agents connect outbound to the panel, so a newly joined node can receive the
// key and firewall setup even before its mesh SSH port is reachable.
func (s *Service) StartAutoProvision(ctx context.Context) {
	go func() {
		s.syncAutoMesh(ctx)
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.syncAutoMesh(ctx)
			}
		}
	}()
}

// syncAutoMesh gives every currently online managed node a mesh key, installs
// every peer public key, and converges the dedicated-port allowlist. Nodes in
// the persisted custom selection use its source CIDRs; other nodes use all
// known managed-node addresses. All mutations are idempotent.
func (s *Service) syncAutoMesh(ctx context.Context) {
	nodes, err := s.selectedNodes(ctx, nil)
	if err != nil {
		log.Printf("[mesh] list online nodes: %v", err)
		return
	}
	if len(nodes) == 0 {
		return
	}

	type member struct {
		node store.Node
		pub  string
	}
	members := make([]member, 0, len(nodes))
	for _, n := range nodes {
		pub, err := s.ensureMeshKey(n.ID)
		if err != nil {
			log.Printf("[mesh] key setup %s: %v", n.Name, err)
			continue
		}
		members = append(members, member{node: n, pub: pub})
	}
	if len(members) == 0 {
		return
	}

	for _, m := range members {
		for _, peer := range members {
			if peer.node.ID == m.node.ID {
				continue
			}
			if err := s.installPubKey(m.node.ID, peer.pub); err != nil {
				log.Printf("[mesh] authorize %s on %s: %v", peer.node.Name, m.node.Name, err)
			}
		}
	}

	allNodes, err := s.Store.ListNodes(ctx)
	if err != nil {
		log.Printf("[mesh] list nodes for firewall sync: %v", err)
		return
	}
	defaultSources := meshAddresses(allNodes)
	if len(defaultSources) == 0 {
		log.Printf("[mesh] skip firewall sync: no managed-node IP addresses")
		return
	}
	access, err := s.loadAccessConfig(ctx)
	if err != nil {
		log.Printf("[mesh] load custom access config: %v; using automatic allowlist", err)
		access = AccessConfig{}
	}
	for _, m := range members {
		sources := access.sourcesForNode(m.node.ID, defaultSources)
		ports := []int{meshSSHPort}
		if access.appliesToNode(m.node.ID) {
			if port, err := nodeSSHPort(m.node); err != nil {
				log.Printf("[mesh] invalid SSH port on %s: %v; protecting only %d", m.node.Name, err, meshSSHPort)
			} else {
				ports = append(ports, port)
			}
		}
		if err := s.applyMeshFirewall(m.node.ID, sources, ports); err != nil {
			log.Printf("[mesh] firewall sync %s: %v", m.node.Name, err)
		}
	}
}

func meshAddresses(nodes []store.Node) []string {
	seen := map[string]bool{}
	for _, n := range nodes {
		for _, raw := range []string{n.IPv4, n.IPv6} {
			ip := net.ParseIP(strings.TrimSpace(raw))
			if ip == nil {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				seen[v4.String()+"/32"] = true
			} else {
				seen[ip.String()+"/128"] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ip := range seen {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

const meshFirewallApplyScript = `#!/bin/sh
set -eu
allow4=/etc/nodepanel/mesh-allowlist.v4
allow6=/etc/nodepanel/mesh-allowlist.v6
ports=/etc/nodepanel/mesh-ports
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

# Ensure the delete statement below is always valid. The following nft batch is
# transactional, so existing access remains intact if the replacement is bad.
nft add table inet nodepanel_mesh 2>/dev/null || true
{
  echo 'delete table inet nodepanel_mesh'
  echo 'table inet nodepanel_mesh {'
  printf '  set allowed_v4 { type ipv4_addr; flags interval;'
  if [ -s "$allow4" ]; then printf ' elements = { '; paste -sd, "$allow4"; printf ' }'; fi
  echo ' }'
  printf '  set allowed_v6 { type ipv6_addr; flags interval;'
  if [ -s "$allow6" ]; then printf ' elements = { '; paste -sd, "$allow6"; printf ' }'; fi
  echo ' }'
  printf '  set restricted_ports { type inet_service; elements = { '
  paste -sd, "$ports"
  echo ' } }'
  echo '  chain input {'
  echo '    type filter hook input priority -10; policy accept;'
  echo '    tcp dport @restricted_ports ip saddr @allowed_v4 accept'
  echo '    tcp dport @restricted_ports ip6 saddr @allowed_v6 accept'
  echo '    tcp dport @restricted_ports drop'
  echo '  }'
  echo '}'
} >"$tmp"

nft -f "$tmp"
`

const meshFirewallUnit = `[Unit]
Description=NodePanel Mesh SSH allowlist
After=network-online.target nftables.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/lib/nodepanel/mesh-firewall-apply

[Install]
WantedBy=multi-user.target
`

// applyMeshFirewall installs or hot-replaces a persistent nftables rule on a
// managed node. The caller chooses the SSH ports to constrain; the dedicated
// mesh port is always included and custom targets also include their actual
// configured administrator SSH port.
func (s *Service) applyMeshFirewall(nodeID string, sources []string, ports []int) error {
	var v4, v6 []string
	for _, source := range sources {
		ip, _, err := net.ParseCIDR(source)
		if err != nil {
			return fmt.Errorf("invalid normalized source %q", source)
		}
		if ip.To4() != nil {
			v4 = append(v4, source)
		} else {
			v6 = append(v6, source)
		}
	}
	if len(v4)+len(v6) == 0 {
		return fmt.Errorf("refusing to install an empty SSH source allowlist")
	}
	portSeen := map[int]bool{}
	var normalizedPorts []int
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid SSH port %d", port)
		}
		if !portSeen[port] {
			portSeen[port] = true
			normalizedPorts = append(normalizedPorts, port)
		}
	}
	if len(normalizedPorts) == 0 {
		return fmt.Errorf("refusing to install an empty SSH port list")
	}
	sort.Ints(normalizedPorts)
	portLines := make([]string, 0, len(normalizedPorts))
	for _, port := range normalizedPorts {
		portLines = append(portLines, strconv.Itoa(port))
	}
	scriptB64 := base64.StdEncoding.EncodeToString([]byte(meshFirewallApplyScript))
	unitB64 := base64.StdEncoding.EncodeToString([]byte(meshFirewallUnit))
	allow4B64 := base64.StdEncoding.EncodeToString([]byte(strings.Join(v4, "\n")))
	allow6B64 := base64.StdEncoding.EncodeToString([]byte(strings.Join(v6, "\n")))
	portsB64 := base64.StdEncoding.EncodeToString([]byte(strings.Join(portLines, "\n")))
	cmd := fmt.Sprintf(`set -eu
install -d -m 700 /etc/nodepanel /usr/local/lib/nodepanel
printf '%%s' %q | base64 -d > /usr/local/lib/nodepanel/mesh-firewall-apply
chmod 700 /usr/local/lib/nodepanel/mesh-firewall-apply
printf '%%s' %q | base64 -d > /etc/nodepanel/mesh-allowlist.v4
chmod 600 /etc/nodepanel/mesh-allowlist.v4
printf '%%s' %q | base64 -d > /etc/nodepanel/mesh-allowlist.v6
chmod 600 /etc/nodepanel/mesh-allowlist.v6
printf '%%s' %q | base64 -d > /etc/nodepanel/mesh-ports
chmod 600 /etc/nodepanel/mesh-ports
printf '%%s' %q | base64 -d > /etc/systemd/system/nodepanel-mesh-firewall.service
command -v nft >/dev/null 2>&1 || { echo 'nftables is required for mesh SSH restrictions' >&2; exit 1; }
systemctl daemon-reload
systemctl enable nodepanel-mesh-firewall.service
# Restrict the dedicated port before making sshd/UFW accept traffic on it.
systemctl restart nodepanel-mesh-firewall.service
mesh_ssh_ports=$(ss -lntp | awk '/sshd/ { p=$4; sub(/^.*:/, "", p); if (p ~ /^[0-9]+$/) print p }' | sort -nu)
if ! printf '%%s\n' "$mesh_ssh_ports" | grep -qx '%d'; then
  [ -n "$mesh_ssh_ports" ] || { echo 'could not determine existing sshd listener ports' >&2; exit 1; }
  install -d -m 755 /etc/ssh/sshd_config.d
  { for p in $mesh_ssh_ports; do printf 'Port %%s\n' "$p"; done; printf 'Port %d\n'; } > /etc/ssh/sshd_config.d/99-nodepanel-mesh.conf
  sshd -t
  systemctl reload ssh 2>/dev/null || systemctl reload sshd
fi
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qx 'Status: active'; then
  # UFW's chain otherwise rejects this new port before the dedicated nft
  # allowlist can make the source-IP decision below.
  for p in %s; do ufw allow "$p/tcp" comment 'NodePanel SSH (nft allowlist)'; done
fi
`, scriptB64, allow4B64, allow6B64, portsB64, unitB64, meshSSHPort, meshSSHPort, strings.Join(portLines, " "))
	_, exit, err := s.execNode(nodeID, cmd, execTimeout)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("firewall setup failed (exit %d)", exit)
	}
	return nil
}

// ---- low-level helpers (drive one agent over the hub) ----

// execNode runs a shell command on one node and returns its stdout (lines) and
// exit code, mirroring how commands/backup stream one-shot RPCs.
func (s *Service) execNode(nodeID, cmd string, timeout int) (string, int, error) {
	reqID := fmt.Sprintf("mesh:%s:%d", nodeID, time.Now().UnixNano())
	ch := s.Hub.Subscribe(reqID)
	defer s.Hub.Unsubscribe(reqID)

	if timeout <= 0 {
		timeout = execTimeout
	}
	env, err := proto.Encode(proto.MsgExec, reqID, proto.ExecRequest{Cmd: cmd, Timeout: timeout})
	if err != nil {
		return "", -1, err
	}
	if err := s.Hub.Send(nodeID, env); err != nil {
		return "", -1, err
	}

	var out strings.Builder
	exit := -1
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		rem := time.Until(deadline)
		if rem <= 0 {
			return out.String(), exit, fmt.Errorf("exec timeout")
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				return out.String(), exit, fmt.Errorf("agent disconnected")
			}
			var o proto.ExecOutput
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &o)
			}
			if o.Stream == "stdout" {
				out.WriteString(o.Data)
			}
			if o.Done {
				return out.String(), o.Exit, nil
			}
		case <-time.After(rem):
			return out.String(), exit, fmt.Errorf("exec timeout")
		}
	}
}

// validatePubKey ensures s is exactly one parseable SSH authorized-key line.
func validatePubKey(s string) (string, error) {
	line := strings.TrimSpace(s)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", fmt.Errorf("invalid public key")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil {
		return "", fmt.Errorf("invalid public key: %v", err)
	}
	return line, nil
}

// ensureMeshKey makes sure the node has an Ed25519 keypair and returns its pubkey.
func (s *Service) ensureMeshKey(nodeID string) (string, error) {
	cmd := fmt.Sprintf(
		"umask 077; mkdir -p /root/.ssh; f=%s; "+
			"[ -s \"$f\" ] || ssh-keygen -t ed25519 -N '' -f \"$f\" -C %s >/dev/null 2>&1; "+
			"cat \"$f.pub\"",
		meshKeyPath, meshComment)
	out, exit, err := s.execNode(nodeID, cmd, execTimeout)
	if err != nil {
		return "", err
	}
	if exit != 0 {
		return "", fmt.Errorf("ensure mesh key failed (exit %d)", exit)
	}
	pub, err := validatePubKey(out)
	if err != nil {
		return "", fmt.Errorf("node %s returned no/odd mesh pubkey: %v", nodeID, err)
	}
	return pub, nil
}

// installPubKey appends pub into the node's root authorized_keys (idempotent).
// The key is shipped base64 so no shell metacharacter can ever leak through.
func (s *Service) installPubKey(nodeID, pub string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(pub))
	cmd := fmt.Sprintf(
		"umask 077; mkdir -p /root/.ssh; f=/root/.ssh/authorized_keys; "+
			"touch \"$f\"; chmod 600 \"$f\"; "+
			"line=\"$(printf '%%s' '%s' | base64 -d)\"; "+
			"grep -qF -- \"$line\" \"$f\" || printf '%%s\\n' \"$line\" >> \"$f\"; "+
			"exit 0",
		b64)
	_, exit, err := s.execNode(nodeID, cmd, execTimeout)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("install key failed (exit %d)", exit)
	}
	return nil
}

// scanAuthorized fingerprints every key currently authorized on a node.
func (s *Service) scanAuthorized(nodeID string) (map[string]string, error) {
	reqID := fmt.Sprintf("meshscan:%s:%d", nodeID, time.Now().UnixNano())
	ch := s.Hub.Subscribe(reqID)
	defer s.Hub.Unsubscribe(reqID)
	env, err := proto.Encode(proto.MsgScanSSH, reqID, proto.ScanSSHRequest{})
	if err != nil {
		return nil, err
	}
	if err := s.Hub.Send(nodeID, env); err != nil {
		return nil, err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("agent disconnected")
		}
		var data proto.SSHKeysData
		if len(msg.Data) > 0 {
			_ = json.Unmarshal(msg.Data, &data)
		}
		fps := map[string]string{} // fp -> pubkey
		for _, k := range data.Keys {
			if k.PubKey == "" {
				continue
			}
			fp := credutil.Fingerprint(k.PubKey)
			fps[fp] = k.PubKey
		}
		return fps, nil
	case <-time.After(40 * time.Second):
		return nil, fmt.Errorf("scan timeout")
	}
}

// ---- node selection ----

func (s *Service) selectedNodes(ctx context.Context, ids []string) ([]store.Node, error) {
	all, err := s.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []store.Node
	for _, n := range all {
		if len(ids) == 0 { // no selection = all online nodes
			if n.Status == "online" {
				out = append(out, n)
			}
			continue
		}
		for _, id := range ids {
			if id == n.ID && !seen[n.ID] {
				seen[n.ID] = true
				out = append(out, n)
			}
		}
	}
	return out, nil
}

func normalizeSourceCIDRs(values []string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range values {
		parts := strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
		})
		for _, raw := range parts {
			var normalized string
			if strings.Contains(raw, "/") {
				_, network, err := net.ParseCIDR(raw)
				if err != nil {
					return nil, fmt.Errorf("无效的 IP/CIDR：%s", raw)
				}
				normalized = network.String()
			} else {
				ip := net.ParseIP(raw)
				if ip == nil {
					return nil, fmt.Errorf("无效的 IP/CIDR：%s", raw)
				}
				if v4 := ip.To4(); v4 != nil {
					normalized = v4.String() + "/32"
				} else {
					normalized = ip.String() + "/128"
				}
			}
			seen[normalized] = true
			if len(seen) > 128 {
				return nil, fmt.Errorf("来源白名单最多支持 128 项")
			}
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeNodeIDs(ids []string, known map[string]bool) ([]string, error) {
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if !known[id] {
			return nil, fmt.Errorf("节点不存在：%s", id)
		}
		seen[id] = true
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (c AccessConfig) sourcesForNode(nodeID string, defaults []string) []string {
	if c.appliesToNode(nodeID) {
		return c.SourceCIDRs
	}
	return defaults
}

func (c AccessConfig) appliesToNode(nodeID string) bool {
	if !c.Enabled {
		return false
	}
	for _, id := range c.NodeIDs {
		if id == nodeID {
			return true
		}
	}
	return false
}

func nodeSSHPort(node store.Node) (int, error) {
	raw := strings.TrimSpace(node.SshPort)
	if raw == "" {
		return 22, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("节点 %s 的 SSH 端口无效：%q", node.Name, raw)
	}
	return port, nil
}

func uniquePorts(ports []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(ports))
	for _, port := range ports {
		if !seen[port] {
			seen[port] = true
			out = append(out, port)
		}
	}
	sort.Ints(out)
	return out
}

func (s *Service) loadAccessConfig(ctx context.Context) (AccessConfig, error) {
	raw, err := s.Store.GetSetting(ctx, meshAccessSetting)
	if err != nil || raw == "" {
		return AccessConfig{}, err
	}
	var cfg AccessConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return AccessConfig{}, err
	}
	if cfg.Enabled && (len(cfg.NodeIDs) == 0 || len(cfg.SourceCIDRs) == 0) {
		return AccessConfig{}, fmt.Errorf("stored SSH access config is incomplete")
	}
	return cfg, nil
}

// ---- HTTP handlers ----

// Access GET /api/mesh/access returns the persisted custom allowlist and the
// automatic fallback derived from all managed-node addresses.
func (s *Service) Access(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadAccessConfig(r.Context())
	if err != nil {
		httpx.InternalErr(w, "读取 SSH 跳板限制失败："+err.Error())
		return
	}
	nodes, err := s.Store.ListNodes(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"config":          cfg,
		"mesh_port":       meshSSHPort,
		"default_sources": meshAddresses(nodes),
	})
}

// PutAccess PUT /api/mesh/access hot-replaces TCP/22022 source restrictions on
// selected online nodes and persists the desired state for offline/reconnecting
// nodes. Removing a node from the custom selection restores the automatic list.
func (s *Service) PutAccess(w http.ResponseWriter, r *http.Request) {
	var body AccessConfig
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	all, err := s.Store.ListNodes(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	known := make(map[string]bool, len(all))
	byID := make(map[string]store.Node, len(all))
	for _, node := range all {
		known[node.ID] = true
		byID[node.ID] = node
	}
	body.NodeIDs, err = normalizeNodeIDs(body.NodeIDs, known)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	body.SourceCIDRs, err = normalizeSourceCIDRs(body.SourceCIDRs)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Enabled && len(body.NodeIDs) == 0 {
		httpx.Err(w, http.StatusBadRequest, "请至少选择一个目标节点")
		return
	}
	if body.Enabled && len(body.SourceCIDRs) == 0 {
		httpx.Err(w, http.StatusBadRequest, "启用限制时至少需要一个允许来源 IP/CIDR")
		return
	}
	if body.Enabled {
		for _, id := range body.NodeIDs {
			if _, err := nodeSSHPort(byID[id]); err != nil {
				httpx.Err(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}

	previous, err := s.loadAccessConfig(r.Context())
	if err != nil {
		httpx.InternalErr(w, "读取原 SSH 跳板限制失败："+err.Error())
		return
	}
	body.UpdatedAt = time.Now().Unix()
	raw, err := json.Marshal(body)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	if err := s.Store.SetSetting(r.Context(), meshAccessSetting, string(raw)); err != nil {
		httpx.InternalErr(w, "保存 SSH 跳板限制失败："+err.Error())
		return
	}

	// Apply both the old and new selections. This immediately restores the
	// automatic list on nodes removed from the custom selection.
	affected := map[string]bool{}
	for _, id := range previous.NodeIDs {
		affected[id] = true
	}
	for _, id := range body.NodeIDs {
		affected[id] = true
	}
	defaults := meshAddresses(all)
	type accessResult struct {
		ID      string `json:"node_id"`
		Name    string `json:"name"`
		Ports   []int  `json:"ports,omitempty"`
		Online  bool   `json:"online"`
		OK      bool   `json:"ok"`
		Pending bool   `json:"pending,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	results := make([]accessResult, 0, len(affected))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id := range affected {
		node, ok := byID[id]
		if !ok {
			continue
		}
		if !s.Hub.Online(id) {
			mu.Lock()
			results = append(results, accessResult{ID: id, Name: node.Name, Pending: true, Error: "节点离线，配置已保存，重连后自动应用"})
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(node store.Node) {
			defer wg.Done()
			res := accessResult{ID: node.ID, Name: node.Name, Online: true}
			sources := body.sourcesForNode(node.ID, defaults)
			ports := []int{meshSSHPort}
			if body.appliesToNode(node.ID) {
				port, err := nodeSSHPort(node)
				if err != nil {
					res.Error = err.Error()
					mu.Lock()
					results = append(results, res)
					mu.Unlock()
					return
				}
				ports = append(ports, port)
			}
			res.Ports = uniquePorts(ports)
			if len(sources) == 0 {
				res.Error = "没有可用的自动来源地址"
			} else if err := s.applyMeshFirewall(node.ID, sources, res.Ports); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(node)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	s.Store.Audit(r.Context(), authUser(r), "mesh.access.update",
		fmt.Sprintf("enabled=%v nodes=%d sources=%d", body.Enabled, len(body.NodeIDs), len(body.SourceCIDRs)))
	httpx.OK(w, map[string]any{"config": body, "mesh_port": meshSSHPort, "results": results})
}

// Provision POST /api/mesh/provision {node_ids?:[...], full_mesh?:bool}
// Ensures each selected node has a mesh keypair, then (full_mesh, default) pushes
// every member's pubkey into every other member's authorized_keys — bidirectional
// key trust between all members. Returns per-node results.
func (s *Service) Provision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeIDs  []string `json:"node_ids"`
		FullMesh bool     `json:"full_mesh"`
	}
	_ = httpx.ReadJSON(r, &body)

	nodes, err := s.selectedNodes(r.Context(), body.NodeIDs)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	if len(nodes) < 2 {
		httpx.Err(w, 400, "至少需要 2 个在线节点组成网格")
		return
	}

	// 1) ensure + read each member's mesh pubkey
	type member struct {
		node store.Node
		pub  string
		err  error
	}
	members := make([]member, len(nodes))
	for i, n := range nodes {
		pub, err := s.ensureMeshKey(n.ID)
		members[i] = member{node: n, pub: pub, err: err}
	}

	// 2) full mesh: push every member's pubkey into every other member
	type nodeRes struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Err  string `json:"error,omitempty"`
		Keys []struct {
			Peer string `json:"peer"`
			Fp   string `json:"fingerprint"`
		} `json:"installed,omitempty"`
	}
	results := make([]nodeRes, 0, len(members))
	installed := map[string][]string{} // nodeID -> peer pubkeys installed

	for _, m := range members {
		res := nodeRes{ID: m.node.ID, Name: m.node.Name}
		if m.err != nil {
			res.Err = m.err.Error()
			results = append(results, res)
			continue
		}
		res.OK = true
		// this member needs every OTHER member's pubkey
		var list []string
		for _, o := range members {
			if o.node.ID == m.node.ID || o.err != nil || o.pub == "" {
				continue
			}
			if body.FullMesh {
				if err := s.installPubKey(m.node.ID, o.pub); err != nil {
					res.Err = err.Error()
					res.OK = false
					break
				}
				list = append(list, o.pub)
			}
		}
		if res.OK {
			installed[m.node.ID] = list
			for _, pub := range list {
				fp := credutil.Fingerprint(pub)
				res.Keys = append(res.Keys, struct {
					Peer string `json:"peer"`
					Fp   string `json:"fingerprint"`
				}{Peer: fp, Fp: fp})
			}
		}
		results = append(results, res)
	}

	s.Store.Audit(r.Context(), authUser(r), "mesh.provision",
		fmt.Sprintf("nodes=%d full=%v", len(nodes), body.FullMesh))
	httpx.OK(w, map[string]any{"full_mesh": body.FullMesh, "results": results})
}

// Status GET /api/mesh/status?node_ids=id1,id2... or all online
// For each node: its own mesh key fingerprint + which mesh members it currently
// authorizes in authorized_keys (by fingerprint).
func (s *Service) Status(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if q := r.URL.Query().Get("node_ids"); q != "" {
		ids = strings.Split(q, ",")
	}
	nodes, err := s.selectedNodes(r.Context(), ids)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}

	type nodeStatus struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		OwnFP       string   `json:"own_fingerprint,omitempty"`
		OwnKeyOK    bool     `json:"own_key"`
		Authorized  []string `json:"authorized_fingerprints"`
		MeshPresent []string `json:"mesh_authorized"`
		Error       string   `json:"error,omitempty"`
	}
	out := make([]nodeStatus, 0)

	// own mesh pubkeys first (fingerprint map)
	own := map[string]string{} // nodeID -> fp
	for _, n := range nodes {
		pub, err := s.ensureMeshKey(n.ID)
		if err != nil {
			out = append(out, nodeStatus{ID: n.ID, Name: n.Name, Error: err.Error()})
			continue
		}
		own[n.ID] = credutil.Fingerprint(pub)
	}
	allFP := map[string]bool{}
	for _, fp := range own {
		allFP[fp] = true
	}

	for _, n := range nodes {
		ns := nodeStatus{ID: n.ID, Name: n.Name}
		if fp, ok := own[n.ID]; ok {
			ns.OwnFP, ns.OwnKeyOK = fp, true
		}
		if fp, ok := own[n.ID]; ok {
			authorized, err := s.scanAuthorized(n.ID)
			if err != nil {
				ns.Error = err.Error()
				out = append(out, ns)
				continue
			}
			for aFP := range authorized {
				ns.Authorized = append(ns.Authorized, aFP)
				if aFP != fp && allFP[aFP] {
					ns.MeshPresent = append(ns.MeshPresent, aFP)
				}
			}
		}
		out = append(out, ns)
	}
	httpx.OK(w, out)
}

func authUser(r *http.Request) string {
	// audit author; best-effort (user id may be empty in some paths)
	return ""
}
