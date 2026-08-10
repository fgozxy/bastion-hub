// Package domains powers the 域名 (Domains) panel: a read/edit view over the
// Cloudflare Tunnel ingress ("config.ingress") that fronts every public hostname.
// It lists each tunnel's hostname→service rules with live DNS-CNAME status, and
// lets the admin edit a rule's origin, add/delete rules, or move a hostname from
// one tunnel to another (repointing DNS).
//
// All access goes through the existing cloudflare.Client using the API token
// saved under the 'cloudflare' setting — the same token container migration
// uses (see backup.cfClient / settings.TestCloudflare). Writes are hot-reloaded:
// remotely-managed tunnels apply a PUT configurations within seconds.
package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"nodepanel/master/internal/cloudflare"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
)

const noTokenMsg = "未配置 Cloudflare 令牌，请先到「设置 → Cloudflare」配置 API token"

// Service implements the /api/domains endpoints.
type Service struct {
	Store *store.Store
}

// cfClient builds a Cloudflare client from the saved 'cloudflare' setting, or
// nil when none is configured. Mirrors backup.cfClient.
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

type dnsInfo struct {
	Target  string `json:"target"`            // CNAME target, "" if no CNAME record exists
	Proxied bool   `json:"proxied"`           // orange-cloud (proxied) vs blue (DNS-only)
	Matches bool   `json:"matches"`           // target == this tunnel's cfargotunnel.com
}

type ruleOut struct {
	Hostname   string   `json:"hostname"`
	Path       string   `json:"path,omitempty"`
	Service    string   `json:"service"`
	IsCatchAll bool     `json:"is_catch_all"`
	DNS        *dnsInfo `json:"dns,omitempty"` // nil for the catch-all rule
}

type nodeRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type tunnelOut struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status,omitempty"`
	Node        *nodeRef  `json:"node,omitempty"`
	CNAMETarget string    `json:"cname_target"` // expected: <tunnelID>.cfargotunnel.com
	Error       string    `json:"error,omitempty"`
	Rules       []ruleOut `json:"rules"`
}

// List GET /api/domains — every tunnel, its ingress rules, the node it maps to
// (via nodes.tunnel_id), and each hostname's live CNAME status. The per-tunnel
// GetConfig calls and the single ResolveCNAMEs call run concurrently.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	ctx := r.Context()
	acct, err := cf.AccountID(ctx)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取 Cloudflare 账号失败: "+err.Error())
		return
	}
	tunnels, err := cf.ListTunnels(ctx)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取 tunnel 列表失败: "+err.Error())
		return
	}

	// Map tunnel id → node (best-effort).
	nodes, _ := s.Store.ListNodes(ctx)
	nodeByTun := map[string]nodeRef{}
	for _, n := range nodes {
		if n.TunnelID != "" {
			nodeByTun[n.TunnelID] = nodeRef{ID: n.ID, Name: n.Name}
		}
	}

	// Fan out GetConfig across tunnels.
	type tcfg struct {
		tun cloudflare.Tunnel
		cfg map[string]any
		err error
	}
	results := make([]tcfg, len(tunnels))
	var wg sync.WaitGroup
	for i := range tunnels {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i].tun = tunnels[i]
			results[i].cfg, results[i].err = cf.GetConfig(ctx, tunnels[i].ID)
		}(i)
	}
	wg.Wait()

	// Collect hostnames for one batched DNS lookup.
	var hosts []string
	for _, rc := range results {
		if rc.err != nil {
			continue
		}
		for _, rule := range cloudflare.IngressFromConfig(rc.cfg) {
			if !rule.IsCatchAll() && rule.Hostname() != "" {
				hosts = append(hosts, rule.Hostname())
			}
		}
	}
	dnsMap, _ := cf.ResolveCNAMEs(ctx, hosts)

	out := make([]tunnelOut, 0, len(results))
	for _, rc := range results {
		to := tunnelOut{
			ID:          rc.tun.ID,
			Name:        rc.tun.Name,
			Status:      rc.tun.Status,
			CNAMETarget: cloudflare.CanonicalCNAMETarget(rc.tun.ID),
		}
		if nr, ok := nodeByTun[rc.tun.ID]; ok {
			to.Node = &nr
		}
		if rc.err != nil {
			to.Error = rc.err.Error()
		} else {
			to.Rules = rulesWithDNS(cloudflare.IngressFromConfig(rc.cfg), dnsMap, to.CNAMETarget)
		}
		out = append(out, to)
	}
	httpx.OK(w, map[string]any{"account_id": acct, "tunnels": out})
}

// rulesWithDNS builds the rule list, attaching each non-catch-all hostname's
// resolved CNAME (Target="" when no CNAME record exists).
func rulesWithDNS(ingress []cloudflare.IngressRule, dns map[string]cloudflare.CNAMEInfo, cnameTarget string) []ruleOut {
	rules := make([]ruleOut, 0, len(ingress))
	for _, rule := range ingress {
		ro := ruleOut{
			Hostname:   rule.Hostname(),
			Service:    rule.Service(),
			IsCatchAll: rule.IsCatchAll(),
		}
		if p, ok := rulePath(rule); ok {
			ro.Path = p
		}
		if !rule.IsCatchAll() && ro.Hostname != "" {
			info, found := dns[strings.ToLower(ro.Hostname)]
			ro.DNS = &dnsInfo{
				Target:  info.Content,
				Proxied: info.Proxied,
				Matches: found && strings.EqualFold(info.Content, cnameTarget),
			}
		}
		rules = append(rules, ro)
	}
	return rules
}

// AddRule POST /api/domains/rule {tunnel_id, hostname, path?, service}
// Inserts a new ingress rule before the catch-all, replacing any existing rule
// for the same hostname+path.
func (s *Service) AddRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TunnelID string `json:"tunnel_id"`
		Hostname string `json:"hostname"`
		Path     string `json:"path"`
		Service  string `json:"service"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求无效")
		return
	}
	body.TunnelID = strings.TrimSpace(body.TunnelID)
	body.Hostname = strings.TrimSpace(body.Hostname)
	body.Path = strings.TrimSpace(body.Path)
	body.Service = strings.TrimSpace(body.Service)
	if body.TunnelID == "" || body.Hostname == "" || body.Service == "" {
		httpx.Err(w, http.StatusBadRequest, "tunnel、域名、指向(service)均不能为空")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	rule := cloudflare.IngressRule{"hostname": body.Hostname, "service": body.Service}
	if body.Path != "" {
		rule["path"] = body.Path
	}
	rules, err := s.mutate(r.Context(), cf, body.TunnelID, func(ing *[]cloudflare.IngressRule) error {
		*ing = upsertBeforeCatchAll(*ing, rule)
		return nil
	})
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"tunnel_id": body.TunnelID, "rules": rules})
}

// EditRule PUT /api/domains/rule {tunnel_id, orig_hostname, orig_path?, hostname, path?, service}
// Locates the rule by its original hostname (+path) and rewrites hostname/path/service in place.
func (s *Service) EditRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TunnelID     string `json:"tunnel_id"`
		OrigHostname string `json:"orig_hostname"`
		OrigPath     string `json:"orig_path"`
		Hostname     string `json:"hostname"`
		Path         string `json:"path"`
		Service      string `json:"service"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求无效")
		return
	}
	body.TunnelID = strings.TrimSpace(body.TunnelID)
	body.OrigHostname = strings.TrimSpace(body.OrigHostname)
	body.Hostname = strings.TrimSpace(body.Hostname)
	body.Path = strings.TrimSpace(body.Path)
	body.Service = strings.TrimSpace(body.Service)
	if body.TunnelID == "" || body.OrigHostname == "" || body.Hostname == "" || body.Service == "" {
		httpx.Err(w, http.StatusBadRequest, "tunnel、原域名、新域名、指向(service)均不能为空")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	rules, err := s.mutate(r.Context(), cf, body.TunnelID, func(ing *[]cloudflare.IngressRule) error {
		idx := findRule(*ing, body.OrigHostname, body.OrigPath)
		if idx < 0 {
			return fmt.Errorf("找不到原域名 %s 的规则", body.OrigHostname)
		}
		rule := (*ing)[idx]
		rule["hostname"] = body.Hostname
		rule["service"] = body.Service
		if body.Path != "" {
			rule["path"] = body.Path
		} else {
			delete(rule, "path")
		}
		(*ing)[idx] = rule
		return nil
	})
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"tunnel_id": body.TunnelID, "rules": rules})
}

// DeleteRule DELETE /api/domains/rule?tunnel_id=&hostname=&path=
// Removes the rule; the hostname then falls through to the catch-all (404). DNS
// is left untouched.
func (s *Service) DeleteRule(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tunnelID := strings.TrimSpace(q.Get("tunnel_id"))
	hostname := strings.TrimSpace(q.Get("hostname"))
	path := strings.TrimSpace(q.Get("path"))
	if tunnelID == "" || hostname == "" {
		httpx.Err(w, http.StatusBadRequest, "tunnel 与域名不能为空")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	rules, err := s.mutate(r.Context(), cf, tunnelID, func(ing *[]cloudflare.IngressRule) error {
		idx := findRule(*ing, hostname, path)
		if idx < 0 {
			return fmt.Errorf("找不到域名 %s 的规则", hostname)
		}
		*ing = append((*ing)[:idx], (*ing)[idx+1:]...)
		return nil
	})
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"tunnel_id": tunnelID, "rules": rules})
}

// Move POST /api/domains/move {hostname, from_tunnel, to_tunnel, service}
// Moves a hostname to another tunnel following migrate.go's safe order: add to
// destination → repoint DNS → remove from source, with rollback on DNS failure.
func (s *Service) Move(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hostname   string `json:"hostname"`
		FromTunnel string `json:"from_tunnel"`
		ToTunnel   string `json:"to_tunnel"`
		Service    string `json:"service"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求无效")
		return
	}
	body.Hostname = strings.TrimSpace(body.Hostname)
	body.FromTunnel = strings.TrimSpace(body.FromTunnel)
	body.ToTunnel = strings.TrimSpace(body.ToTunnel)
	body.Service = strings.TrimSpace(body.Service)
	if body.Hostname == "" || body.FromTunnel == "" || body.ToTunnel == "" || body.Service == "" {
		httpx.Err(w, http.StatusBadRequest, "域名、源 tunnel、目标 tunnel、指向(service)均不能为空")
		return
	}
	if body.FromTunnel == body.ToTunnel {
		httpx.Err(w, http.StatusBadRequest, "源与目标 tunnel 相同，请直接编辑指向")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	ctx := r.Context()
	h := body.Hostname

	// 1. Add the rule to the destination tunnel (replace any existing same hostname).
	destCfg, err := cf.GetConfig(ctx, body.ToTunnel)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取目标 tunnel 配置失败: "+err.Error())
		return
	}
	destIng := cloudflare.IngressFromConfig(destCfg)
	destIng = upsertBeforeCatchAll(destIng, cloudflare.IngressRule{"hostname": h, "service": body.Service})
	destCfg["ingress"] = toAny(destIng)
	if err := cf.PutConfig(ctx, body.ToTunnel, destCfg); err != nil {
		httpx.Err(w, http.StatusBadGateway, "写入目标 tunnel ingress 失败: "+err.Error())
		return
	}

	// 2. Repoint DNS to the destination tunnel; roll back the destination ingress on failure.
	if _, err := cf.RepointCNAME(ctx, h, body.ToTunnel); err != nil {
		if destCfg2, _ := cf.GetConfig(ctx, body.ToTunnel); destCfg2 != nil {
			destCfg2["ingress"] = toAny(removeHostname(cloudflare.IngressFromConfig(destCfg2), h))
			_ = cf.PutConfig(ctx, body.ToTunnel, destCfg2)
		}
		httpx.Err(w, http.StatusBadGateway, fmt.Sprintf("DNS 切换失败 (%s): %s；已回滚目标 ingress", h, err.Error()))
		return
	}

	// 3. Remove the rule from the source tunnel. Non-fatal: DNS already points to dest.
	note := ""
	if srcCfg, err := cf.GetConfig(ctx, body.FromTunnel); err != nil {
		note = "域名已切到目标 tunnel，但读取源 tunnel 配置失败: " + err.Error() + "（不影响访问）"
	} else {
		srcCfg["ingress"] = toAny(removeHostname(cloudflare.IngressFromConfig(srcCfg), h))
		if err := cf.PutConfig(ctx, body.FromTunnel, srcCfg); err != nil {
			note = "域名已切到目标 tunnel，但清理源 tunnel ingress 失败: " + err.Error() + "（不影响访问）"
		}
	}
	httpx.OK(w, map[string]any{
		"moved":       true,
		"hostname":    h,
		"from_tunnel": body.FromTunnel,
		"to_tunnel":   body.ToTunnel,
		"note":        note,
	})
}

// mutate reads a tunnel's config, hands the ingress slice to fn (which may
// rewrite it in place), writes the whole config back, and returns the resulting
// plain rule list. Only the ingress array is ever changed; warp-routing and
// other config keys are preserved.
func (s *Service) mutate(ctx context.Context, cf *cloudflare.Client, tunnelID string, fn func(*[]cloudflare.IngressRule) error) ([]ruleOut, error) {
	cfg, err := cf.GetConfig(ctx, tunnelID)
	if err != nil {
		return nil, fmt.Errorf("读取 tunnel 配置失败: %w", err)
	}
	ingress := cloudflare.IngressFromConfig(cfg)
	if err := fn(&ingress); err != nil {
		return nil, err
	}
	cfg["ingress"] = toAny(ingress)
	if err := cf.PutConfig(ctx, tunnelID, cfg); err != nil {
		return nil, fmt.Errorf("写入 tunnel 配置失败: %w", err)
	}
	return plainRules(ingress), nil
}

// plainRules is the ingress view returned from mutations (no DNS, for feedback).
func plainRules(ingress []cloudflare.IngressRule) []ruleOut {
	rules := make([]ruleOut, 0, len(ingress))
	for _, rule := range ingress {
		ro := ruleOut{Hostname: rule.Hostname(), Service: rule.Service(), IsCatchAll: rule.IsCatchAll()}
		if p, ok := rulePath(rule); ok {
			ro.Path = p
		}
		rules = append(rules, ro)
	}
	return rules
}

// rulePath reports the rule's path and whether a path key is present.
func rulePath(r cloudflare.IngressRule) (string, bool) {
	p, ok := r["path"].(string)
	return p, ok
}

// findRule returns the index of the non-catch-all rule whose hostname matches
// and whose path matches (path=="" ⇒ a rule with no path key), or -1.
func findRule(ingress []cloudflare.IngressRule, host, path string) int {
	for i, r := range ingress {
		if r.IsCatchAll() || r.Hostname() != host {
			continue
		}
		rp, hasPath := rulePath(r)
		if path == "" {
			if !hasPath {
				return i
			}
		} else if hasPath && rp == path {
			return i
		}
	}
	return -1
}

// upsertBeforeCatchAll drops any existing rule with the same hostname+path as
// `rule` (replace semantics) and inserts `rule` immediately before the catch-all
// (kept last). Rules with no catch-all simply append.
func upsertBeforeCatchAll(ingress []cloudflare.IngressRule, rule cloudflare.IngressRule) []cloudflare.IngressRule {
	host := rule.Hostname()
	_, ruleHasPath := rulePath(rule)
	out := make([]cloudflare.IngressRule, 0, len(ingress)+1)
	var catchAll []cloudflare.IngressRule
	for _, r := range ingress {
		if r.IsCatchAll() {
			catchAll = append(catchAll, r)
			continue
		}
		if r.Hostname() == host {
			if _, hp := rulePath(r); hp == ruleHasPath {
				continue // replace existing same hostname+path
			}
		}
		out = append(out, r)
	}
	out = append(out, rule)
	out = append(out, catchAll...)
	return out
}

// removeHostname drops every non-catch-all rule whose hostname matches.
func removeHostname(ingress []cloudflare.IngressRule, host string) []cloudflare.IngressRule {
	out := make([]cloudflare.IngressRule, 0, len(ingress))
	for _, r := range ingress {
		if !r.IsCatchAll() && r.Hostname() == host {
			continue
		}
		out = append(out, r)
	}
	return out
}

// toAny converts an ingress slice to []any for JSON-encoding into config.ingress.
func toAny(rules []cloudflare.IngressRule) []any {
	out := make([]any, 0, len(rules))
	for _, r := range rules {
		out = append(out, map[string]any(r))
	}
	return out
}
