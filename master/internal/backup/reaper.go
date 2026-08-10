package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// reaperCfg tunes the stale-"running" reaper. max_age_minutes is how old a
// running backup must be before it's treated as an orphan. The internal single-
// job timeout is 8h, so anything running past a couple of hours and not yet
// finalized was abandoned by a master restart. Floored at 10m so a slow-but-
// legitimate backup can't be reaped out from under a live job.
type reaperCfg struct {
	MaxAgeMinutes int `json:"max_age_minutes"`
}

func (s *Service) loadReaperCfg() reaperCfg {
	raw, _ := s.Store.GetSetting(context.Background(), "backup_reaper")
	var c reaperCfg
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	if c.MaxAgeMinutes < 10 {
		c.MaxAgeMinutes = 120
	}
	return c
}

// StartReaper launches the stale-running finalizer in the background. It reaps
// once immediately on start (cleaning orphans inherited from a previous master
// lifetime), then on a 5-minute ticker. See Store.ReapStaleBackups for the
// rationale and the orphan definition.
func (s *Service) StartReaper() {
	go s.reaperLoop()
}

func (s *Service) reaperLoop() {
	s.reapOnce() // finalize orphans left by the previous master lifetime on boot
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.reapOnce()
	}
}

func (s *Service) reapOnce() {
	ctx := context.Background()
	maxAge := time.Duration(s.loadReaperCfg().MaxAgeMinutes) * time.Minute
	reaped, err := s.Store.ReapStaleBackups(ctx, maxAge)
	if err != nil || len(reaped) == 0 {
		return
	}
	// An orphaned run may have left a partial stage file behind; evict it the
	// same way runBackup's failure path does, so it can't accumulate on disk.
	for _, b := range reaped {
		if b.StagePath != "" {
			_ = os.Remove(b.StagePath)
		}
		// ReapStaleBackups SELECTed these pre-UPDATE, so fix up the in-memory
		// copy before broadcasting so the UI sees the terminal status.
		b.Status = "failed"
		s.broadcast(&b)
	}
	if s.Notify != nil {
		names := make([]string, 0, len(reaped))
		for _, b := range reaped {
			nm := b.Name
			if nm == "" {
				nm = b.Container
			}
			if nm == "" {
				nm = shortID(b.ID)
			}
			names = append(names, nm)
		}
		s.Notify.Notify(ctx, fmt.Sprintf("🧹 清理 %d 个僵尸备份记录（running 超过 %s）：%s",
			len(reaped), maxAge, strings.Join(names, "、")))
	}
}
