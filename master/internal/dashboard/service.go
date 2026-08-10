// Package dashboard aggregates panel-wide statistics for the dashboard view.
package dashboard

import (
	"net/http"
	"time"

	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
)

type Service struct{ Store *store.Store }

type Stats struct {
	Nodes       NodeStats        `json:"nodes"`
	Commands    CountStats       `json:"commands"`
	Backups     BackupStats      `json:"backups"`
	Credentials int              `json:"credentials"`
	Countries   []CountryCount   `json:"countries"`
	Metrics     map[string]Mini  `json:"metrics"`
	Recent      []RecentCommand  `json:"recent"`
}

type NodeStats struct {
	Total  int `json:"total"`
	Online int `json:"online"`
}

type CountStats struct {
	Total int `json:"total"`
	Today int `json:"today"`
}

type BackupStats struct {
	Total   int            `json:"total"`
	Success int            `json:"success"`
	Failed  int            `json:"failed"`
	Recent  []MiniBackup   `json:"recent"`
}

type MiniBackup struct {
	ID        string `json:"id"`
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

type CountryCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type Mini struct {
	CPU       float64 `json:"cpu"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
	Load1     float64 `json:"load1"`
}

type RecentCommand struct {
	ID     string `json:"id"`
	Cmd    string `json:"cmd"`
	Status string `json:"status"`
	At     int64  `json:"at"`
}

// Stats GET /api/dashboard
func (s *Service) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodes, _ := s.Store.ListNodes(ctx)
	ns := NodeStats{Total: len(nodes)}
	countries := map[string]int{}
	for _, n := range nodes {
		if n.Status == "online" {
			ns.Online++
		}
		if n.CountryCode != "" {
			countries[n.CountryCode]++
		}
	}
	var cc []CountryCount
	for code, c := range countries {
		cc = append(cc, CountryCount{Code: code, Count: c})
	}

	cmds, _ := s.Store.ListCommands(ctx, 200)
	cs := CountStats{Total: len(cmds)}
	startToday := time.Now().Truncate(24 * time.Hour).Unix()
	for _, c := range cmds {
		if c.CreatedAt >= startToday {
			cs.Today++
		}
	}
	var recent []RecentCommand
	for i, c := range cmds {
		if i >= 10 {
			break
		}
		recent = append(recent, RecentCommand{ID: c.ID, Cmd: trunc(c.Cmd, 80), Status: c.Status, At: c.CreatedAt})
	}

	backups, _ := s.Store.ListBackups(ctx, "")
	bs := BackupStats{Total: len(backups)}
	for _, b := range backups {
		if b.Status == "ok" {
			bs.Success++
		} else if b.Status == "failed" {
			bs.Failed++
		}
	}
	for i, b := range backups {
		if i >= 14 {
			break
		}
		bs.Recent = append(bs.Recent, MiniBackup{b.ID, b.NodeID, b.Name, b.Size, b.Status, b.CreatedAt})
	}

	creds, _ := s.Store.ListCredentials(ctx)
	latest, _ := s.Store.LatestMetrics(ctx)
	metrics := map[string]Mini{}
	for k, m := range latest {
		metrics[k] = Mini{CPU: m.CPU, MemUsed: m.MemUsed, MemTotal: m.MemTotal, DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal, Load1: m.Load1}
	}

	httpx.OK(w, Stats{
		Nodes: ns, Commands: cs, Backups: bs, Credentials: len(creds),
		Countries: cc, Metrics: metrics, Recent: recent,
	})
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
