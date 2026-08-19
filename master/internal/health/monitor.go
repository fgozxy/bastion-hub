package health

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"nodepanel/master/internal/store"
)

// pollerCfg is the 'health_monitor' setting: interval + on/off.
type pollerCfg struct {
	Enabled  bool `json:"enabled"`
	Interval int  `json:"interval_seconds"`
}

func (s *Service) loadPollerCfg() pollerCfg {
	raw, _ := s.Store.GetSetting(context.Background(), "health_monitor")
	var c pollerCfg
	configured := raw != ""
	if configured {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	// Default ON once the feature is deployed, so installing Netdata "just
	// works". The loop no-ops when no nodes are enabled, so there is no
	// overhead on panels that never enroll a node.
	if !configured {
		c.Enabled = true
	}
	if c.Interval < 10 {
		c.Interval = 15
	}
	return c
}

// loop is the poller: for each enabled+online node, fetch Netdata allmetrics,
// cache the sample, and evaluate that node's alert rules.
func (s *Service) loop() {
	for {
		c := s.loadPollerCfg()
		if !c.Enabled {
			time.Sleep(60 * time.Second) // disabled — re-check periodically
			continue
		}
		s.pollOnce()
		time.Sleep(time.Duration(c.Interval) * time.Second)
	}
}

func (s *Service) pollOnce() {
	ctx := context.Background()
	hnodes, err := s.Store.ListHealthNodes(ctx)
	if err != nil {
		return
	}
	if len(hnodes) == 0 {
		return
	}
	tmpl := s.loadTemplate()
	enabled := tmpl.enabledSet()
	// Node-name lookup for alert messages.
	nameOf := map[string]string{}
	if nodes, err := s.Store.ListNodes(ctx); err == nil {
		for _, n := range nodes {
			nameOf[n.ID] = n.Name
		}
	}
	alertsByNode := map[string][]store.HealthAlert{}
	all, err := s.Store.ListAllEnabledHealthAlerts(ctx)
	if err == nil {
		for _, a := range all {
			alertsByNode[a.NodeID] = append(alertsByNode[a.NodeID], a)
		}
	}

	for _, hn := range hnodes {
		if !hn.Enabled || !s.Hub.Online(hn.NodeID) {
			continue
		}
		sample, ok := s.fetchSample(ctx, hn.NodeID, enabled)
		if !ok {
			continue
		}
		sample.Cores = hn.Cores
		s.cache.Put(hn.NodeID, sample)
		s.evaluate(ctx, hn.NodeID, nameOf[hn.NodeID], sample, alertsByNode[hn.NodeID])
	}
}

// fetchSample pulls only the charts the template has enabled from each node's
// local Netdata, concurrently, and decodes a Sample.
//
// We deliberately do NOT use /api/v1/allmetrics: that bundle includes every
// chart on the host (one per container/disk/net iface), so on container-heavy
// nodes it exceeds the agent's 512KB MsgHTTPFetch cap, gets truncated mid-JSON,
// and json.Unmarshal then fails wholesale — silently dropping ALL metrics for
// busy nodes. Fetching the few system charts we actually render returns a few
// hundred bytes each, stays far under the cap on any node, and needs no agent
// change. system.swap 404s on swapless boxes and is simply skipped.
func (s *Service) fetchSample(ctx context.Context, nodeID string, enabled map[string]bool) (Sample, bool) {
	// Map enabled metrics → the Netdata chart they come from. cpu & iowait share
	// system.cpu, so dedup to one fetch when both are on.
	chartFor := map[string]string{
		"cpu": "system.cpu", "iowait": "system.cpu",
		"load":      "system.load",
		"mem":       "system.ram",
		"swap":      "system.swap",
		"net":       "system.net",
		"disk_io":   "system.io",
		"processes": "system.processes",
	}
	want := map[string]bool{}
	for metric := range enabled {
		if c, ok := chartFor[metric]; ok {
			want[c] = true
		}
	}
	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		results   = map[string]map[string]float64{}
		diskUsed  float64 // accumulated GB used across real mounts
		diskTotal float64 // accumulated GB capacity across real mounts
		diskGot   bool
	)
	for c := range want {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			if v := s.fetchChart(ctx, nodeID, c); v != nil {
				mu.Lock()
				results[c] = v
				mu.Unlock()
			}
		}(c)
	}
	// Disk space: discover real mounts (cached ~1h) and fetch each in parallel,
	// accumulating used/capacity so we report ONE total disk % (no per-partition
	// cards). disk_space dimensions are in GB: avail, used, "reserved for root".
	if enabled["disk_space"] {
		for _, mount := range s.discoverMounts(ctx, nodeID) {
			wg.Add(1)
			go func(mount string) {
				defer wg.Done()
				v := s.fetchChart(ctx, nodeID, "disk_space."+mount)
				if v == nil {
					return
				}
				total := v["avail"] + v["used"] + v["reserved for root"]
				if total <= 0 {
					return
				}
				mu.Lock()
				diskUsed += v["used"]
				diskTotal += total
				diskGot = true
				mu.Unlock()
			}(mount)
		}
	}
	wg.Wait()
	if len(results) == 0 && !diskGot {
		return Sample{}, false // Netdata didn't answer anything
	}
	sp := Sample{Ts: time.Now().Unix()}
	if diskTotal > 0 {
		sp.DiskUsedPct = diskUsed / diskTotal * 100
	}
	if v := results["system.load"]; v != nil {
		sp.Load1, sp.Load5, sp.Load15 = v["load1"], v["load5"], v["load15"]
	}
	if v := results["system.cpu"]; v != nil {
		sp.IOWait = v["iowait"]
		if idle, ok := v["idle"]; ok {
			sp.CPU = 100 - idle
		} else {
			// No idle dimension (some kernels): sum every reported component —
			// they are all non-idle, so the total is the busy %.
			var sum float64
			for _, x := range v {
				sum += x
			}
			sp.CPU = sum
		}
	}
	if v := results["system.ram"]; v != nil {
		total := v["free"] + v["used"] + v["cached"] + v["buffers"]
		if total > 0 {
			sp.MemUsedPct = v["used"] / total * 100
		}
	}
	if v := results["system.swap"]; v != nil {
		total := v["free"] + v["used"]
		if total > 0 {
			sp.SwapUsedPct = v["used"] / total * 100
		}
	}
	if v := results["system.net"]; v != nil {
		// abs: Netdata occasionally reports one direction as negative.
		sp.NetRx = math.Abs(v["received"])
		sp.NetTx = math.Abs(v["sent"])
	}
	if v := results["system.io"]; v != nil {
		sp.DiskRead = math.Abs(v["reads"])
		sp.DiskWrite = math.Abs(v["writes"])
	}
	if v := results["system.processes"]; v != nil {
		sp.ProcRunning = v["running"]
		sp.ProcBlocked = v["blocked"]
	}
	return sp, true
}

// fetchChart GETs one Netdata chart's latest point and returns label→value.
// nil on any transport/HTTP/parse failure (incl. 404 for swap on swapless hosts).
func (s *Service) fetchChart(ctx context.Context, nodeID, chart string) map[string]float64 {
	u := "http://127.0.0.1:19999/api/v1/data?chart=" + url.QueryEscape(chart) + "&format=json&points=1&options=seconds"
	r, _ := s.HTTPFetch(ctx, nodeID, u, 8*time.Second)
	if r.Err != "" || r.Status != 200 || r.Body == "" {
		return nil
	}
	var d struct {
		Labels []string    `json:"labels"`
		Data   [][]float64 `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.Body), &d); err != nil || len(d.Data) == 0 {
		return nil
	}
	row := d.Data[0]
	m := map[string]float64{}
	for i, lab := range d.Labels {
		if lab == "time" || i >= len(row) {
			continue
		}
		m[lab] = row[i]
	}
	return m
}

// mountCacheEntry caches a node's discovered disk mounts with a TTL, so we don't
// fetch the (large) /api/v1/charts body every 15s poll.
type mountCacheEntry struct {
	mounts    []string
	fetchedAt time.Time
}

// diskSpaceChartIDRe extracts disk_space.<mount> chart ids from the charts JSON.
// Run on the raw (possibly truncated) body — no json.Unmarshal, so truncation is
// harmless, and the disk_space charts sit in the first 512KB anyway.
var diskSpaceChartIDRe = regexp.MustCompile(`"id":"disk_space\.([^"]+)"`)

// discoverMounts returns the node's real disk mount points (e.g. "/", "/mnt/data").
// Cached per node for ~1h (mounts rarely change); falls back to "/" on any failure.
func (s *Service) discoverMounts(ctx context.Context, nodeID string) []string {
	s.mountMu.Lock()
	if e, ok := s.mounts[nodeID]; ok && time.Since(e.fetchedAt) < time.Hour {
		s.mountMu.Unlock()
		return e.mounts
	}
	s.mountMu.Unlock()

	r, _ := s.HTTPFetch(ctx, nodeID, "http://127.0.0.1:19999/api/v1/charts", 10*time.Second)
	var mounts []string
	if r.Err == "" && r.Status == 200 {
		seen := map[string]bool{}
		for _, m := range diskSpaceChartIDRe.FindAllStringSubmatch(r.Body, -1) {
			mount := strings.ReplaceAll(m[1], `\/`, `/`) // unescape JSON-escaped slashes
			if isPseudoMount(mount) || seen[mount] {
				continue
			}
			seen[mount] = true
			mounts = append(mounts, mount)
		}
	}
	if len(mounts) == 0 {
		mounts = []string{"/"} // never leave disk monitoring empty
	}
	sort.Strings(mounts)
	s.mountMu.Lock()
	s.mounts[nodeID] = mountCacheEntry{mounts: mounts, fetchedAt: time.Now()}
	s.mountMu.Unlock()
	return mounts
}

// isPseudoMount skips tmpfs / kernel mounts that aren't real disks (their
// "space" is RAM-backed, so a disk-space card would be misleading).
func isPseudoMount(m string) bool {
	if m == "/run" || m == "/run/lock" || m == "/dev/shm" || m == "/dev" || m == "/sys" {
		return true
	}
	return strings.HasPrefix(m, "/run/") || strings.HasPrefix(m, "/var/lib/docker/")
}

// evaluate checks a node's alert rules against the fresh sample and notifies on a
// sustained breach, once per outage (announce-once, re-arm on recovery).
func (s *Service) evaluate(ctx context.Context, nodeID, nodeName string, sp Sample, alerts []store.HealthAlert) {
	now := time.Now().Unix()
	for _, a := range alerts {
		v, ok := sp.Value(a.Metric)
		if !ok {
			continue
		}
		// load alert with threshold 0 means "cores × 2" (scales per node).
		threshold := a.Threshold
		if a.Metric == "load" && threshold == 0 {
			threshold = float64(sp.Cores) * 2
			if threshold == 0 {
				threshold = 4 // cores unknown — safe generic default
			}
		}
		breaching := v > threshold
		switch {
		case breaching && a.BreachSince == 0:
			// first breach this outage — start the sustained window
			_ = s.Store.TouchHealthAlertState(ctx, a.ID, now, 0)
		case breaching && now-a.BreachSince >= int64(a.WindowSec) && a.LastNotified < a.BreachSince:
			// held past the window and not yet announced this outage
			if s.TG != nil {
				s.TG.Notify(ctx, formatBreach(nodeName, a, sp, v, threshold))
			}
			_ = s.Store.TouchHealthAlertState(ctx, a.ID, a.BreachSince, now)
		case !breaching && a.BreachSince != 0:
			// recovered — clear so the next outage re-announces
			_ = s.Store.TouchHealthAlertState(ctx, a.ID, 0, 0)
		}
	}
}

func formatBreach(node string, a store.HealthAlert, sp Sample, v, threshold float64) string {
	p := healthAlertPresentation(a.Metric)
	thresholdText := fmtVal(threshold, a.Metric)
	currentText := fmtVal(v, a.Metric)
	resourceLine := ""
	if isLoadMetric(a.Metric) && sp.Cores > 0 {
		resourceLine = fmt.Sprintf("\nCPU 核心：%d 核", sp.Cores)
		currentText = fmt.Sprintf("%s（每核 %.2f）", currentText, v/float64(sp.Cores))
	}
	if (a.Metric == "load" || a.Metric == "load1") && a.Threshold == 0 && sp.Cores > 0 {
		thresholdText = fmt.Sprintf("%s（%d 核 × 2）", thresholdText, sp.Cores)
	}
	return fmt.Sprintf("⚠️ 健康告警｜%s\n──────────\n节点：%s\n问题：%s%s\n当前：%s\n阈值：%s\n持续：≥ %d 秒",
		p.Title, orNA(node), p.Metric, resourceLine, currentText, thresholdText, a.WindowSec)
}

func isLoadMetric(metric string) bool {
	return metric == "load" || metric == "load1" || metric == "load5" || metric == "load15"
}

type alertPresentation struct {
	Title  string
	Metric string
}

func healthAlertPresentation(metric string) alertPresentation {
	switch metric {
	case "cpu":
		return alertPresentation{
			Title:  "CPU 使用率过高",
			Metric: "CPU 使用率",
		}
	case "mem":
		return alertPresentation{
			Title:  "内存使用率过高",
			Metric: "物理内存使用率",
		}
	case "disk":
		return alertPresentation{
			Title:  "磁盘空间不足",
			Metric: "磁盘空间使用率",
		}
	case "iowait":
		return alertPresentation{
			Title:  "磁盘 I/O 等待过高",
			Metric: "CPU I/O Wait",
		}
	case "swap":
		return alertPresentation{
			Title:  "Swap 使用率过高",
			Metric: "Swap 使用率",
		}
	case "load", "load1":
		return alertPresentation{
			Title:  "系统任务负载过高",
			Metric: "1 分钟任务队列平均数（运行或等待 I/O）",
		}
	case "load5":
		return alertPresentation{
			Title:  "系统任务负载过高",
			Metric: "5 分钟任务队列平均数（运行或等待 I/O）",
		}
	case "load15":
		return alertPresentation{
			Title:  "系统任务负载长期过高",
			Metric: "15 分钟任务队列平均数（运行或等待 I/O）",
		}
	default:
		return alertPresentation{
			Title:  "指标超过阈值",
			Metric: metric,
		}
	}
}

func fmtVal(v float64, metric string) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d%s", int64(v), MetricUnit(metric))
	}
	return fmt.Sprintf("%.2f%s", v, MetricUnit(metric))
}

func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（未知节点）"
	}
	return s
}
