// Package cloudflare is a thin client for the subset of the Cloudflare API
// NodePanel needs: remotely-managed Tunnel ingress configuration (the
// config.ingress field) and DNS CNAME management, used to re-point a migrated
// container's public domain from one node's tunnel to another's.
//
// It speaks the token-based API (Authorization: Bearer <api token>). The account
// id is resolved from the token itself (GET /accounts) and cached.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

// Client is a Cloudflare API client. The zero value is not usable — use New.
type Client struct {
	token     string
	accountID string // resolved lazily by AccountID(), then cached
	hc        *http.Client
}

// New returns a client authenticated with the given API token.
func New(token string) *Client {
	return &Client{
		token: strings.TrimSpace(token),
		hc:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Token reports whether the client has a configured token.
func (c *Client) Token() bool { return c.token != "" }

// Tunnel is one Cloudflare Tunnel (id + name + health status).
type Tunnel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status,omitempty"` // "healthy" / "degraded" / "down" / "inactive"
}

// cfEnvelope is the standard Cloudflare response wrapper.
type cfEnvelope struct {
	Success    bool            `json:"success"`
	Errors     []cfMessage     `json:"errors"`
	Messages   []cfMessage     `json:"messages"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *resultInfo     `json:"result_info,omitempty"`
}
type cfMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// resultInfo is CF's pagination block. TotalPages drives the page loop in
// ListZones / ListRecords (the endpoints cap per_page at 50 / 200 respectively
// and silently truncate anything higher, so multi-page fetching is mandatory).
type resultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
}

// do performs a Cloudflare API call and returns the unwrapped result. A non-2xx
// HTTP status OR success=false is turned into a Go error carrying the API message.
func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	env, err := c.doEnvelope(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return env.Result, nil
}

// doEnvelope is like do but returns the full envelope so callers that need
// ResultInfo (pagination) can read it.
func (c *Client) doEnvelope(ctx context.Context, method, path string, body any) (*cfEnvelope, error) {
	if c.token == "" {
		return nil, fmt.Errorf("cloudflare: no api token configured")
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// Not JSON (rare) — surface status + a snippet.
		snip := string(raw)
		if len(snip) > 300 {
			snip = snip[:300]
		}
		return nil, fmt.Errorf("cloudflare: %s %s: HTTP %d: %s", method, path, resp.StatusCode, snip)
	}
	if !env.Success || resp.StatusCode >= 400 {
		return nil, cfError(method, path, resp.StatusCode, env)
	}
	return &env, nil
}

func cfError(method, path string, status int, env cfEnvelope) error {
	var msgs []string
	for _, m := range env.Errors {
		msgs = append(msgs, fmt.Sprintf("[%d] %s", m.Code, m.Message))
	}
	for _, m := range env.Messages {
		msgs = append(msgs, m.Message)
	}
	joined := strings.Join(msgs, "; ")
	if joined == "" {
		joined = fmt.Sprintf("http %d", status)
	}
	return fmt.Errorf("cloudflare %s %s: %s", method, path, joined)
}

// AccountID resolves and caches the account id accessible by this token
// (the first account returned by GET /accounts).
func (c *Client) AccountID(ctx context.Context) (string, error) {
	if c.accountID != "" {
		return c.accountID, nil
	}
	res, err := c.do(ctx, http.MethodGet, "/accounts?per_page=5", nil)
	if err != nil {
		return "", err
	}
	var accounts []Tunnel // {id,name} shape suffices
	if err := json.Unmarshal(res, &accounts); err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "", fmt.Errorf("cloudflare: token has no accessible account")
	}
	c.accountID = accounts[0].ID
	return c.accountID, nil
}

// ListTunnels returns the tunnels in the account (id + name). Used by the
// settings "test connection" view to confirm the token can read tunnels.
func (c *Client) ListTunnels(ctx context.Context) ([]Tunnel, error) {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, http.MethodGet, "/accounts/"+acct+"/cfd_tunnel?per_page=50&is_deleted=false", nil)
	if err != nil {
		return nil, err
	}
	var out []Tunnel
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateTunnel creates a remotely-managed (config_src=cloudflare) tunnel and
// returns its id + the connector token passed to `cloudflared tunnel run`. A
// remotely-managed tunnel needs NO tunnel_secret; the connector token is
// returned once and the caller persists it for re-provisioning.
func (c *Client) CreateTunnel(ctx context.Context, name string) (id, token string, err error) {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return "", "", err
	}
	res, err := c.do(ctx, http.MethodPost, "/accounts/"+acct+"/cfd_tunnel",
		map[string]any{"name": name, "config_src": "cloudflare"})
	if err != nil {
		return "", "", err
	}
	var t struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res, &t); err != nil {
		return "", "", err
	}
	return t.ID, t.Token, nil
}

// RenameTunnel renames a tunnel. The tunnel id is unchanged, so a running
// connector is unaffected. Works for any tunnel the token can edit, including
// ones not created from this panel. The name must satisfy Cloudflare's tunnel
// naming rules (caller validates).
func (c *Client) RenameTunnel(ctx context.Context, tunnelID, newName string) error {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPatch,
		"/accounts/"+acct+"/cfd_tunnel/"+url.PathEscape(tunnelID),
		map[string]any{"name": newName})
	return err
}

// DeleteTunnel removes a tunnel. Cloudflare refuses if the tunnel still has
// active connector connections — stop the node's cloudflared first.
func (c *Client) DeleteTunnel(ctx context.Context, tunnelID string) error {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete,
		"/accounts/"+acct+"/cfd_tunnel/"+url.PathEscape(tunnelID), nil)
	return err
}

// Connection is one live connector session reported by a tunnel.
type Connection struct {
	ColoName           string `json:"colo_name"`
	ClientID           string `json:"client_id"`
	ClientVersion      string `json:"client_version"`
	IsPendingReconnect bool   `json:"is_pending_reconnect"`
}

// GetTunnel returns a single tunnel's metadata (status) + its live connections,
// for monitoring detail (colo / cloudflared version).
func (c *Client) GetTunnel(ctx context.Context, tunnelID string) (tunnel Tunnel, conns []Connection, err error) {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return Tunnel{}, nil, err
	}
	res, err := c.do(ctx, http.MethodGet,
		"/accounts/"+acct+"/cfd_tunnel/"+url.PathEscape(tunnelID), nil)
	if err != nil {
		return Tunnel{}, nil, err
	}
	var full struct {
		ID          string       `json:"id"`
		Name        string       `json:"name"`
		Status      string       `json:"status"`
		Connections []Connection `json:"connections"`
	}
	if err := json.Unmarshal(res, &full); err != nil {
		return Tunnel{}, nil, err
	}
	return Tunnel{ID: full.ID, Name: full.Name, Status: full.Status}, full.Connections, nil
}

// GetConfig returns a remotely-managed tunnel's full configuration object (the
// `config` map: ingress, warp-routing, …). Callers mutate `ingress` and PutConfig
// the whole map back so unrelated config is preserved.
func (c *Client) GetConfig(ctx context.Context, tunnelID string) (map[string]any, error) {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, http.MethodGet,
		"/accounts/"+acct+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/configurations", nil)
	if err != nil {
		return nil, err
	}
	var outer struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(res, &outer); err != nil {
		return nil, err
	}
	if outer.Config == nil {
		outer.Config = map[string]any{}
	}
	return outer.Config, nil
}

// PutConfig replaces a tunnel's configuration with cfg. The whole config object
// is sent back (we only ever mutate the ingress array), preserving warp-routing
// and other fields CF stores.
func (c *Client) PutConfig(ctx context.Context, tunnelID string, cfg map[string]any) error {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"config": cfg}
	_, err = c.do(ctx, http.MethodPut,
		"/accounts/"+acct+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/configurations", body)
	return err
}

// IngressRule is one entry of a tunnel's config.ingress array. It is a generic
// map so every Cloudflare field (hostname, service, path, originRequest, …) is
// preserved verbatim across a move — we only ever rewrite the `service` value.
type IngressRule map[string]any

func (r IngressRule) Hostname() string {
	h, _ := r["hostname"].(string)
	return h
}
func (r IngressRule) Service() string {
	s, _ := r["service"].(string)
	return s
}
func (r IngressRule) SetService(s string) { r["service"] = s }

// IsCatchAll reports whether this is the terminal catch-all rule (no hostname),
// which must always remain last in the ingress array.
func (r IngressRule) IsCatchAll() bool {
	_, ok := r["hostname"]
	return !ok
}

// IngressFromConfig extracts the ingress array from a config map (empty if none).
func IngressFromConfig(cfg map[string]any) []IngressRule {
	arr, _ := cfg["ingress"].([]any)
	out := make([]IngressRule, 0, len(arr))
	for _, a := range arr {
		if m, ok := a.(map[string]any); ok {
			out = append(out, IngressRule(m))
		}
	}
	return out
}

// ServicePort extracts the numeric host port from a tunnel service URL like
// "http://localhost:8089" / "https://127.0.0.1:8443" (the part after the last
// ':'). Returns 0 if it can't be parsed.
func ServicePort(service string) int {
	service = strings.TrimRight(service, "/")
	i := strings.LastIndex(service, ":")
	if i < 0 {
		return 0
	}
	p, err := strconv.Atoi(service[i+1:])
	if err != nil {
		return 0
	}
	return p
}

// RewriteServicePort returns service with its port replaced by newPort, keeping
// scheme and host intact ("http://localhost:8089" + 34152 → "http://localhost:34152").
func RewriteServicePort(service string, newPort int) string {
	service = strings.TrimRight(service, "/")
	i := strings.LastIndex(service, ":")
	if i < 0 {
		return service
	}
	return service[:i+1] + strconv.Itoa(newPort)
}

// zoneIDFor resolves the zone id for a hostname by querying Cloudflare for
// progressively shorter parent domains until one is a registered zone. E.g. for
// "a.b.example.com" it tries "a.b.example.com", "b.example.com", "example.com".
func (c *Client) zoneIDFor(ctx context.Context, hostname string) (string, error) {
	id, _, err := c.zoneFor(ctx, hostname)
	return id, err
}

// zoneFor is like zoneIDFor but also returns the matching zone name (the base
// domain of the hostname — what container-migration strips to get the subdomain
// prefix when renaming a migrated domain).
func (c *Client) zoneFor(ctx context.Context, hostname string) (id, name string, err error) {
	labels := strings.Split(strings.TrimSuffix(hostname, "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		zone := strings.Join(labels[i:], ".")
		res, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(zone), nil)
		if err != nil {
			continue
		}
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(res, &zones) == nil && len(zones) > 0 {
			return zones[0].ID, zones[0].Name, nil
		}
	}
	return "", "", fmt.Errorf("cloudflare: no zone found for %s", hostname)
}

// ZoneNameFor returns the registered zone (base domain) of a hostname, e.g.
// "panel.example.com" → "example.com". Used to split a hostname into prefix + base.
func (c *Client) ZoneNameFor(ctx context.Context, hostname string) (string, error) {
	_, name, err := c.zoneFor(ctx, hostname)
	if err != nil {
		return "", err
	}
	return name, nil
}

// RepointCNAME updates the DNS CNAME for hostname so it targets
// "<targetTunnelID>.cfargotunnel.com" (the standard tunnel CNAME target). Returns
// the previous CNAME target (for logging) and an error if the record can't be
// found or updated.
func (c *Client) RepointCNAME(ctx context.Context, hostname, targetTunnelID string) (prev string, err error) {
	zone, err := c.zoneIDFor(ctx, hostname)
	if err != nil {
		return "", err
	}
	res, err := c.do(ctx, http.MethodGet,
		"/zones/"+zone+"/dns_records?name="+url.QueryEscape(hostname)+"&type=CNAME", nil)
	if err != nil {
		return "", err
	}
	var recs []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(res, &recs); err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "", fmt.Errorf("cloudflare: no CNAME record for %s (is the hostname proxied via tunnel?)", hostname)
	}
	rec := recs[0]
	target := strings.ToLower(targetTunnelID) + ".cfargotunnel.com"
	if strings.EqualFold(rec.Content, target) {
		return rec.Content, nil // already points there
	}
	_, err = c.do(ctx, http.MethodPatch, "/zones/"+zone+"/dns_records/"+rec.ID,
		map[string]any{"content": target})
	return rec.Content, err
}

// CreateOrUpdateCNAME ensures a CNAME record for hostname points at
// "<targetTunnelID>.cfargotunnel.com" (proxied/orange). If a CNAME exists it is
// patched; otherwise a new proxied CNAME is created. Used when renaming a
// migrated container's domain to a brand-new hostname. Returns whether a new
// record was created.
func (c *Client) CreateOrUpdateCNAME(ctx context.Context, hostname, targetTunnelID string) (created bool, err error) {
	zone, err := c.zoneIDFor(ctx, hostname)
	if err != nil {
		return false, err
	}
	target := CanonicalCNAMETarget(targetTunnelID)
	res, err := c.do(ctx, http.MethodGet,
		"/zones/"+zone+"/dns_records?name="+url.QueryEscape(hostname)+"&type=CNAME", nil)
	if err != nil {
		return false, err
	}
	var recs []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(res, &recs); err != nil {
		return false, err
	}
	if len(recs) > 0 {
		if strings.EqualFold(recs[0].Content, target) {
			return false, nil // already correct
		}
		_, err = c.do(ctx, http.MethodPatch, "/zones/"+zone+"/dns_records/"+recs[0].ID,
			map[string]any{"content": target, "proxied": true})
		return false, err
	}
	_, err = c.do(ctx, http.MethodPost, "/zones/"+zone+"/dns_records", map[string]any{
		"type": "CNAME", "name": hostname, "content": target, "proxied": true,
	})
	return err == nil, err
}

// DeleteCNAME removes the CNAME record for hostname. A missing record is treated
// as success (the hostname may never have had one). Used to clean up a migrated
// container's old domain after it has been renamed.
func (c *Client) DeleteCNAME(ctx context.Context, hostname string) error {
	zone, err := c.zoneIDFor(ctx, hostname)
	if err != nil {
		return nil // no zone ⇒ no record to delete; treat as success
	}
	res, err := c.do(ctx, http.MethodGet,
		"/zones/"+zone+"/dns_records?name="+url.QueryEscape(hostname)+"&type=CNAME", nil)
	if err != nil {
		return err
	}
	var recs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res, &recs); err != nil {
		return err
	}
	for _, r := range recs {
		_, _ = c.do(ctx, http.MethodDelete, "/zones/"+zone+"/dns_records/"+r.ID, nil)
	}
	return nil
}

// CNAMEInfo is the resolved DNS state for one hostname.
type CNAMEInfo struct {
	Content string `json:"content"` // the CNAME target, e.g. "d1943d63-….cfargotunnel.com"
	Proxied bool   `json:"proxied"` // orange-cloud (proxied) vs blue (DNS-only)
}

// CanonicalCNAMETarget is the standard tunnel CNAME for a tunnel id.
func CanonicalCNAMETarget(tunnelID string) string {
	return strings.ToLower(tunnelID) + ".cfargotunnel.com"
}

// ResolveCNAMEs looks up the current CNAME DNS record for each hostname, batched
// per zone: one GET /zones plus one GET /zones/{id}/dns_records?type=CNAME per
// zone that is a suffix of any requested hostname. Hostnames with no CNAME record
// (or in zones the token can't read) are absent from the returned map. Keys are
// lower-cased, trailing-dot-trimmed hostnames.
func (c *Client) ResolveCNAMEs(ctx context.Context, hostnames []string) (map[string]CNAMEInfo, error) {
	out := map[string]CNAMEInfo{}
	want := make(map[string]bool, len(hostnames))
	for _, h := range hostnames {
		h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		if h != "" {
			want[h] = true
		}
	}
	if len(want) == 0 {
		return out, nil
	}
	res, err := c.do(ctx, http.MethodGet, "/zones?per_page=50", nil)
	if err != nil {
		return nil, err
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(res, &zones); err != nil {
		return nil, err
	}
	for _, z := range zones {
		zn := strings.ToLower(z.Name)
		relevant := false
		for h := range want {
			if h == zn || strings.HasSuffix(h, "."+zn) {
				relevant = true
				break
			}
		}
		if !relevant {
			continue
		}
		res, err := c.do(ctx, http.MethodGet,
			"/zones/"+z.ID+"/dns_records?type=CNAME&per_page=200", nil)
		if err != nil {
			continue // zone not readable by this token — skip
		}
		var recs []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
			Proxied bool   `json:"proxied"`
		}
		if json.Unmarshal(res, &recs) != nil {
			continue
		}
		for _, r := range recs {
			name := strings.ToLower(r.Name)
			if !want[name] {
				continue
			}
			if _, exists := out[name]; !exists {
				out[name] = CNAMEInfo{Content: strings.TrimSuffix(r.Content, "."), Proxied: r.Proxied}
			}
		}
	}
	return out, nil
}

// Zone is one Cloudflare zone (a registered domain) the token can see.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // active / pending / ...
}

// Record is the flat, type-agnostic shape of a Cloudflare DNS record. The same
// struct is used for create/update payloads and list responses. Priority is a
// pointer so it is omitted from JSON for record types that don't use it (only
// MX/SRV carry one); SRV content is a flat "weight port target" string and CAA
// content is "flags tag value" — callers format those, we never parse.
type Record struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`                 // 1 == Auto (required when Proxied)
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`  // MX/SRV only
	Comment  string `json:"comment,omitempty"`
}

// ListZones returns every zone the token can read, paging at 50/page (the /zones
// endpoint cap). Used by the DNS panel's zone picker.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var out []Zone
	page := 1
	for {
		env, err := c.doEnvelope(ctx, http.MethodGet,
			fmt.Sprintf("/zones?per_page=50&page=%d", page), nil)
		if err != nil {
			return nil, err
		}
		var zones []Zone
		if err := json.Unmarshal(env.Result, &zones); err != nil {
			return nil, err
		}
		out = append(out, zones...)
		total := 1
		if env.ResultInfo != nil && env.ResultInfo.TotalPages > total {
			total = env.ResultInfo.TotalPages
		}
		if page >= total {
			break
		}
		page++
	}
	return out, nil
}

// ListRecords returns every DNS record in zoneID, paging at 200/page (the
// /dns_records endpoint cap).
func (c *Client) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	var out []Record
	page := 1
	for {
		env, err := c.doEnvelope(ctx, http.MethodGet,
			fmt.Sprintf("/zones/%s/dns_records?per_page=200&page=%d", url.PathEscape(zoneID), page), nil)
		if err != nil {
			return nil, err
		}
		var recs []Record
		if err := json.Unmarshal(env.Result, &recs); err != nil {
			return nil, err
		}
		out = append(out, recs...)
		total := 1
		if env.ResultInfo != nil && env.ResultInfo.TotalPages > total {
			total = env.ResultInfo.TotalPages
		}
		if page >= total {
			break
		}
		page++
	}
	return out, nil
}

// CreateRecord creates a DNS record in zoneID and returns the created record.
func (c *Client) CreateRecord(ctx context.Context, zoneID string, rec Record) (Record, error) {
	res, err := c.do(ctx, http.MethodPost,
		"/zones/"+url.PathEscape(zoneID)+"/dns_records", rec)
	if err != nil {
		return Record{}, err
	}
	var out Record
	if err := json.Unmarshal(res, &out); err != nil {
		return Record{}, err
	}
	return out, nil
}

// UpdateRecord replaces zoneID's record recID with rec via PUT (full overwrite,
// not PATCH) so fields the form doesn't show can't be silently dropped.
func (c *Client) UpdateRecord(ctx context.Context, zoneID, recID string, rec Record) (Record, error) {
	res, err := c.do(ctx, http.MethodPut,
		"/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recID), rec)
	if err != nil {
		return Record{}, err
	}
	var out Record
	if err := json.Unmarshal(res, &out); err != nil {
		return Record{}, err
	}
	return out, nil
}

// DeleteRecord removes a DNS record. A missing record is a CF error (not coerced
// to success here — the DNS panel surfaces it).
func (c *Client) DeleteRecord(ctx context.Context, zoneID, recID string) error {
	_, err := c.do(ctx, http.MethodDelete,
		"/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recID), nil)
	return err
}
