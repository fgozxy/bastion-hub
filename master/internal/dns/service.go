// Package dns powers the DNS panel: a full Cloudflare DNS record editor over
// every zone the saved API token can read. Unlike the domains panel (which only
// edits Tunnel ingress + the CNAMEs that point at tunnels), this manages A /
// AAAA / CNAME / MX / TXT / NS / SRV / CAA / … records of any zone, with the
// record content pointing anywhere the operator wants.
//
// It is stateless: the source of truth is the Cloudflare API, read through the
// existing cloudflare.Client. There is no DB table for DNS records.
package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"nodepanel/master/internal/cloudflare"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
)

const noTokenMsg = "未配置 Cloudflare 令牌，请先到「设置 → Cloudflare」配置 API token"

// Service implements the /api/dns endpoints.
type Service struct {
	Store *store.Store
}

// cfClient builds a Cloudflare client from the saved 'cloudflare' setting, or
// nil when none is configured. Mirrors domains.cfClient.
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

// allowedTypes is the set of DNS record types the panel will create/edit. It
// covers everything an operator normally touches; exotic types (NAPTR, SMIMEA,
// …) can be added here when needed.
var allowedTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true,
	"NS": true, "SRV": true, "CAA": true, "PTR": true, "LOC": true,
	"SSHFP": true, "DS": true, "TLSA": true,
}

// isProxiableType reports whether CF allows the orange cloud for this type.
// Only A/AAAA/CNAME may be proxied; CF rejects proxied:true for everything else
// ("DNS record type is not allowed to be proxied").
func isProxiableType(t string) bool {
	t = strings.ToUpper(t)
	return t == "A" || t == "AAAA" || t == "CNAME"
}

// usesPriority reports whether the type carries a priority field (MX/SRV).
func usesPriority(t string) bool {
	t = strings.ToUpper(t)
	return t == "MX" || t == "SRV"
}

// recordReq is the JSON body for create/update. ZoneID is only used on create
// (update carries zone_id as a query param + the record id in the path).
type recordReq struct {
	ZoneID   string `json:"zone_id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority"`
	Comment  string `json:"comment"`
}

// toRecord builds a cloudflare.Record from the request, applying the coercions
// CF requires: proxied forced off for non-proxiable types, TTL forced to 1
// (Auto) when proxied or when left at 0, and priority dropped for non-MX/SRV.
func toRecord(b recordReq) cloudflare.Record {
	t := strings.ToUpper(strings.TrimSpace(b.Type))
	rec := cloudflare.Record{
		Type:    t,
		Name:    strings.TrimSpace(b.Name),
		Content: b.Content,
		TTL:     b.TTL,
		Proxied: b.Proxied,
		Comment: strings.TrimSpace(b.Comment),
	}
	if !isProxiableType(t) {
		rec.Proxied = false
	}
	if rec.Proxied || rec.TTL == 0 {
		rec.TTL = 1
	}
	if usesPriority(t) && b.Priority != nil {
		p := *b.Priority
		rec.Priority = &p
	}
	return rec
}

// Zones GET /api/dns/zones — every zone the token can read (for the picker).
func (s *Service) Zones(w http.ResponseWriter, r *http.Request) {
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	zones, err := cf.ListZones(r.Context())
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取域名列表失败: "+err.Error())
		return
	}
	httpx.OK(w, map[string]any{"zones": zones})
}

// Records GET /api/dns/records?zone_id= — all DNS records in the zone.
func (s *Service) Records(w http.ResponseWriter, r *http.Request) {
	zoneID := strings.TrimSpace(r.URL.Query().Get("zone_id"))
	if zoneID == "" {
		httpx.Err(w, http.StatusBadRequest, "缺少 zone_id")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	records, err := cf.ListRecords(r.Context(), zoneID)
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "读取 DNS 记录失败: "+err.Error())
		return
	}
	httpx.OK(w, map[string]any{"records": records})
}

// CreateRecord POST /api/dns/records {zone_id,type,name,content,ttl,proxied,priority?,comment?}
func (s *Service) CreateRecord(w http.ResponseWriter, r *http.Request) {
	var b recordReq
	if err := httpx.ReadJSON(r, &b); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求无效")
		return
	}
	b.ZoneID = strings.TrimSpace(b.ZoneID)
	b.Type = strings.ToUpper(strings.TrimSpace(b.Type))
	b.Name = strings.TrimSpace(b.Name)
	if b.ZoneID == "" || !allowedTypes[b.Type] || b.Name == "" || strings.TrimSpace(b.Content) == "" {
		httpx.Err(w, http.StatusBadRequest, "zone、类型、名称、内容均不能为空，且类型必须合法")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	rec, err := cf.CreateRecord(r.Context(), b.ZoneID, toRecord(b))
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "新建记录失败: "+err.Error())
		return
	}
	httpx.OK(w, map[string]any{"record": rec})
}

// UpdateRecord PUT /api/dns/records/{id}?zone_id= {type,name,content,ttl,proxied,priority?,comment?}
func (s *Service) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	recID := strings.TrimSpace(chi.URLParam(r, "id"))
	zoneID := strings.TrimSpace(r.URL.Query().Get("zone_id"))
	if recID == "" || zoneID == "" {
		httpx.Err(w, http.StatusBadRequest, "缺少记录 id 或 zone_id")
		return
	}
	var b recordReq
	if err := httpx.ReadJSON(r, &b); err != nil {
		httpx.Err(w, http.StatusBadRequest, "请求无效")
		return
	}
	b.Type = strings.ToUpper(strings.TrimSpace(b.Type))
	b.Name = strings.TrimSpace(b.Name)
	if !allowedTypes[b.Type] || b.Name == "" || strings.TrimSpace(b.Content) == "" {
		httpx.Err(w, http.StatusBadRequest, "类型、名称、内容均不能为空，且类型必须合法")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	rec, err := cf.UpdateRecord(r.Context(), zoneID, recID, toRecord(b))
	if err != nil {
		httpx.Err(w, http.StatusBadGateway, "更新记录失败: "+err.Error())
		return
	}
	httpx.OK(w, map[string]any{"record": rec})
}

// DeleteRecord DELETE /api/dns/records/{id}?zone_id=
func (s *Service) DeleteRecord(w http.ResponseWriter, r *http.Request) {
	recID := strings.TrimSpace(chi.URLParam(r, "id"))
	zoneID := strings.TrimSpace(r.URL.Query().Get("zone_id"))
	if recID == "" || zoneID == "" {
		httpx.Err(w, http.StatusBadRequest, "缺少记录 id 或 zone_id")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, noTokenMsg)
		return
	}
	if err := cf.DeleteRecord(r.Context(), zoneID, recID); err != nil {
		httpx.Err(w, http.StatusBadGateway, "删除记录失败: "+err.Error())
		return
	}
	httpx.OK(w, map[string]any{"deleted": true, "id": recID})
}
