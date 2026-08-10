// Package monitor periodically scans the stored container inventory and pushes a
// Telegram notification when a container enters a bad state (exited / dead /
// restarting). Each container is announced once until it recovers, so a
// sustained outage does not spam.
//
// The inventory itself is refreshed by agents every 30s (MsgContainers →
// ReplaceNodeContainers), so this package only reads the containers table — no
// agent change. Frequency + on/off live in the 'container_monitor' setting.
package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"nodepanel/master/internal/store"
	"nodepanel/master/internal/telegram"
)

// badStates are the container states treated as "has a problem".
var badStates = map[string]bool{"exited": true, "dead": true, "restarting": true}

// exitCodeRe pulls the exit code out of a Docker status like "Exited (0) 34 hours ago".
var exitCodeRe = regexp.MustCompile(`Exited \(([+-]?\d+)\)`)

// cleanExit reports an exited container's status as an INTENTIONAL clean stop
// (exit code 0). We do NOT alert on these: an exit-0 almost always means someone
// stopped the container on purpose (e.g. cloudflared after a Cloudflare Tunnel
// was dismantled and replaced with NPM). Crashes surface as non-zero codes —
// OOM-killed is 137 — and those still alert, along with `dead`/`restarting`.
func cleanExit(status string) bool {
	m := exitCodeRe.FindStringSubmatch(status)
	if len(m) != 2 {
		return false // "exited" with no parseable code → be safe, treat as bad
	}
	n, err := strconv.Atoi(m[1])
	return err == nil && n == 0
}

// Service scans container state and notifies on problems.
type Service struct {
	Store *store.Store
	TG    *telegram.Service

	mu       sync.Mutex
	notified map[string]bool // key = nodeID+"/"+name → currently known-bad
}

// New builds a monitor over the given store, pushing via tg.
func New(s *store.Store, tg *telegram.Service) *Service {
	return &Service{Store: s, TG: tg, notified: map[string]bool{}}
}

type cfg struct {
	Enabled  bool `json:"enabled"`
	Interval int  `json:"interval_seconds"`
}

// loadCfg reads the 'container_monitor' setting, defaulting interval to 60s and
// flooring it at 30s (the agent's own report period — scanning faster is noise).
func (s *Service) loadCfg() cfg {
	raw, _ := s.Store.GetSetting(context.Background(), "container_monitor")
	var c cfg
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	if c.Interval < 30 {
		c.Interval = 60
	}
	return c
}

// Start launches the scan loop in the background.
func (s *Service) Start() {
	go s.loop()
}

func (s *Service) loop() {
	for {
		c := s.loadCfg()
		if !c.Enabled {
			time.Sleep(60 * time.Second) // disabled — re-check periodically
			continue
		}
		s.scan()
		time.Sleep(time.Duration(c.Interval) * time.Second)
	}
}

// badEntry is one currently-bad container, formatted for the notification.
type badEntry struct {
	Node   string
	Name   string
	State  string
	Status string
}

// scan reads the latest inventory, finds newly-bad containers (not seen bad in
// the previous cycle), and pushes a single merged notification for them.
func (s *Service) scan() {
	ctx := context.Background()
	cons, err := s.Store.ListContainers(ctx)
	if err != nil {
		return
	}
	nodes, _ := s.Store.ListNodes(ctx)
	nodeName := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nodeName[n.ID] = n.Name
	}

	// Collect currently-bad containers (by stable node+name key).
	cur := make(map[string]badEntry)
	for _, c := range cons {
		if !badStates[c.State] {
			continue
		}
		// exited(0) = intentional clean stop — not an anomaly. Only crashes
		// (non-zero exit) and dead/restarting containers are flagged.
		if c.State == "exited" && cleanExit(c.Status) {
			continue
		}
		name := c.DisplayName
		if name == "" {
			name = c.Name
		}
		if name == "" {
			name = c.ContainerID
		}
		key := c.NodeID + "/" + name
		cur[key] = badEntry{Node: nodeName[c.NodeID], Name: name, State: c.State, Status: c.Status}
	}

	// Fresh = currently-bad but not yet announced this outage.
	s.mu.Lock()
	fresh := make([]badEntry, 0)
	for k, e := range cur {
		if !s.notified[k] {
			fresh = append(fresh, e)
		}
	}
	// Reset the known set to the current bad set: recovered containers drop out
	// (so they re-announce if they go bad again), ongoing ones stay (so they
	// don't re-announce every cycle).
	s.notified = make(map[string]bool, len(cur))
	for k := range cur {
		s.notified[k] = true
	}
	s.mu.Unlock()

	if len(fresh) == 0 || s.TG == nil {
		return
	}
	s.TG.Notify(ctx, formatBad(fresh))
}

// formatBad renders the merged notification: one block per container using the
// fixed 所属节点/容器名 shape, plus a state line for context.
func formatBad(fresh []badEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ 容器异常（共 %d 个）\n", len(fresh))
	for _, e := range fresh {
		b.WriteString("──────────\n")
		fmt.Fprintf(&b, "所属节点: %s\n", orNA(e.Node))
		fmt.Fprintf(&b, "容器名: %s\n", e.Name)
		if e.Status != "" {
			fmt.Fprintf(&b, "状态: %s（%s）\n", e.State, e.Status)
		} else {
			fmt.Fprintf(&b, "状态: %s\n", e.State)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func orNA(s string) string {
	if s == "" {
		return "（未知节点）"
	}
	return s
}
