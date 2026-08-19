package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"nodepanel/shared/proto"
)

// AgentVersion is reported to the panel.
const AgentVersion = "2.4.7" // 2.4.7: paginated registry tags + safe floating/variant updates. 2.4.6: auto-prune dangling images after update/rebuild/upgrade. 2.4.5: semver auto-update. 2.4.4: IPv4 prefer + registry retry.

// Keepalive: the agent pings the panel every agentPingPeriod and expects a pong
// within agentPongWait. The reverse WSS path goes through Cloudflare, which can
// silently drop an idle/half-open connection without delivering a close frame or
// RST to either side. Without a read deadline the agent's ReadMessage blocks
// forever on such a dead socket and it never reconnects (the panel shows it
// offline even though the process is alive). The pong handler refreshes the read
// deadline on every pong; if pongs stop arriving the deadline fires, ReadMessage
// returns a timeout, and the outer reconnect loop (Run) dials again. Mirrors the
// panel-side ping/pong in agenthub/hub.go (writeWait/pongWait/pingPeriod).
//
// Periods are intentionally a bit tighter than Cloudflare's ~100s idle window so
// a silently dropped edge is noticed before the next scheduled job fires into a
// half-open socket.
const (
	agentPongWait   = 45 * time.Second
	agentPingPeriod = 20 * time.Second
	agentWriteWait  = 10 * time.Second
	agentDialWait   = 12 * time.Second
)

// Agent is the running agent state.
type Agent struct {
	cfg     *FileConfig
	state   *State
	dialer  *websocket.Dialer
	conn    *websocket.Conn
	writeMu sync.Mutex
	metrics *metricsSampler
}

// Run loads config and runs the agent until interrupted.
func Run(configPath string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if cfg.Server == "" || cfg.Token == "" {
		return fmt.Errorf("config missing server or token")
	}
	state, _ := LoadState()
	a := &Agent{
		cfg:     cfg,
		state:   state,
		metrics: &metricsSampler{},
	}
	a.dialer = a.buildDialer(cfg.Server)

	log.Printf("nodepanel-agent starting (server=%s)", cfg.Server)
	failures := 0
	for {
		if err := a.connectAndServe(); err != nil {
			failures++
			log.Printf("agent: disconnected: %v", err)
		} else {
			failures = 0
		}
		sleep(backoff(failures))
	}
}

func (a *Agent) buildDialer(server string) *websocket.Dialer {
	d := &websocket.Dialer{
		HandshakeTimeout: agentDialWait,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		// Prefer IPv4: several nodes resolve panel.example.com to CF AAAA first and
		// then hang on broken/slow IPv6 paths (read tcp [...]:443 i/o timeout).
		// Fall back to IPv6 only when no A record works.
		NetDialContext: dialContextPreferIPv4,
	}
	if strings.HasPrefix(server, "https://") || strings.HasPrefix(server, "wss://") {
		d.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return d
}

// panelHTTPClient is used for enroll and other one-shot HTTPS calls to the
// panel. Same IPv4 preference as the WSS dialer.
var panelHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dialContextPreferIPv4,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	},
}

// dialContextPreferIPv4 resolves host and dials A records before AAAA. Hosts
// that are already literal IPs (including IPv6) are dialed as-is.
func dialContextPreferIPv4(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		return (&net.Dialer{Timeout: agentDialWait}).DialContext(ctx, network, address)
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var v4, v6 []net.IPAddr
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	order := append(append([]net.IPAddr{}, v4...), v6...)
	if len(order) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}

	d := &net.Dialer{Timeout: agentDialWait}
	var last error
	for _, ip := range order {
		n := network
		switch network {
		case "tcp", "tcp4", "tcp6":
			if ip.IP.To4() != nil {
				n = "tcp4"
			} else {
				n = "tcp6"
			}
		}
		conn, err := d.DialContext(ctx, n, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("dial %s: no usable address", address)
	}
	return nil, last
}

// connectAndServe ensures enrollment, connects the websocket, and serves.
func (a *Agent) connectAndServe() error {
	if err := a.ensureEnrolled(); err != nil {
		return err
	}
	wsURL := wsURL(a.cfg.Server) + "/agent/ws?token=" + url.QueryEscape(a.state.AgentToken)
	conn, resp, err := a.dialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			a.state.AgentToken = ""
			_ = SaveState(a.state)
		}
		return err
	}
	if resp != nil {
		resp.Body.Close()
	}
	a.conn = conn
	defer func() {
		a.writeMu.Lock()
		a.conn = nil
		a.writeMu.Unlock()
		conn.Close()
	}()

	log.Printf("agent: connected to panel")
	a.sendEnv(proto.MsgHello, "", a.hello())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.metricsLoop(ctx)
	go a.containersLoop(ctx)
	go a.pingLoop(ctx, conn)

	// Keepalive: the read deadline is refreshed every time a pong arrives from
	// the panel. If the WSS connection goes half-open (Cloudflare/NAT silently
	// drops it, no close frame) the pongs stop, the deadline fires, ReadMessage
	// returns a timeout, and Run's outer loop reconnects — instead of blocking
	// forever on a dead socket.
	conn.SetReadDeadline(time.Now().Add(agentPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(agentPongWait))
		return nil
	})

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var env proto.Envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		a.dispatch(env)
	}
}

// pingLoop sends a WebSocket ping every agentPingPeriod so the panel replies
// with a pong (its default PingHandler), which refreshes the read deadline set
// in connectAndServe. WriteControl is safe to call concurrently with the data
// writes in sendEnv, so it needs no lock. Stops when ctx is cancelled (the
// connection is closing / reconnecting).
func (a *Agent) pingLoop(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(agentPingPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(agentWriteWait)); err != nil {
				return
			}
		}
	}
}

func (a *Agent) ensureEnrolled() error {
	if a.state.AgentToken != "" {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"token": a.cfg.Token, "hello": a.hello()})
	req, err := http.NewRequest(http.MethodPost, a.cfg.Server+"/api/agent/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := panelHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("enroll failed (%d): %s", resp.StatusCode, string(b))
	}
	var out struct {
		AgentToken string `json:"agent_token"`
		NodeID     string `json:"node_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.AgentToken == "" {
		return fmt.Errorf("enroll returned no token")
	}
	a.state.AgentToken = out.AgentToken
	a.state.NodeID = out.NodeID
	return SaveState(a.state)
}

func (a *Agent) metricsLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	// send an immediate sample
	a.sendEnv(proto.MsgMetrics, "", a.metrics.sample())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sendEnv(proto.MsgMetrics, "", a.metrics.sample())
		}
	}
}

func (a *Agent) dispatch(env proto.Envelope) {
	switch env.Type {
	case proto.MsgExec:
		var req proto.ExecRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handleExec(env.ID, req)
	case proto.MsgScanSSH:
		var req proto.ScanSSHRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handleScanSSH(env.ID, req)
	case proto.MsgTestSSH:
		var req proto.TestSSHRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handleTestSSH(env.ID, req)
	case proto.MsgBackup:
		var req proto.BackupRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handleBackup(env.ID, req)
	case proto.MsgRestore:
		var req proto.RestoreRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handleRestore(env.ID, req)
	case proto.MsgRestorePreflight:
		var req proto.RestorePreflightRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handlePreflight(env.ID, req)
	case proto.MsgContainerOp:
		var req proto.ContainerOpRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handleContainerOp(env.ID, req)
	case proto.MsgContainerScan:
		go a.handleContainerScan(env.ID)
	case proto.MsgAgentUpdate:
		go a.handleAgentUpdate(env.ID)
	case proto.MsgHTTPFetch:
		var req proto.HTTPFetchRequest
		_ = json.Unmarshal(env.Data, &req)
		go a.handleHTTPFetch(env.ID, req)
	case proto.MsgPing:
		a.sendEnv(proto.MsgPong, env.ID, nil)
	}
}

// sendEnv wraps a payload and writes it to the websocket.
func (a *Agent) sendEnv(typ, id string, data any) {
	env, err := proto.Encode(typ, id, data)
	if err != nil {
		return
	}
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if a.conn != nil {
		_ = a.conn.WriteMessage(websocket.TextMessage, b)
	}
}

func (a *Agent) hello() proto.HelloData {
	ipv4, ipv6 := netInfo()
	return proto.HelloData{
		AgentVersion: AgentVersion,
		Hostname:     hostName(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Kernel:       kernelRelease(),
		IPv4:         ipv4,
		IPv6:         ipv6,
		Timezone:     timezone(),
		Uptime:       uptime(),
	}
}

func wsURL(server string) string {
	server = strings.TrimSpace(server)
	switch {
	case strings.HasPrefix(server, "https://"):
		return "wss://" + strings.TrimPrefix(server, "https://")
	case strings.HasPrefix(server, "http://"):
		return "ws://" + strings.TrimPrefix(server, "http://")
	default:
		return server
	}
}

// backoff returns how long to wait before the next reconnect attempt.
// First failure reconnects in 1s (was 4s) so a panel restart or CF blip does not
// leave the node offline for most of a short maintenance window. Cap at 30s.
func backoff(failures int) time.Duration {
	if failures <= 1 {
		return time.Second
	}
	// failures=2 → 2s, 3 → 4s, 4 → 8s, …
	shift := failures - 1
	if shift > 5 {
		shift = 5
	}
	d := time.Duration(1<<uint(shift)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func sleep(d time.Duration) { time.Sleep(d) }

func kernelRelease() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func timezone() string {
	if _, err := os.Stat("/etc/timezone"); err == nil {
		b, err := os.ReadFile("/etc/timezone")
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return time.Local.String()
}

// itoa is a dependency-free int -> string used by other files.
func itoa(n int) string { return fmt.Sprintf("%d", n) }
