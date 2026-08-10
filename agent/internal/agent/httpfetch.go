package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nodepanel/shared/proto"
)

// handleHTTPFetch replies to MsgHTTPFetch: it performs an in-process net/http
// GET to a node-local URL (e.g. http://127.0.0.1:19999/api/v1/...) and returns
// the body. This is how the master pulls a node's local Netdata metrics without
// spawning curl per poll and without exposing the service publicly.
//
// The host MUST be loopback (127.x / ::1 / localhost). Without this check the
// master could direct any agent to GET arbitrary URLs — turning the agent into
// an SSRF / internal-network pivot. Only http(s) schemes are accepted.
func (a *Agent) handleHTTPFetch(id string, req proto.HTTPFetchRequest) {
	if err := assertLoopbackURL(req.URL); err != nil {
		a.sendEnv(proto.MsgHTTPFetchResult, id, proto.HTTPFetchResult{Err: err.Error()})
		return
	}
	timeout := 10 * time.Second
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	httpClient := &http.Client{Timeout: timeout}
	rq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		a.sendEnv(proto.MsgHTTPFetchResult, id, proto.HTTPFetchResult{Err: err.Error()})
		return
	}
	resp, err := httpClient.Do(rq)
	if err != nil {
		a.sendEnv(proto.MsgHTTPFetchResult, id, proto.HTTPFetchResult{Err: err.Error()})
		return
	}
	defer resp.Body.Close()

	// Cap the body so a misbehaving local service can't saturate the websocket
	// or OOM the agent. Read one byte past the cap to detect truncation.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(proto.HTTPFetchMaxBody)+1))
	if int64(len(body)) > proto.HTTPFetchMaxBody {
		body = body[:proto.HTTPFetchMaxBody]
	}
	a.sendEnv(proto.MsgHTTPFetchResult, id, proto.HTTPFetchResult{
		Status: resp.StatusCode,
		Body:   string(body),
	})
}

// assertLoopbackURL rejects any URL whose host is not loopback. Prevents the
// agent from being used as an open proxy / SSRF relay by a compromised master.
func assertLoopbackURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("empty url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("bad url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("non-http(s) scheme rejected: %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("non-loopback host rejected: %s", host)
}
