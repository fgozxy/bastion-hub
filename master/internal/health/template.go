package health

import (
	"context"
	"encoding/json"
)

// MetricDef is one collectible metric in the fixed catalog. The template
// selects a subset via Enabled; collection + display code key off Key.
type MetricDef struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Unit    string `json:"unit"`              // "", "%", "KB/s"
	Chart   string `json:"chart"`             // "line" | "gauge" | "sparkline"
	Netdata string `json:"netdata,omitempty"` // source chart(s), documentation only
}

// Catalog is the universe of metrics the panel can collect and render. Adding a
// metric here + a collection branch in fetchSample is all that's needed to make
// it available to the template.
var Catalog = []MetricDef{
	{Key: "cpu", Label: "CPU 使用率", Unit: "%", Chart: "gauge", Netdata: "system.cpu"},
	{Key: "load", Label: "平均负载 1/5/15", Unit: "", Chart: "line", Netdata: "system.load"},
	{Key: "mem", Label: "内存", Unit: "%", Chart: "gauge", Netdata: "system.ram"},
	{Key: "swap", Label: "Swap", Unit: "%", Chart: "gauge", Netdata: "system.swap"},
	{Key: "disk_space", Label: "磁盘空间", Unit: "%", Chart: "gauge", Netdata: "disk_space.<mount>"},
	{Key: "disk_io", Label: "磁盘 I/O", Unit: "KB/s", Chart: "line", Netdata: "system.io"},
	{Key: "net", Label: "网络", Unit: "KB/s", Chart: "line", Netdata: "system.net"},
	{Key: "iowait", Label: "I/O 等待", Unit: "%", Chart: "sparkline", Netdata: "system.cpu"},
	{Key: "processes", Label: "进程", Unit: "", Chart: "sparkline", Netdata: "system.processes"},
}

// AlertDef is one default alert rule carried by the template and seeded per node.
type AlertDef struct {
	Metric    string  `json:"metric"`
	Threshold float64 `json:"threshold"`
	WindowSec int     `json:"window_sec"`
}

// Template drives both collection (which charts to poll) and display (which
// cards to render), plus the default alert rules seeded per node. Stored as the
// "health_template" setting; absent/invalid → DefaultTemplate().
//
// Conventions:
//   - load alert Threshold==0 means "cores × 2" (resolved per node at evaluate
//     time, so it scales with each node's CPU count).
//   - disk alert threshold applies to ANY mount exceeding it.
type Template struct {
	Enabled []string   `json:"enabled"`
	Alerts  []AlertDef `json:"alerts"`
}

// DefaultTemplate is the out-of-the-box set: a role-agnostic baseline covering
// Web/DB/container/storage workloads. Users adjust via the template editor.
func DefaultTemplate() Template {
	return Template{
		Enabled: []string{"cpu", "load", "mem", "swap", "disk_space", "disk_io", "net", "iowait", "processes"},
		Alerts: []AlertDef{
			{Metric: "cpu", Threshold: 90, WindowSec: 300},
			{Metric: "mem", Threshold: 92, WindowSec: 300},
			{Metric: "disk", Threshold: 90, WindowSec: 0},
			{Metric: "load", Threshold: 0, WindowSec: 0}, // → cores×2 per node
		},
	}
}

// loadTemplate reads the stored template, falling back to DefaultTemplate on any
// problem. Cheap enough to call every poll cycle (one SQLite read).
func (s *Service) loadTemplate() Template {
	raw, err := s.Store.GetSetting(context.Background(), "health_template")
	if err != nil || raw == "" {
		return DefaultTemplate()
	}
	var t Template
	if err := json.Unmarshal([]byte(raw), &t); err != nil || len(t.Enabled) == 0 {
		return DefaultTemplate()
	}
	return t
}

// enabledSet returns the enabled metrics as a set for quick lookup.
func (t Template) enabledSet() map[string]bool {
	m := make(map[string]bool, len(t.Enabled))
	for _, k := range t.Enabled {
		m[k] = true
	}
	return m
}
