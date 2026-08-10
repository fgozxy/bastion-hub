package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"nodepanel/shared/proto"
)

// HTTPFetch asks the node's agent to GET a node-local URL and return the body
// (MsgHTTPFetch). Agents older than 2.1.0 don't speak MsgHTTPFetch, so we fall
// back to running curl over the existing Exec RPC. URL must be loopback
// (enforced here and again in the agent).
func (s *Service) HTTPFetch(ctx context.Context, nodeID, u string, timeout time.Duration) (proto.HTTPFetchResult, error) {
	if err := assertLoopbackURL(u); err != nil {
		return proto.HTTPFetchResult{Err: err.Error()}, nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	// Old agent → curl fallback over MsgExec.
	if n, err := s.Store.GetNode(ctx, nodeID); err == nil && !supportsHTTPFetch(n.AgentVersion) {
		cmd := fmt.Sprintf("curl -fsS --max-time %d '%s'", int(timeout.Seconds()), u)
		out, exit, err := s.Nodes.ExecSync(ctx, nodeID, cmd, timeout+8*time.Second)
		if err != nil {
			return proto.HTTPFetchResult{Err: err.Error()}, nil
		}
		if exit != 0 {
			msg := strings.TrimSpace(out)
			if msg == "" {
				msg = fmt.Sprintf("curl exit %d", exit)
			}
			return proto.HTTPFetchResult{Err: msg}, nil
		}
		return proto.HTTPFetchResult{Status: 200, Body: out}, nil
	}

	// Native MsgHTTPFetch.
	reqID := "hf:" + nodeID + ":" + uuid.NewString()
	ch := s.Hub.Subscribe(reqID)
	defer s.Hub.Unsubscribe(reqID)
	env, err := proto.Encode(proto.MsgHTTPFetch, reqID, proto.HTTPFetchRequest{URL: u, Timeout: int(timeout.Seconds())})
	if err != nil {
		return proto.HTTPFetchResult{Err: err.Error()}, nil
	}
	if err := s.Hub.Send(nodeID, env); err != nil {
		return proto.HTTPFetchResult{Err: "节点不可达: " + err.Error()}, nil
	}
	timer := time.NewTimer(timeout + 5*time.Second)
	defer timer.Stop()
	select {
	case msg, ok := <-ch:
		if !ok {
			return proto.HTTPFetchResult{Err: "agent disconnected"}, nil
		}
		var r proto.HTTPFetchResult
		if len(msg.Data) > 0 {
			_ = json.Unmarshal(msg.Data, &r)
		}
		return r, nil
	case <-timer.C:
		return proto.HTTPFetchResult{Err: "拉取超时"}, nil
	case <-ctx.Done():
		return proto.HTTPFetchResult{Err: ctx.Err().Error()}, nil
	}
}

// supportsHTTPFetch reports whether the agent understands MsgHTTPFetch (>=2.1.0).
func supportsHTTPFetch(version string) bool { return versionGE(version, "2.1.0") }

// versionGE is a minimal major.minor.patch compare. Unparseable → false.
func versionGE(a, b string) bool {
	pa := splitVer(a)
	pb := splitVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] >= pb[i]
		}
	}
	return true
}

func splitVer(v string) [3]int {
	var out [3]int
	v = strings.TrimSpace(v)
	parts := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(strings.SplitN(parts[i], "-", 2)[0])
		out[i] = n
	}
	return out
}

// assertLoopbackURL mirrors the agent's check: only loopback hosts, http(s) only.
func assertLoopbackURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("empty url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("bad url: %w", err)
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return fmt.Errorf("non-http(s) scheme rejected")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("non-loopback host rejected: %s", host)
}
