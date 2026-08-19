// Package agentapi implements the agent-facing endpoints: enrollment, the
// agent websocket, backup upload, the install script and binary downloads.
package agentapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/browserhub"
	"nodepanel/master/internal/config"
	"nodepanel/master/internal/credutil"
	"nodepanel/master/internal/geoip"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/installscript"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Service struct {
	Store   *store.Store
	Hub     *agenthub.Hub
	Geo     *geoip.Resolver
	Browser *browserhub.Hub
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

// InstallScript GET /install.sh?token=
func (s *Service) InstallScript(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		httpx.Err(w, 400, "missing token")
		return
	}
	if _, err := s.Store.GetNodeByEnrollment(r.Context(), token); err != nil {
		httpx.Err(w, 404, "invalid token")
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(installscript.Render(s.baseURL(), token)))
}

// ServeBinary GET /dl/{name}
func (s *Service) ServeBinary(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Path)
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.Cfg.AssetsDir, name)
	if _, err := os.Stat(full); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// EnrollRequest is the body posted by an agent on first connect.
type EnrollRequest struct {
	Token string          `json:"token"` // enrollment token
	Hello proto.HelloData `json:"hello"`
}

// Enroll POST /api/agent/enroll
func (s *Service) Enroll(w http.ResponseWriter, r *http.Request) {
	var req EnrollRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	node, err := s.Store.GetNodeByEnrollment(r.Context(), req.Token)
	if err != nil {
		httpx.Err(w, 404, "invalid enrollment token")
		return
	}
	agentToken := newToken()
	if err := s.Store.SetAgentToken(r.Context(), node.ID, agentToken); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.applyHello(r.Context(), node.ID, req.Hello, clientIP(r))
	httpx.JSON(w, 200, map[string]string{
		"agent_token": agentToken,
		"node_id":     node.ID,
	})
}

// AgentWS GET /agent/ws?token=<agent_token>
func (s *Service) AgentWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		httpx.Err(w, 401, "missing token")
		return
	}
	node, err := s.Store.GetNodeByAgentToken(r.Context(), token)
	if err != nil {
		httpx.Err(w, 401, "invalid agent token")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	go s.Hub.ServeAgent(conn, node.ID, clientIP(r))
}

// ServeStaged GET /api/agent/dl?id=<backupID>&token=<agent_token>
// Streams a staged backup archive to an agent for restore.
func (s *Service) ServeStaged(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, err := s.Store.GetNodeByAgentToken(r.Context(), token); err != nil {
		httpx.Err(w, 401, "invalid agent token")
		return
	}
	id := r.URL.Query().Get("id")
	b, err := s.Store.GetBackup(r.Context(), id)
	if err != nil || b.StagePath == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, b.StagePath)
}

// Upload POST /api/agent/upload?id=<backupID>&token=<agent_token>
//
// Supports two modes:
//   - single-shot (legacy): the whole archive in one request body. Used by
//     older agents and still the fallback when no chunk params are present.
//   - chunked: ?chunk=<i>&last=<0|1>. The agent streams the archive in
//     <100 MB pieces so it fits under Cloudflare's request-body cap. Chunk 0
//     truncates the staged file; every chunk appends; last=1 finalizes.
func (s *Service) Upload(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, err := s.Store.GetNodeByAgentToken(r.Context(), token); err != nil {
		httpx.Err(w, 401, "invalid agent token")
		return
	}
	id := r.URL.Query().Get("id")
	b, err := s.Store.GetBackup(r.Context(), id)
	if err != nil {
		httpx.Err(w, 404, "unknown backup job")
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.StagePath), 0o755); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}

	// Chunked? (?chunk=... present)
	if q := r.URL.Query(); q.Has("chunk") {
		idx := 0
		if v, err := strconv.Atoi(q.Get("chunk")); err == nil {
			idx = v
		}
		flag := os.O_CREATE | os.O_WRONLY
		if idx == 0 {
			flag |= os.O_TRUNC // fresh archive: discard any prior partial attempt
		} else {
			flag |= os.O_APPEND
		}
		f, err := os.OpenFile(b.StagePath, flag, 0o644)
		if err != nil {
			httpx.InternalErr(w, err.Error())
			return
		}
		if _, err := io.Copy(f, r.Body); err != nil {
			f.Close()
			httpx.InternalErr(w, err.Error())
			return
		}
		f.Close()
		httpx.OK(w, map[string]string{"ok": "1"})
		return
	}

	// Legacy single-shot upload.
	f, err := os.Create(b.StagePath)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, r.Body); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// applyHello updates node fields from a hello/metrics and resolves geo.
func (s *Service) applyHello(ctx context.Context, nodeID string, h proto.HelloData, remoteIP string) {
	// Agents behind NAT often only know a private interface address. Prefer a
	// public address for display + geo lookup: the agent's own IP if public,
	// otherwise the connection's real remote IP (via X-Forwarded-For /
	// CF-Connecting-IP), which is the host's public address through Cloudflare.
	ipv4 := h.IPv4
	if isPrivateIP(ipv4) && isPublicIP(remoteIP) {
		ipv4 = remoteIP
	}
	lookupIP := ipv4
	if lookupIP == "" {
		lookupIP = remoteIP
	}
	lk := s.Geo.LookupIP(ctx, lookupIP)
	n := &store.Node{
		ID: nodeID, Hostname: h.Hostname, OS: h.OS, Arch: h.Arch, Kernel: h.Kernel,
		IPv4: ipv4, IPv6: h.IPv6, AgentVersion: h.AgentVersion,
	}
	_ = s.Store.NodeSeen(ctx, n)
	if lk.CountryCode != "" {
		_ = s.Store.SetNodeGeo(ctx, nodeID, lk.CountryCode, lk.Country)
	}
}

func isPrivateIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsUnspecified()
}

func isPublicIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return !isPrivateIP(s) && !ip.IsLinkLocalMulticast()
}

// NewHubHandlers builds the agenthub lifecycle callbacks.
func (s *Service) NewHubHandlers() agenthub.Handlers {
	return agenthub.Handlers{
		OnConnect: func(nodeID string) {
			_ = s.Store.NodeHeartbeat(context.Background(), nodeID)
			s.Browser.Broadcast(browserhub.NewOut("node.status", map[string]string{"id": nodeID, "status": "online"}))
		},
		OnDisconnect: func(nodeID string) {
			_ = s.Store.NodeOffline(context.Background(), nodeID)
			s.Browser.Broadcast(browserhub.NewOut("node.status", map[string]string{"id": nodeID, "status": "offline"}))
		},
		OnHello: func(nodeID string, h proto.HelloData, remoteIP string) {
			s.applyHello(context.Background(), nodeID, h, remoteIP)
			if n, err := s.Store.GetNode(context.Background(), nodeID); err == nil {
				s.Browser.Broadcast(browserhub.NewOut("node.update", map[string]any{
					"id": n.ID, "name": n.Name, "status": "online", "hostname": n.Hostname,
					"os": n.OS, "arch": n.Arch, "ipv4": n.IPv4, "ipv6": n.IPv6,
					"country_code": n.CountryCode, "country": n.Country, "agent_version": n.AgentVersion,
					"online": true,
				}))
			}
		},
		OnMetrics: func(nodeID string, m proto.MetricsData) {
			_ = s.Store.NodeHeartbeat(context.Background(), nodeID)
			mm := store.Metric{
				NodeID: nodeID, Ts: time.Now().Unix(), CPU: m.CPU, MemUsed: m.MemUsed, MemTotal: m.MemTotal,
				DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal, Load1: m.Load1,
			}
			_ = s.Store.RecordMetric(context.Background(), mm)
			s.Browser.Broadcast(browserhub.NewOut("node.metrics", map[string]any{
				"id": nodeID, "cpu": m.CPU, "mem_used": m.MemUsed, "mem_total": m.MemTotal,
				"disk_used": m.DiskUsed, "disk_total": m.DiskTotal, "load1": m.Load1,
			}))
		},
		OnNewKeys: func(nodeID string, k proto.NewKeysData) {
			for _, key := range k.Keys {
				_ = s.Store.CreateCredential(context.Background(), &store.Credential{
					Name: key.Name, PubKey: key.PubKey, PrivKey: key.PrivKey,
					Fingerprint: credutil.Fingerprint(key.PubKey),
					Kind:        credutil.Kind(key.PubKey), Source: "command", NodeID: nodeID,
				})
			}
			if len(k.Keys) > 0 {
				s.Browser.Broadcast(browserhub.NewOut("credential.new", map[string]int{"count": len(k.Keys)}))
			}
		},
		OnContainers: func(nodeID string, d proto.ContainersData) {
			_ = s.Store.NodeHeartbeat(context.Background(), nodeID)
			list := make([]store.Container, 0, len(d.Containers))
			for _, c := range d.Containers {
				list = append(list, store.Container{
					NodeID: nodeID, ContainerID: c.ID, Name: c.Name, Image: c.Image,
					ImageID: c.ImageID, State: c.State, Status: c.Status, Created: c.Created,
					UpdateType: c.UpdateType,
				})
			}
			_ = s.Store.ReplaceNodeContainers(context.Background(), nodeID, list)
			s.Browser.Broadcast(browserhub.NewOut("container.inventory", map[string]any{"node_id": nodeID}))
		},
	}
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clientIP(r *http.Request) string {
	// Prefer Cloudflare's real-client header when the short HTTPS path still
	// goes through CF Tunnel (agent egress self-report). Fall back to XFF / peer.
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if ff := r.Header.Get("X-Forwarded-For"); ff != "" {
		return strings.TrimSpace(strings.Split(ff, ",")[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// ReportEgress POST /api/agent/report-egress?token=<agent_token>
// Body optional: {"ip":"1.2.3.4"}. If ip is omitted, the request's client IP
// is stored (works via CF with CF-Connecting-IP). Used so the host can keep
// UFW 8443 locked to real agent egress IPs even after ISP renumbers — the
// report itself can travel over panel.example.com (CF) which is never IP-filtered.
func (s *Service) ReportEgress(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if token == "" {
		httpx.Err(w, 401, "missing token")
		return
	}
	node, err := s.Store.GetNodeByAgentToken(r.Context(), token)
	if err != nil {
		httpx.Err(w, 401, "invalid agent token")
		return
	}
	ip := ""
	var body struct {
		IP string `json:"ip"`
	}
	_ = httpx.ReadJSON(r, &body)
	ip = strings.TrimSpace(body.IP)
	if ip == "" {
		ip = clientIP(r)
	}
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		httpx.Err(w, 400, "ipv4 required")
		return
	}
	if err := s.Store.UpsertAgentEgress(r.Context(), node.ID, ip); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.JSON(w, 200, map[string]any{
		"ok": true, "node_id": node.ID, "ip": ip,
	})
}
