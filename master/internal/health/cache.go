// Package health implements the Netdata-backed health monitoring panel: it
// installs Netdata on selected nodes, polls each node's local Netdata via the
// agent (MsgHTTPFetch, curl fallback on old agents), serves cached metrics to
// the frontend, and evaluates per-node alert thresholds — pushing Telegram on a
// sustained breach.
package health

import (
	"sync"
)

// maxHistory caps the rolling sample buffer per node (~1h at a 15s poll).
const maxHistory = 240

// Sample is one decoded Netdata snapshot for a node. Derived percentages are
// precomputed so alert evaluation and the frontend read plain scalars.
type Sample struct {
	Ts          int64   `json:"ts"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	CPU         float64 `json:"cpu"`    // busy % (100 - idle)
	IOWait      float64 `json:"iowait"` // % (system.cpu iowait)
	MemUsedPct  float64 `json:"mem_used_pct"`
	SwapUsedPct float64 `json:"swap_used_pct"`
	NetRx       float64 `json:"net_rx"`    // KB/s
	NetTx       float64 `json:"net_tx"`    // KB/s
	DiskRead    float64 `json:"disk_read"` // KB/s
	DiskWrite   float64 `json:"disk_write"`
	ProcRunning float64 `json:"proc_running"`
	ProcBlocked float64 `json:"proc_blocked"`
	Cores       int     `json:"cores"`         // node CPU cores (load alert + display)
	DiskUsedPct float64 `json:"disk_used_pct"` // total disk used % across all real mounts (one number, no per-partition)
}

// Value returns the scalar for an alert metric key, or (0,false) if unknown.
func (s Sample) Value(metric string) (float64, bool) {
	switch metric {
	case "load1", "load":
		return s.Load1, true
	case "load5":
		return s.Load5, true
	case "load15":
		return s.Load15, true
	case "iowait":
		return s.IOWait, true
	case "cpu":
		return s.CPU, true
	case "mem":
		return s.MemUsedPct, true
	case "swap":
		return s.SwapUsedPct, true
	case "disk":
		return s.DiskUsedPct, true
	}
	return 0, false
}

// MetricUnit is a display helper for the alert message.
func MetricUnit(metric string) string {
	switch metric {
	case "load", "load1", "load5", "load15":
		return ""
	case "cpu", "iowait", "mem", "swap", "disk":
		return "%"
	}
	return ""
}

// nodeCache holds the latest sample plus a rolling history for one node.
type nodeCache struct {
	latest  Sample
	history []Sample
	hasData bool
}

// Cache is the in-memory metric store, keyed by node id.
type Cache struct {
	mu    sync.Mutex
	nodes map[string]*nodeCache
}

func newCache() *Cache { return &Cache{nodes: map[string]*nodeCache{}} }

// Put appends a sample and trims to maxHistory.
func (c *Cache) Put(nodeID string, s Sample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nc := c.nodes[nodeID]
	if nc == nil {
		nc = &nodeCache{}
		c.nodes[nodeID] = nc
	}
	nc.latest = s
	nc.hasData = true
	nc.history = append(nc.history, s)
	if len(nc.history) > maxHistory {
		nc.history = nc.history[len(nc.history)-maxHistory:]
	}
}

// Latest returns the newest sample and whether any data exists.
func (c *Cache) Latest(nodeID string) (Sample, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nc := c.nodes[nodeID]
	if nc == nil || !nc.hasData {
		return Sample{}, false
	}
	return nc.latest, true
}

// Delete drops the cached sample/history for a node. Called on uninstall so the
// node's card immediately stops showing stale metrics instead of lingering until
// the rolling buffer ages out.
func (c *Cache) Delete(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.nodes, nodeID)
}

// History returns samples with Ts >= since (oldest first).
func (c *Cache) History(nodeID string, since int64) []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	nc := c.nodes[nodeID]
	if nc == nil {
		return []Sample{}
	}
	out := make([]Sample, 0, len(nc.history))
	for _, s := range nc.history {
		if s.Ts >= since {
			out = append(out, s)
		}
	}
	return out
}
