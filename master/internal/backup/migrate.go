package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/browserhub"
	"nodepanel/master/internal/cloudflare"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

// cfClient builds a Cloudflare client from the saved 'cloudflare' setting, or
// returns nil when none is configured. The client is cheap to construct; we make
// it on demand at migration time rather than wiring a singleton.
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

// sourceContainerDomains returns the source tunnel ingress hostnames whose
// service port matches one of the migrating container's bound host ports (i.e.
// this container's public domains). Used to decide whether a tunnel-less
// destination would strand the domain. Returns (nil, err) if the source config
// can't be read (caller treats that as "keep source, can't verify").
func sourceContainerDomains(ctx context.Context, cf *cloudflare.Client, srcTunnelID string, srcPorts map[string]string) ([]string, error) {
	if cf == nil || srcTunnelID == "" || len(srcPorts) == 0 {
		return nil, nil
	}
	cfg, err := cf.GetConfig(ctx, srcTunnelID)
	if err != nil {
		return nil, err
	}
	portSet := map[string]bool{}
	for _, hp := range srcPorts {
		portSet[hp] = true
	}
	var out []string
	for _, rule := range cloudflare.IngressFromConfig(cfg) {
		if rule.IsCatchAll() {
			continue
		}
		if portSet[strconv.Itoa(cloudflare.ServicePort(rule.Service()))] {
			if h := rule.Hostname(); h != "" {
				out = append(out, h)
			}
		}
	}
	return out, nil
}

// stageContainerBackup backs up one container to local staging (no remote targets)
// and returns the completed backup row (with manifest). Used by migration as the
// "copy the container's data + config off the source" step.
func (s *Service) stageContainerBackup(ctx context.Context, nodeID, container, actor string) (*store.Backup, error) {
	node, tgts, b, err := s.createBackupJob(ctx, nodeID, nil, container, nil, "migrate-"+container, actor)
	if err != nil {
		return nil, err
	}
	ur, _ := s.runBackup(b, node, tgts, nil, container)
	if !ur.TarOK {
		if b != nil {
			return b, fmt.Errorf("%s", b.Error)
		}
		return nil, fmt.Errorf("%s", ur.Err)
	}
	return b, nil
}

// MigrateJobItem is one container to migrate (from the HTTP request).
type MigrateJobItem struct {
	NodeID      string `json:"node_id"`
	ContainerID string `json:"container_id"`
	Name        string `json:"name"`
}

// Migrate POST /api/containers/migrate {items:[{node_id,container_id,name}], dest_node_id, remove_source}
// Migrates each selected container to the destination node: backs up the source,
// restores+recreates on the destination (auto-remapping any conflicting host
// port), re-points the container's public domain from the source node's tunnel to
// the destination's, then (optionally) removes the source container. One
// MigrateJob per container, streamed live + persisted for history. Requires both
// source and destination agents >= 1.9.0 (tunnel-id reporting + post-recreate
// port reporting), so the domain move is reliable.
func (s *Service) Migrate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items          []MigrateJobItem `json:"items"`
		DestNodeID     string           `json:"dest_node_id"`
		RemoveSource   bool             `json:"remove_source"`
		RenameDomains  bool             `json:"rename_domains"`
		DestBaseDomain string           `json:"dest_base_domain"`
		DomainMap      []DomainMapItem  `json:"domain_map"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.Items) == 0 || body.DestNodeID == "" {
		httpx.Err(w, 400, "items and dest_node_id are required")
		return
	}
	dest, err := s.Store.GetNode(r.Context(), body.DestNodeID)
	if err != nil {
		httpx.Err(w, 404, "目标节点不存在")
		return
	}
	if !s.Hub.Online(dest.ID) {
		httpx.Err(w, 409, "目标节点离线")
		return
	}
	if !versionAtLeast(dest.AgentVersion, 1, 9) {
		httpx.Err(w, 409, fmt.Sprintf("目标节点 agent 版本过低（%s），需 ≥ 1.9.0 才能迁移", dest.AgentVersion))
		return
	}
	actor := auth.UserID(r.Context())
	if actor == "" {
		actor = "admin"
	}
	// Validate every source node once: online + agent >= 1.9.0 + not the destination.
	seen := map[string]bool{}
	for _, it := range body.Items {
		if it.NodeID == dest.ID {
			httpx.Err(w, 400, "不能迁移到容器所在节点")
			return
		}
		if seen[it.NodeID] {
			continue
		}
		seen[it.NodeID] = true
		n, err := s.Store.GetNode(r.Context(), it.NodeID)
		if err != nil {
			httpx.Err(w, 404, "源节点不存在: "+it.NodeID)
			return
		}
		if !s.Hub.Online(n.ID) {
			httpx.Err(w, 409, "源节点离线: "+n.Name)
			return
		}
		if !versionAtLeast(n.AgentVersion, 1, 9) {
			httpx.Err(w, 409, fmt.Sprintf("源节点 %s agent 版本过低（%s），需 ≥ 1.9.0", n.Name, n.AgentVersion))
			return
		}
	}

	jobIDs := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		src, _ := s.Store.GetNode(r.Context(), it.NodeID)
		name := it.Name
		if name == "" {
			name = it.ContainerID
		}
		// Resolve this container's domain rename plan: {src_hostname(lower) → target_hostname}.
		plan := map[string]string{}
		if body.RenameDomains {
			for _, dm := range body.DomainMap {
				if dm.ContainerID == it.ContainerID || strings.HasPrefix(it.ContainerID, dm.ContainerID) || strings.HasPrefix(dm.ContainerID, it.ContainerID) {
					if h := strings.TrimSpace(dm.SrcHostname); h != "" {
						plan[strings.ToLower(h)] = strings.TrimSpace(dm.TargetHostname)
					}
				}
			}
		}
		job := &store.MigrateJob{
			Container: name, SourceNode: src.ID, TargetNode: dest.ID,
			Status: statusRunning, Stage: "backup", AgentVersion: dest.AgentVersion, Actor: actor,
		}
		_ = s.Store.CreateMigrateJob(r.Context(), job)
		s.broadcastMigrateJob(job, "migrate.update")
		jobIDs = append(jobIDs, job.ID)
		go s.runMigrate(job, src, dest, it.ContainerID, body.RemoveSource, body.RenameDomains, plan)
	}
	httpx.OK(w, map[string]any{"started": len(jobIDs), "job_ids": jobIDs})
}

// ListMigrateJobs GET /api/migrate/jobs — recent migration jobs (history), with
// source/target node names resolved.
func (s *Service) ListMigrateJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Store.ListMigrateJobs(r.Context(), 200)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	names := map[string]string{}
	nodeName := func(id string) string {
		if id == "" {
			return ""
		}
		if n, ok := names[id]; ok {
			return n
		}
		nm := shortID(id)
		if n, err := s.Store.GetNode(r.Context(), id); err == nil {
			nm = n.Name
		}
		names[id] = nm
		return nm
	}
	type jobOut struct {
		store.MigrateJob
		SourceNodeName string `json:"source_node_name"`
		TargetNodeName string `json:"target_node_name"`
	}
	out := make([]jobOut, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobOut{MigrateJob: j, SourceNodeName: nodeName(j.SourceNode), TargetNodeName: nodeName(j.TargetNode)})
	}
	httpx.OK(w, out)
}

// DomainMapItem maps one of a container's current public hostnames to the target
// hostname it should become after migration. target == src_hostname (or empty)
// means keep the original name.
type DomainMapItem struct {
	ContainerID    string `json:"container_id"`
	SrcHostname    string `json:"src_hostname"`
	TargetHostname string `json:"target_hostname"`
}

// domainPlanItem is one hostname of one container in the pre-migration plan.
type domainPlanItem struct {
	SrcHostname    string `json:"src_hostname"`
	Prefix         string `json:"prefix,omitempty"`
	TargetHostname string `json:"target_hostname"`
	Available      bool   `json:"available"`
	ConflictReason string `json:"conflict_reason,omitempty"`
	Rename         bool   `json:"rename"` // false ⇒ keep original (no base domain / needs upgrade)
}

type domainPlanContainer struct {
	ContainerID   string           `json:"container_id"`
	ContainerName string           `json:"container_name"`
	NodeID        string           `json:"node_id"`
	TunnelID      string           `json:"tunnel_id,omitempty"`
	Items         []domainPlanItem `json:"items"`
	Error         string           `json:"error,omitempty"`
}

func versionAtLeastPatch(v string, maj, min, patch int) bool {
	var a, b, c int
	if _, err := fmt.Sscanf(v, "%d.%d.%d", &a, &b, &c); err != nil {
		if _, err := fmt.Sscanf(v, "%d.%d", &a, &b); err != nil {
			return false
		}
	}
	if a != maj {
		return a > maj
	}
	if b != min {
		return b > min
	}
	return c >= patch
}

// DomainPlan POST /api/containers/migrate/domain-plan
// {items:[{node_id,container_id,name}], dest_node_id, dest_base_domain?}
// Pre-computes, per selected container, what each of its current public domains
// becomes on the destination node (<prefix>.<dest base domain>), with an
// availability check (dest tunnel ingress + live DNS) so the UI can collect a
// custom subdomain for conflicts. Requires the source agent >= 1.9.2 (so the
// container's host ports are cached and can be matched to its tunnel ingress).
func (s *Service) DomainPlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items          []MigrateJobItem `json:"items"`
		DestNodeID     string           `json:"dest_node_id"`
		DestBaseDomain string           `json:"dest_base_domain"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.Items) == 0 || body.DestNodeID == "" {
		httpx.Err(w, http.StatusBadRequest, "items and dest_node_id are required")
		return
	}
	cf := s.cfClient(r.Context())
	if cf == nil {
		httpx.Err(w, http.StatusBadRequest, "未配置 Cloudflare 令牌，请先到「设置 → Cloudflare」配置 API token")
		return
	}
	ctx := r.Context()
	dest, err := s.Store.GetNode(ctx, body.DestNodeID)
	if err != nil {
		httpx.Err(w, http.StatusNotFound, "目标节点不存在")
		return
	}
	destBase := strings.TrimSpace(body.DestBaseDomain)
	if destBase == "" {
		destBase = strings.TrimSpace(dest.BaseDomain)
	}

	// Destination tunnel hostnames (for the availability check), fetched once.
	destHosts := map[string]bool{}
	if dest.TunnelID != "" {
		if cfg, err := cf.GetConfig(ctx, dest.TunnelID); err == nil {
			for _, rule := range cloudflare.IngressFromConfig(cfg) {
				if h := rule.Hostname(); h != "" {
					destHosts[strings.ToLower(h)] = true
				}
			}
		}
	}
	destHostsList := make([]string, 0, len(destHosts))
	for h := range destHosts {
		destHostsList = append(destHostsList, h)
	}

	srcCfgCache := map[string]map[string]any{} // tunnelID → config (dedupes multi-container nodes)
	plan := make([]domainPlanContainer, 0, len(body.Items))
	var allTargets []string

	for _, it := range body.Items {
		c := domainPlanContainer{ContainerID: it.ContainerID, ContainerName: it.Name, NodeID: it.NodeID}
		src, err := s.Store.GetNode(ctx, it.NodeID)
		if err != nil {
			c.Error = "源节点不存在"
			plan = append(plan, c)
			continue
		}
		if src.TunnelID == "" {
			c.Error = "源节点未检测到 CF Tunnel（agent ≥ 1.9.0 且本机运行 cloudflared）"
			plan = append(plan, c)
			continue
		}
		c.TunnelID = src.TunnelID
		if !versionAtLeastPatch(src.AgentVersion, 1, 9, 2) {
			c.Error = "源节点 agent 版本过低（需 ≥ 1.9.2 才能上报容器端口并做域名预检）"
			plan = append(plan, c)
			continue
		}

		// Container's published host ports (cached from agent >= 1.9.2 inventory).
		var hostPorts []int
		foundContainer := false
		if cols, err := s.Store.ContainersByNode(ctx, src.ID); err == nil {
			for _, cc := range cols {
				if cc.ContainerID == it.ContainerID || strings.HasPrefix(cc.ContainerID, it.ContainerID) || strings.HasPrefix(it.ContainerID, cc.ContainerID) {
					foundContainer = true
					hostPorts = cc.HostPorts
					break
				}
			}
		}
		if !foundContainer {
			c.Error = "未找到容器库存（请等待 agent 刷新容器列表后重试）"
			plan = append(plan, c)
			continue
		}
		if len(hostPorts) == 0 {
			plan = append(plan, c)
			continue
		}

		srcCfg, ok := srcCfgCache[src.TunnelID]
		if !ok {
			srcCfg, _ = cf.GetConfig(ctx, src.TunnelID)
			srcCfgCache[src.TunnelID] = srcCfg
		}
		if srcCfg == nil {
			c.Error = "读取源 tunnel 配置失败"
			plan = append(plan, c)
			continue
		}

		portSet := map[int]bool{}
		for _, p := range hostPorts {
			portSet[p] = true
		}
		for _, rule := range cloudflare.IngressFromConfig(srcCfg) {
			if rule.IsCatchAll() {
				continue
			}
			if !portSet[cloudflare.ServicePort(rule.Service())] {
				continue
			}
			h := rule.Hostname()
			if h == "" {
				continue
			}
			item := domainPlanItem{SrcHostname: h, Available: true}
			if destBase == "" {
				item.TargetHostname = h
				item.ConflictReason = "目标节点未配主域名，将保留原域名"
			} else {
				prefix := h
				if zone, zerr := cf.ZoneNameFor(ctx, h); zerr == nil && zone != "" && strings.HasSuffix(strings.ToLower(h), "."+strings.ToLower(zone)) {
					prefix = h[:len(h)-len(zone)-1]
				}
				if prefix == "" {
					prefix = h // degenerate; keep whole
				}
				item.Prefix = prefix
				item.Rename = true
				item.TargetHostname = prefix + "." + destBase
				allTargets = append(allTargets, item.TargetHostname)
			}
			c.Items = append(c.Items, item)
		}
		plan = append(plan, c)
	}

	// Availability: a target conflicts if the dest tunnel already has it OR a DNS
	// CNAME for it already exists.
	dnsMap, _ := cf.ResolveCNAMEs(ctx, allTargets)
	for i := range plan {
		for j := range plan[i].Items {
			it := &plan[i].Items[j]
			if !it.Rename {
				continue
			}
			t := strings.ToLower(it.TargetHostname)
			reason := ""
			if destHosts[t] {
				reason = "目标 tunnel 已有该域名"
			}
			if reason == "" {
				if info, ok := dnsMap[t]; ok && info.Content != "" {
					reason = "DNS 已有 CNAME：" + info.Content
				}
			}
			if reason != "" {
				it.Available = false
				it.ConflictReason = reason
			}
		}
	}

	httpx.OK(w, map[string]any{
		"dest_base_domain":     destBase,
		"dest_base_domain_set": destBase != "",
		"dest_hostnames":       destHostsList,
		"plan":                 plan,
	})
}

// moveDomains moves a container's public hostname(s) from the source tunnel to
// the destination, optionally RENAMING each to a target hostname (the migrated
// "rename" mode: a.a.com → a.<dest base>). plan maps each src hostname (lower)
// to its target; a target equal to the src hostname (or absent) keeps the
// original name. Order + rollback mirror maybeMoveDomain: add dest ingress →
// set DNS → remove src ingress; rename additionally deletes the old hostname's
// DNS CNAME. Returns (note, ok, hadDomain, movedHostnames) like maybeMoveDomain.
func (s *Service) moveDomains(ctx context.Context, src, dest *store.Node, b *store.Backup, result proto.RestoreResult, plan map[string]string) (note string, ok bool, hadDomain bool, moved []string) {
	cf := s.cfClient(ctx)
	if cf == nil {
		return "未配置 Cloudflare：仅迁移容器/数据，未切换域名（保留源容器以免域名断开）", true, false, nil
	}
	// External-line (NPM) source: its public domains live outside CF (we can't see
	// or move them). Keep the source so an NPM-served domain isn't stranded. Key
	// off declared IngressType, not TunnelID — agents leave a stale id on stop.
	if !src.SupportsCFDomain() {
		return "源节点为外部线路（NPM），公网域名不在 CF 隧道管理范围，未切换；已保留源容器（如该域名指向此容器，请在 NPM 手动调整）", false, true, nil
	}
	srcPorts := readSnapshotPortMap(b.StagePath)
	if src.TunnelID == "" {
		return "源节点无 CF Tunnel，容器无公网域名需切换", true, false, nil
	}
	if !dest.SupportsCFDomain() || dest.TunnelID == "" {
		// Destination can't receive the domain — declared external-line (NPM) or no
		// live tunnel. Only delete the source if it has no public domain; otherwise
		// the domain strands on the source (the bug that stranded ai.example.com).
		// NB: IngressType is authoritative here — agents leave a stale tunnel id on
		// cloudflared stop, so TunnelID alone would miss an NPM node like dama.
		hostnames, derr := sourceContainerDomains(ctx, cf, src.TunnelID, srcPorts)
		switch {
		case derr != nil:
			return "目标节点为外部线路（NPM）/无 CF Tunnel，读取源域名失败(" + derr.Error() + ")；已保留源容器", false, true, nil
		case len(hostnames) > 0:
			return fmt.Sprintf("目标节点为外部线路（NPM）/无 CF Tunnel，源容器有公网域名 %s；已保留源容器（域名跟随仅支持 CF 隧道节点）", strings.Join(hostnames, ", ")), false, true, hostnames
		default:
			return "目标节点为外部线路（NPM）/无 CF Tunnel，源容器无公网域名，无需切换", true, false, nil
		}
	}
	if len(srcPorts) == 0 {
		return "无法读取源容器端口映射（快照缺失），跳过域名切换（保留源容器）", true, false, nil
	}
	dstPorts := result.Ports
	if len(dstPorts) == 0 {
		return "目标 agent 未回传容器端口（需 ≥ 1.9.0），跳过域名切换（保留源容器）", true, false, nil
	}

	srcCfg, err := cf.GetConfig(ctx, src.TunnelID)
	if err != nil {
		return "读取源 tunnel 配置失败: " + err.Error(), false, true, nil
	}
	srcIngress := cloudflare.IngressFromConfig(srcCfg)

	// src host port → dest host port, aligned by containerPort/proto key.
	hostPorts := map[string]bool{}
	for _, hp := range srcPorts {
		hostPorts[hp] = true
	}
	dstBySrcPort := map[int]int{}
	for cpKey, srcHost := range srcPorts {
		shp, _ := strconv.Atoi(srcHost)
		if shp == 0 {
			continue
		}
		for cpKey2, bindings := range dstPorts {
			if cpKey2 != cpKey || len(bindings) == 0 {
				continue
			}
			dhp, _ := strconv.Atoi(bindings[0].HostPort)
			if dhp != 0 {
				dstBySrcPort[shp] = dhp
			}
		}
	}

	type moveItem struct {
		rule       cloudflare.IngressRule
		srcHost    string
		targetHost string // effective hostname on dest (renamed or kept)
		rename     bool
	}
	var items []moveItem
	var skipped []string
	for _, rule := range srcIngress {
		if rule.IsCatchAll() {
			continue
		}
		shp := cloudflare.ServicePort(rule.Service())
		if !hostPorts[strconv.Itoa(shp)] {
			continue
		}
		dhp, aligned := dstBySrcPort[shp]
		if !aligned {
			skipped = append(skipped, rule.Hostname()+": 无法对齐目标端口，保留")
			continue
		}
		h := rule.Hostname()
		target := plan[strings.ToLower(h)]
		rename := target != "" && !strings.EqualFold(target, h)
		eff := h
		if rename {
			eff = target
		}
		// Rewrite the preserved rule's service to the destination port; copy the
		// whole map so originRequest etc. survive, then set hostname (possibly
		// renamed) + service.
		cp := cloudflare.IngressRule{}
		for k, v := range rule {
			cp[k] = v
		}
		cp["hostname"] = eff
		cp["service"] = cloudflare.RewriteServicePort(rule.Service(), dhp)
		items = append(items, moveItem{rule: cp, srcHost: h, targetHost: eff, rename: rename})
	}
	if len(items) == 0 {
		if len(skipped) > 0 {
			return "无可移动的域名规则；" + strings.Join(skipped, ", "), true, false, nil
		}
		return "未发现公网域名（该容器未通过 tunnel 暴露），无需切换", true, false, nil
	}

	// 1. Add the (renamed) rules to the destination tunnel before the catch-all,
	//    replacing any pre-existing rule for the same target hostname.
	destCfg, err := cf.GetConfig(ctx, dest.TunnelID)
	if err != nil {
		return "读取目标 tunnel 配置失败: " + err.Error(), false, true, nil
	}
	destIng := cloudflare.IngressFromConfig(destCfg)
	existing := map[string]bool{}
	for _, it := range items {
		existing[strings.ToLower(it.targetHost)] = true
	}
	var keep, catchAll []cloudflare.IngressRule
	for _, r := range destIng {
		if r.IsCatchAll() {
			catchAll = append(catchAll, r)
		} else if !existing[strings.ToLower(r.Hostname())] {
			keep = append(keep, r)
		}
	}
	for _, it := range items {
		keep = append(keep, it.rule)
	}
	destCfg["ingress"] = ingressToAny(append(keep, catchAll...))
	if err := cf.PutConfig(ctx, dest.TunnelID, destCfg); err != nil {
		return "写入目标 tunnel ingress 失败: " + err.Error(), false, true, nil
	}

	// 2. Set DNS per item (create/update for rename, repoint for keep). On failure
	//    roll back the destination ingress we just added.
	movedHosts := []string{}
	for _, it := range items {
		var derr error
		if it.rename {
			_, derr = cf.CreateOrUpdateCNAME(ctx, it.targetHost, dest.TunnelID)
		} else {
			_, derr = cf.RepointCNAME(ctx, it.srcHost, dest.TunnelID)
		}
		if derr != nil {
			if cfg2, _ := cf.GetConfig(ctx, dest.TunnelID); cfg2 != nil {
				drop := map[string]bool{strings.ToLower(it.targetHost): true}
				cfg2["ingress"] = ingressToAny(stripHostnames(cloudflare.IngressFromConfig(cfg2), drop))
				_ = cf.PutConfig(ctx, dest.TunnelID, cfg2)
			}
			return fmt.Sprintf("DNS 切换失败 (%s): %s；已回滚目标 ingress，源容器保留", it.targetHost, derr.Error()), false, true, movedHosts
		}
		movedHosts = append(movedHosts, it.targetHost)
	}

	// 3. Remove the source rules (non-fatal) and, for renamed hosts, delete the
	//    old DNS CNAME so the stale hostname stops resolving.
	srcDrop := map[string]bool{}
	for _, it := range items {
		srcDrop[strings.ToLower(it.srcHost)] = true
	}
	srcCfg["ingress"] = ingressToAny(stripHostnames(srcIngress, srcDrop))
	srcPutErr := cf.PutConfig(ctx, src.TunnelID, srcCfg)
	for _, it := range items {
		if it.rename {
			_ = cf.DeleteCNAME(ctx, it.srcHost)
		}
	}

	note = fmt.Sprintf("域名已从 %s 切到 %s：%s", src.Name, dest.Name, strings.Join(movedHosts, ", "))
	if len(skipped) > 0 {
		note += "（" + strings.Join(skipped, ", ") + "）"
	}
	if srcPutErr != nil {
		note += "；域名已切到目标节点，但清理源 tunnel ingress 失败: " + srcPutErr.Error() + "（不影响访问）"
	}
	return note, true, true, movedHosts
}

// runMigrate runs the full migration pipeline for one container. Every stage
// updates the job and broadcasts; the source container is removed only when the
// restore succeeded AND the domain step succeeded (or the container had no
// domain). Any failure leaves the source intact so the operator can re-run.
func (s *Service) runMigrate(job *store.MigrateJob, src, dest *store.Node, containerID string, removeSource, renameDomains bool, plan map[string]string) {
	ctx := context.Background()
	finalize := func(status, detail, errStr string) {
		job.Status = status
		job.Stage = ""
		if detail != "" {
			job.Detail = detail
		}
		if errStr != "" {
			job.Error = errStr
		}
		job.FinishedAt = time.Now().Unix()
		_ = s.Store.UpdateMigrateJob(ctx, job)
		s.broadcastMigrateJob(job, "migrate.update")
		s.Browser.Broadcast(browserhub.NewOut("migrate.jobs", nil))
	}
	setStage := func(stage, detail string) {
		job.Stage = stage
		if detail != "" {
			job.Detail = detail
		}
		_ = s.Store.UpdateMigrateJobProgress(ctx, job.ID, stage, job.BytesDone, job.BytesTotal, job.Percent)
		job.Detail = detail
		s.broadcastMigrateJob(job, "migrate.progress")
	}

	// 1. Backup the source container (data volumes + config snapshot) to staging.
	setStage("backup", "备份源容器 "+job.Container)
	b, err := s.stageContainerBackup(ctx, src.ID, containerID, job.Actor)
	if err != nil || b == nil || b.Status != "ok" {
		finalize(statusFailed, "", "备份源容器失败: "+errStrOf(err, "归档未生成"))
		return
	}
	job.BackupID = b.ID
	job.Image = imageFromManifest(b.Manifest)

	// 2. Preflight the destination (disk space; port conflicts are advisory —
	// the recreate remaps them automatically).
	pfNote, hardErr := s.preflightDest(ctx, dest, b.Manifest)
	if hardErr != nil {
		finalize(statusFailed, "", "目标节点不可用: "+hardErr.Error())
		return
	}
	setStage("preflight", strings.TrimSpace(pfNote))

	// 3. Restore + recreate on the destination (auto-pull, port remap, bridge
	// fallback — all handled by the agent's recreateContainer).
	setStage("restore", "恢复并重建到目标节点 "+dest.Name)
	result, timedOut := s.restoreToDest(ctx, b, dest, job)
	if timedOut {
		finalize(statusFailed, "", "恢复超时（2h）")
		return
	}
	if !result.OK {
		finalize(statusFailed, result.Detail, result.Err)
		return
	}
	// OK=true ⇒ the container is running on the destination (newly recreated, or
	// already present from a prior partial run). result.Ports (agents >= 1.9.0)
	// tells us the actual host ports it bound — needed for the domain move.

	// 4. Re-point the container's public domain from src tunnel → dest tunnel.
	//    renameDomains uses moveDomains (a.a.com → a.<dest base>, old DNS cleaned);
	//    otherwise fall back to maybeMoveDomain (keep hostname, repoint DNS).
	setStage("domain", "切换公网域名")
	var domainNote string
	var domainOK, hadDomain bool
	var movedHosts []string
	if renameDomains {
		domainNote, domainOK, hadDomain, movedHosts = s.moveDomains(ctx, src, dest, b, result, plan)
	} else {
		domainNote, domainOK, hadDomain, movedHosts = s.maybeMoveDomain(ctx, src, dest, b, result)
	}
	job.Domains = strings.Join(movedHosts, ", ")
	job.DomainMoved = domainOK && hadDomain
	if hadDomain && !domainOK {
		// Domain move failed while a domain existed: do NOT remove the source,
		// or the public domain would break. Mark partial so the operator fixes CF.
		job.Detail = domainNote
		finalize(statusPartial, domainNote, "域名切换失败；源容器已保留，修复 Cloudflare 后重试")
		return
	}
	if domainNote != "" {
		job.Detail = domainNote
	}

	// 5. Remove the source container (only after the destination is serving and
	// the domain — if any — has moved). Backup row is retained for rollback.
	if removeSource {
		setStage("cleanup", "删除源容器 "+job.Container)
		if err := s.deleteContainer(ctx, src, containerID); err != nil {
			// Migration itself succeeded; a failed source cleanup is non-fatal but
			// surfaced. Source remains for manual removal.
			job.SourceRemoved = false
			finalize(statusOK, (job.Detail + "；⚠️ 源容器删除失败: " + err.Error() + "（可手动删除）"), "")
			return
		}
		job.SourceRemoved = true
	} else {
		job.Detail = (job.Detail + "；已保留源容器")
	}
	finalize(statusOK, job.Detail, "")
}

func errStrOf(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// preflightDest asks the destination agent to feasibility-check the container's
// footprint (bound ports, disk, image) without touching data. Port conflicts are
// advisory (returned as a human note — recreate remaps them); insufficient disk
// is a hard error. Any agent communication failure is treated as non-fatal (the
// recreate is the source of truth) and returns an empty note.
func (s *Service) preflightDest(ctx context.Context, dest *store.Node, manifestJSON string) (note string, hardErr error) {
	if manifestJSON == "" {
		return "", nil
	}
	var m proto.BackupManifest
	if json.Unmarshal([]byte(manifestJSON), &m) != nil {
		return "", nil
	}
	items := []proto.PreflightItem{}
	items = append(items, m.Ports...)
	items = append(items, m.Binds...)
	if m.Image != "" || m.Size > 0 {
		items = append(items, proto.PreflightItem{Image: m.Image, Size: m.Size})
	}
	if len(items) == 0 {
		return "", nil
	}
	reqID := "mpreflight:" + dest.ID + ":" + uuid.NewString()
	env, _ := proto.Encode(proto.MsgRestorePreflight, reqID, proto.RestorePreflightRequest{Items: items})
	msg, _ := s.Hub.RequestOne(dest.ID, env, 15*time.Second)
	if msg == nil || len(msg.Data) == 0 {
		return "", nil // agent didn't answer; recreate is authoritative anyway
	}
	var pf proto.PreflightResult
	_ = json.Unmarshal(msg.Data, &pf)
	var notes []string
	for _, c := range pf.PortConflicts {
		notes = append(notes, fmt.Sprintf("端口 %s/%s 在目标被占用（将自动改用空闲端口）", c.HostPort, c.Proto))
	}
	if pf.DiskRequired > 0 && pf.DiskAvailable > 0 && pf.DiskRequired > int64(float64(pf.DiskAvailable)*0.95) {
		return strings.Join(notes, "；"), fmt.Errorf("磁盘空间不足：需要约 %s，可用 %s", humanSize(pf.DiskRequired), humanSize(pf.DiskAvailable))
	}
	return strings.Join(notes, "；"), nil
}

// restoreToDest sends the recreate-restore to the destination and drains the
// streamed progress + terminal result into the migrate job. Mirrors the restore
// path (Recreate=true, AutoPull=true) but writes progress as migrate events.
func (s *Service) restoreToDest(ctx context.Context, b *store.Backup, dest *store.Node, job *store.MigrateJob) (proto.RestoreResult, bool) {
	if err := s.ensureStaged(ctx, b); err != nil {
		return proto.RestoreResult{OK: false, Err: "归档暂存不可用: " + err.Error()}, false
	}
	reqID := "migrate:" + b.ID + ":" + dest.ID
	ch := s.Hub.Subscribe(reqID)
	dest2 := "/tmp/np-migrate-" + b.ID
	download := s.baseURL() + "/api/agent/dl?id=" + b.ID + "&token=" + url.QueryEscape(dest.AgentToken)
	env, _ := proto.Encode(proto.MsgRestore, reqID, proto.RestoreRequest{
		Download: download, Token: dest.AgentToken, Dest: dest2, Recreate: true, AutoPull: true,
	})
	if err := s.Hub.Send(dest.ID, env); err != nil {
		s.Hub.Unsubscribe(reqID)
		return proto.RestoreResult{OK: false, Err: "目标节点不可达: " + err.Error()}, false
	}
	defer s.Hub.Unsubscribe(reqID)
	timeout := time.NewTimer(2 * time.Hour)
	defer timeout.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return proto.RestoreResult{OK: false, Err: "agent disconnected"}, false
			}
			switch msg.Type {
			case proto.MsgRestoreProgress:
				var p proto.RestoreProgress
				if len(msg.Data) > 0 {
					_ = json.Unmarshal(msg.Data, &p)
				}
				_ = s.Store.UpdateMigrateJobProgress(ctx, job.ID, "restore", p.BytesDone, p.BytesTotal, int64(p.Percent))
				job.BytesDone, job.BytesTotal, job.Percent = p.BytesDone, p.BytesTotal, int64(p.Percent)
				s.broadcastMigrateJob(job, "migrate.progress")
			case proto.MsgRestoreResult:
				var res proto.RestoreResult
				if len(msg.Data) > 0 {
					_ = json.Unmarshal(msg.Data, &res)
				}
				return res, false
			}
		case <-timeout.C:
			return proto.RestoreResult{}, true
		}
	}
}

// deleteContainer removes the source container (docker rm -f -v) via the existing
// container-op path.
func (s *Service) deleteContainer(ctx context.Context, node *store.Node, containerID string) error {
	reqID := "migratedel:" + node.ID + ":" + time.Now().Format("150405.000000")
	ch := s.Hub.Subscribe(reqID)
	env, _ := proto.Encode(proto.MsgContainerOp, reqID, proto.ContainerOpRequest{Action: "delete", IDs: []string{containerID}})
	if err := s.Hub.Send(node.ID, env); err != nil {
		s.Hub.Unsubscribe(reqID)
		return err
	}
	defer s.Hub.Unsubscribe(reqID)
	select {
	case msg, ok := <-ch:
		if !ok {
			return fmt.Errorf("agent disconnected")
		}
		var res proto.ContainerResult
		if len(msg.Data) > 0 {
			_ = json.Unmarshal(msg.Data, &res)
		}
		// delete returns OK=true with the container in details even if already gone.
		if !res.OK && res.Err != "" {
			return fmt.Errorf("%s", res.Err)
		}
		return nil
	case <-time.After(60 * time.Second):
		return fmt.Errorf("删除超时")
	}
}

// maybeMoveDomain discovers the source container's public hostname(s) from the
// source tunnel's ingress (by matching the container's bound host ports) and, if
// Cloudflare is configured and both nodes report a tunnel id, moves those ingress
// rules to the destination tunnel (service rewritten to the destination's actual
// port) and repoints DNS. Returns (note, ok, hadDomain, movedHostnames). The note
// folds in any port remap; the caller stores it on the job.
//
// When Cloudflare is not configured, or either node has no tunnel id, the domain
// is left untouched (hadDomain=false from our perspective) so the caller keeps the
// source — a safe degraded "copy" mode.
func (s *Service) maybeMoveDomain(ctx context.Context, src, dest *store.Node, b *store.Backup, result proto.RestoreResult) (note string, ok bool, hadDomain bool, moved []string) {
	cf := s.cfClient(ctx)
	if cf == nil {
		return "未配置 Cloudflare：仅迁移容器/数据，未切换域名（保留源容器以免域名断开）", true, false, nil
	}
	// External-line (NPM) source: its public domains live outside CF (we can't see
	// or move them). Keep the source so an NPM-served domain isn't stranded. Key
	// off declared IngressType, not TunnelID — agents leave a stale id on stop.
	if !src.SupportsCFDomain() {
		return "源节点为外部线路（NPM），公网域名不在 CF 隧道管理范围，未切换；已保留源容器（如该域名指向此容器，请在 NPM 手动调整）", false, true, nil
	}
	// Source container's containerPort/proto → host port, from the backup snapshot.
	srcPorts := readSnapshotPortMap(b.StagePath)
	if src.TunnelID == "" {
		return "源节点无 CF Tunnel，容器无公网域名需切换", true, false, nil
	}
	if !dest.SupportsCFDomain() || dest.TunnelID == "" {
		// Destination can't receive the domain — declared external-line (NPM) or no
		// live tunnel. Only delete the source if it has no public domain; otherwise
		// the domain strands on the source (the bug that stranded ai.example.com).
		// NB: IngressType is authoritative here — agents leave a stale tunnel id on
		// cloudflared stop, so TunnelID alone would miss an NPM node like dama.
		hostnames, derr := sourceContainerDomains(ctx, cf, src.TunnelID, srcPorts)
		switch {
		case derr != nil:
			return "目标节点为外部线路（NPM）/无 CF Tunnel，读取源域名失败(" + derr.Error() + ")；已保留源容器", false, true, nil
		case len(hostnames) > 0:
			return fmt.Sprintf("目标节点为外部线路（NPM）/无 CF Tunnel，源容器有公网域名 %s；已保留源容器（域名跟随仅支持 CF 隧道节点）", strings.Join(hostnames, ", ")), false, true, hostnames
		default:
			return "目标节点为外部线路（NPM）/无 CF Tunnel，源容器无公网域名，无需切换", true, false, nil
		}
	}
	if len(srcPorts) == 0 {
		return "无法读取源容器端口映射（快照缺失），跳过域名切换（保留源容器）", true, false, nil
	}
	// Destination container's containerPort/proto → host ports, from the recreate result.
	dstPorts := result.Ports
	if len(dstPorts) == 0 {
		return "目标 agent 未回传容器端口（需 ≥ 1.9.0），跳过域名切换（保留源容器）", true, false, nil
	}

	// Discover ingress rules on the source tunnel whose service points at one of
	// the source container's host ports. Those hostnames are the container's domain.
	srcCfg, err := cf.GetConfig(ctx, src.TunnelID)
	if err != nil {
		return "读取源 tunnel 配置失败: " + err.Error(), false, true, nil
	}
	srcIngress := cloudflare.IngressFromConfig(srcCfg)
	hostPorts := map[string]bool{}
	for _, hp := range srcPorts {
		hostPorts[hp] = true
	}
	var matched []cloudflare.IngressRule
	for _, rule := range srcIngress {
		if rule.IsCatchAll() {
			continue
		}
		if hostPorts[strconv.Itoa(cloudflare.ServicePort(rule.Service()))] {
			matched = append(matched, rule)
		}
	}
	if len(matched) == 0 {
		// No public hostname fronts this container — nothing to move.
		return "未发现公网域名（该容器未通过 tunnel 暴露），无需切换", true, false, nil
	}

	// Align source host port → destination host port by containerPort/proto key.
	// For each matched rule, find the containerPort whose source host port equals
	// the rule's service port, then look up that containerPort in the dest map.
	dstBySrcPort := map[int]int{} // srcHostPort → dstHostPort
	for cpKey, srcHost := range srcPorts {
		shp, _ := strconv.Atoi(srcHost)
		if shp == 0 {
			continue
		}
		for cpKey2, bindings := range dstPorts {
			if cpKey2 != cpKey || len(bindings) == 0 {
				continue
			}
			dhp, _ := strconv.Atoi(bindings[0].HostPort)
			if dhp != 0 {
				dstBySrcPort[shp] = dhp
			}
		}
	}

	// Move each matched rule: rewrite its service to the destination port, add to
	// the destination tunnel (repoint DNS), then remove from the source tunnel.
	var remaps, movedHosts []string
	rulesByHost := map[string][]cloudflare.IngressRule{}
	for _, rule := range matched {
		h := rule.Hostname()
		shp := cloudflare.ServicePort(rule.Service())
		dhp, ok2 := dstBySrcPort[shp]
		if !ok2 {
			// Couldn't align this port (dest didn't bind the matching container port);
			// skip this rule rather than guess — its hostname stays on the source.
			remaps = append(remaps, fmt.Sprintf("%s: 无法对齐目标端口，保留", h))
			continue
		}
		movedHosts = append(movedHosts, h)
		if dhp != shp {
			remaps = append(remaps, fmt.Sprintf("%s: %d→%d", h, shp, dhp))
		}
		cp := cloudflare.IngressRule{}
		for k, v := range rule {
			cp[k] = v
		}
		cp.SetService(cloudflare.RewriteServicePort(rule.Service(), dhp))
		rulesByHost[h] = append(rulesByHost[h], cp)
	}

	if len(movedHosts) == 0 {
		return "无可移动的域名规则（端口对齐失败）；保留源容器", true, false, nil
	}

	// 1. Add the rules to the destination tunnel (before the catch-all), replacing
	// any pre-existing rule for the same hostname.
	dstCfg, err := cf.GetConfig(ctx, dest.TunnelID)
	if err != nil {
		return "读取目标 tunnel 配置失败: " + err.Error(), false, true, movedHosts
	}
	dstIngress := cloudflare.IngressFromConfig(dstCfg)
	// drop any existing rule whose hostname we're adding (replace semantics)
	existing := map[string]bool{}
	for _, r := range rulesByHost {
		existing[r[0].Hostname()] = true
	}
	pruned := make([]cloudflare.IngressRule, 0, len(dstIngress)+len(matched))
	for _, r := range dstIngress {
		if !r.IsCatchAll() && existing[r.Hostname()] {
			continue
		}
		pruned = append(pruned, r)
	}
	// insert new rules before the catch-all
	inserted := make([]cloudflare.IngressRule, 0, len(pruned)+len(matched))
	catchAll := []cloudflare.IngressRule{}
	for _, r := range pruned {
		if r.IsCatchAll() {
			catchAll = append(catchAll, r)
		} else {
			inserted = append(inserted, r)
		}
	}
	for _, rules := range rulesByHost {
		inserted = append(inserted, rules...)
	}
	inserted = append(inserted, catchAll...)
	// write back preserving the rest of dstCfg
	dstCfg["ingress"] = ingressToAny(inserted)
	if err := cf.PutConfig(ctx, dest.TunnelID, dstCfg); err != nil {
		return "写入目标 tunnel ingress 失败: " + err.Error(), false, true, movedHosts
	}

	// 2. Repoint each hostname's DNS CNAME to the destination tunnel.
	for h := range rulesByHost {
		if _, err := cf.RepointCNAME(ctx, h, dest.TunnelID); err != nil {
			// DNS repoint failed: we already added the dest ingress. Roll back the
			// dest ingress addition so we don't leave a stale duplicate, and abort.
			dstCfg2, _ := cf.GetConfig(ctx, dest.TunnelID)
			if dstCfg2 != nil {
				dstCfg2["ingress"] = ingressToAny(stripHostnames(cloudflare.IngressFromConfig(dstCfg2), map[string]bool{h: true}))
				_ = cf.PutConfig(ctx, dest.TunnelID, dstCfg2)
			}
			return fmt.Sprintf("DNS 切换失败 (%s): %s；已回滚目标 ingress，源容器保留", h, err.Error()), false, true, movedHosts
		}
	}

	// 3. Remove the moved hostnames' rules from the source tunnel.
	srcCfg["ingress"] = ingressToAny(stripHostnames(srcIngress, existing))
	if err := cf.PutConfig(ctx, src.TunnelID, srcCfg); err != nil {
		// Non-fatal: DNS already points to dest, so the leftover source rule is inert.
		return "域名已切到目标节点，但清理源 tunnel ingress 失败: " + err.Error() + "（不影响访问）", true, true, movedHosts
	}

	note = fmt.Sprintf("域名已从 %s 切到 %s：%s", src.Name, dest.Name, strings.Join(movedHosts, ", "))
	if len(remaps) > 0 {
		note += "（端口 " + strings.Join(remaps, ", ") + "）"
	}
	return note, true, true, movedHosts
}

// ingressToAny converts []IngressRule to []any for JSON marshalling back into a config map.
func ingressToAny(rules []cloudflare.IngressRule) []any {
	out := make([]any, 0, len(rules))
	for _, r := range rules {
		out = append(out, map[string]any(r))
	}
	return out
}

// stripHostnames returns ingress minus non-catch-all rules whose hostname is in drop.
func stripHostnames(ingress []cloudflare.IngressRule, drop map[string]bool) []cloudflare.IngressRule {
	out := make([]cloudflare.IngressRule, 0, len(ingress))
	for _, r := range ingress {
		if !r.IsCatchAll() && drop[r.Hostname()] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// readSnapshotPortMap opens a backup archive, reads its first `container.json`
// member, and returns the container's PortBindings as containerPort/proto → host
// port (first binding). Used to align source host ports with the destination's
// (possibly remapped) host ports when moving a domain. Only the first tar entry
// is read, so cost is independent of archive size.
func readSnapshotPortMap(stagePath string) map[string]string {
	f, err := os.Open(stagePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return nil
		}
		if hdr.Name != "container.json" {
			// skip past this entry without reading it fully
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil
			}
			continue
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return nil
		}
		var snap struct {
			HostConfig json.RawMessage `json:"host_config"`
		}
		if json.Unmarshal(buf, &snap) != nil {
			return nil
		}
		var hc struct {
			PortBindings map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		}
		if json.Unmarshal(snap.HostConfig, &hc) != nil {
			return nil
		}
		out := map[string]string{}
		for k, bs := range hc.PortBindings {
			if len(bs) > 0 && bs[0].HostPort != "" && bs[0].HostPort != "0" {
				out[k] = bs[0].HostPort
			}
		}
		return out
	}
}

// broadcastMigrateJob emits a migrate job state event keyed by job_id (streaming
// progress + terminal state). Mirrors broadcastRestoreJob.
func (s *Service) broadcastMigrateJob(job *store.MigrateJob, event string) {
	s.Browser.Broadcast(browserhub.NewOut(event, map[string]any{
		"job_id": job.ID, "container": job.Container, "image": job.Image,
		"source_node": job.SourceNode, "target_node": job.TargetNode,
		"backup_id": job.BackupID, "status": job.Status, "stage": job.Stage,
		"detail": job.Detail, "error": job.Error, "domains": job.Domains,
		"ports_remapped": job.PortsRemapped, "domain_moved": job.DomainMoved,
		"source_removed": job.SourceRemoved, "bytes_total": job.BytesTotal,
		"bytes_done": job.BytesDone, "percent": job.Percent,
		"agent_version": job.AgentVersion, "started_at": job.StartedAt, "finished_at": job.FinishedAt,
	}))
}
