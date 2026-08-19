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
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/credutil"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

const (
	meshKeyPath = "/root/.ssh/nodepanel_mesh"
	meshComment = "nodepanel-mesh"
	execTimeout = 40 // seconds per node-side command
	meshSSHPort = 22022
)

type Service struct {
	Store *store.Store
	Hub   *agenthub.Hub
}

// StartAutoProvision keeps the managed SSH mesh converged.  It deliberately
// manages only the dedicated mesh port (22022): existing administrator SSH
// listeners/rules, commonly on port 22, are left untouched.
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
// every peer public key, and replaces the dedicated-port allowlist with the
// current managed-node IPv4 set.  All mutations are idempotent.
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

	ips := meshIPv4s(nodes)
	if len(ips) == 0 {
		log.Printf("[mesh] skip firewall sync: no managed-node IPv4 addresses")
		return
	}
	for _, m := range members {
		if err := s.applyMeshFirewall(m.node.ID, ips); err != nil {
			log.Printf("[mesh] firewall sync %s: %v", m.node.Name, err)
		}
	}
}

func meshIPv4s(nodes []store.Node) []string {
	seen := map[string]bool{}
	for _, n := range nodes {
		ip := net.ParseIP(strings.TrimSpace(n.IPv4))
		if ip == nil || ip.To4() == nil {
			continue
		}
		seen[ip.To4().String()] = true
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
allow=/etc/nodepanel/mesh-allowlist.v4
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

{
  echo 'table inet nodepanel_mesh {'
  printf '  set allowed_v4 { type ipv4_addr; flags interval; elements = { '
  paste -sd, "$allow"
  echo ' } }'
  echo '  chain input {'
  echo '    type filter hook input priority -10; policy accept;'
  echo '    tcp dport 22022 ip saddr @allowed_v4 accept'
  echo '    tcp dport 22022 drop'
  echo '    tcp dport 22022 ip6 saddr ::/0 drop'
  echo '  }'
  echo '}'
} >"$tmp"

nft delete table inet nodepanel_mesh 2>/dev/null || true
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

// applyMeshFirewall installs a persistent nftables rule on a managed node.
// Only TCP/22022 is constrained; IPv6 is closed on that dedicated port until
// IPv6 node addresses are deliberately supported.
func (s *Service) applyMeshFirewall(nodeID string, ips []string) error {
	allow := strings.Join(ips, "\n") + "\n"
	scriptB64 := base64.StdEncoding.EncodeToString([]byte(meshFirewallApplyScript))
	unitB64 := base64.StdEncoding.EncodeToString([]byte(meshFirewallUnit))
	allowB64 := base64.StdEncoding.EncodeToString([]byte(allow))
	cmd := fmt.Sprintf(`set -eu
install -d -m 700 /etc/nodepanel /usr/local/lib/nodepanel
printf '%%s' %q | base64 -d > /usr/local/lib/nodepanel/mesh-firewall-apply
chmod 700 /usr/local/lib/nodepanel/mesh-firewall-apply
printf '%%s' %q | base64 -d > /etc/nodepanel/mesh-allowlist.v4
chmod 600 /etc/nodepanel/mesh-allowlist.v4
printf '%%s' %q | base64 -d > /etc/systemd/system/nodepanel-mesh-firewall.service
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
  ufw allow %d/tcp comment 'NodePanel Mesh (nft allowlist)'
fi
systemctl daemon-reload
systemctl enable --now nodepanel-mesh-firewall.service
`, scriptB64, allowB64, unitB64, meshSSHPort, meshSSHPort, meshSSHPort)
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

// ---- HTTP handlers ----

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
