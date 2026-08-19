// Package scheduler runs backup and container-update jobs on cron schedules.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/backup"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

// beijing is Asia/Shanghai (UTC+8, no DST). The master runs in a UTC container,
// so cron schedules are evaluated against Beijing wall-clock regardless of the
// container's own timezone — the "hour:minute" the admin picks always means
// Beijing time. FixedZone avoids any dependency on tzdata.
var beijing = time.FixedZone("CST", 8*3600)

type Scheduler struct {
	cron    *cron.Cron
	store   *store.Store
	backup  *backup.Service
	hub     *agenthub.Hub
	notify  backup.Notifier
	mu      sync.Mutex
	started bool

	// Backup report aggregation: every schedule's batch enqueues its outcome
	// instead of pushing its own Telegram message; once no batch is in flight a
	// short debounce timer flushes everything as ONE combined report, so a
	// nightly window of N schedules produces a single push, not N.
	reportMu       sync.Mutex
	reportPending  []backupReportSection
	reportTimer    *time.Timer
	backupInFlight int
}

// backupReportSection is one schedule batch's contribution to the combined
// report: its per-unit outcomes plus the storage targets they were pushed to.
type backupReportSection struct {
	results []backup.UnitResult
	targets []backup.TargetInfo
}

func New(s *store.Store, b *backup.Service, h *agenthub.Hub, n backup.Notifier) *Scheduler {
	return &Scheduler{store: s, backup: b, hub: h, notify: n}
}

// Start begins the scheduler and re-syncs every minute.
func (sc *Scheduler) Start() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.started {
		return
	}
	sc.started = true
	sc.rebuild()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			sc.mu.Lock()
			sc.rebuild()
			sc.mu.Unlock()
		}
	}()
}

// Sync forces an immediate rebuild (call after schedule changes).
func (sc *Scheduler) Sync() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.rebuild()
}

func (sc *Scheduler) rebuild() {
	if sc.cron != nil {
		<-sc.cron.Stop().Done()
	}
	c := cron.New(cron.WithLocation(beijing))
	scheds, err := sc.store.ListSchedules(context.Background())
	if err != nil {
		log.Printf("scheduler: list: %v", err)
		sc.cron = c
		c.Start()
		return
	}
	for _, s := range scheds {
		if !s.Enabled || s.Cron == "" {
			continue
		}
		s := s
		var job func()
		switch s.Type {
		case "backup":
			job = func() { sc.runBackup(s) }
		case "container_update":
			job = func() { sc.runContainerUpdate(s) }
		default:
			continue
		}
		if _, err := c.AddFunc(s.Cron, job); err != nil {
			log.Printf("scheduler: add %s (%s): %v", s.Type, s.Cron, err)
			continue
		}
	}
	sc.cron = c
	c.Start()
}

// backupTarget is one container to back up within a single schedule.
type backupTarget struct {
	NodeID    string `json:"node_id"`
	Container string `json:"container_id"`
	Name      string `json:"name"` // optional: stable across container recreation
}

type backupCfg struct {
	Paths                 []string       `json:"paths"`
	Container             string         `json:"container"`              // legacy single container (node = schedule.NodeID)
	Containers            []backupTarget `json:"containers"`             // multi-container: one tar archive per entry
	IncrementalContainers []backupTarget `json:"incremental_containers"` // multi-container: one restic snapshot per entry
	NodeIDs               []string       `json:"node_ids"`               // multi-node dir backup
	TargetID              string         `json:"target_id"`              // legacy single target
	TargetIDs             []string       `json:"target_ids"`             // multi-target push
	Name                  string         `json:"name"`
}

// backupUnit is one (node, container|paths) to back up within a single schedule.
type backupUnit struct {
	nodeID      string
	container   string
	name        string
	paths       []string
	incremental bool
}

func (sc *Scheduler) runBackup(s store.Schedule) {
	var cfg backupCfg
	_ = json.Unmarshal([]byte(s.Config), &cfg)

	tids := cfg.TargetIDs
	if len(tids) == 0 && cfg.TargetID != "" {
		tids = []string{cfg.TargetID}
	}

	// Expand the schedule into (nodeID, container|paths) units. One archive is
	// produced per unit; each is pushed to every target id. This lets a single
	// schedule cover many containers/nodes instead of fanning out into many
	// near-identical schedules.
	var units []backupUnit
	appendContainerUnits := func(entries []backupTarget, incremental bool) {
		for _, c := range entries {
			if c.NodeID == "" {
				continue
			}
			// Prefer the container NAME when present: it survives recreation
			// (docker compose up, migration) whereas the stored 64-char ID does
			// not. If a named entry no longer exists in the live inventory, skip it
			// instead of falling back to a stale ID and producing an empty archive.
			ref := c.Container
			if c.Name != "" {
				cur := sc.store.ContainerIDByName(context.Background(), c.NodeID, c.Name)
				if cur == "" {
					log.Printf("scheduler: backup %s node %s container %q no longer exists; skipping stale entry", s.ID, c.NodeID, c.Name)
					continue
				}
				ref = cur
			}
			if ref != "" {
				units = append(units, backupUnit{nodeID: c.NodeID, container: ref, name: c.Name, incremental: incremental})
			}
		}
	}
	if len(cfg.Containers) > 0 {
		appendContainerUnits(cfg.Containers, false)
	}
	if len(cfg.IncrementalContainers) > 0 {
		appendContainerUnits(cfg.IncrementalContainers, true)
	}
	if len(cfg.Containers) == 0 && len(cfg.IncrementalContainers) == 0 && cfg.Container != "" && s.NodeID != "" {
		// legacy single-container schedule
		units = append(units, backupUnit{nodeID: s.NodeID, container: cfg.Container})
	}
	if len(units) == 0 && len(cfg.Paths) > 0 {
		nodeIDs := cfg.NodeIDs
		if len(nodeIDs) == 0 && s.NodeID != "" {
			nodeIDs = []string{s.NodeID}
		}
		for _, nid := range nodeIDs {
			units = append(units, backupUnit{nodeID: nid, paths: cfg.Paths})
		}
	}
	if len(units) == 0 {
		return
	}

	// Run the whole batch detached: every unit backs up concurrently (as the old
	// fire-and-forget path did), then exactly one node×target report is pushed
	// once every unit has settled. Detaching keeps this cron invocation short so
	// the per-minute rebuild never blocks on a long backup window.
	go sc.runBackupBatch(s, units, tids, cfg.Name)
}

// offlineRetryWindow bounds how long a schedule batch waits for a node that was
// offline at fire time to come back and still get its backup. The batch runs in
// a detached goroutine, so a long wait only delays this schedule's status report
// — it never blocks the per-minute rebuild. Keep it comfortably under the
// schedule's own interval (daily schedules → a multi-hour window is safe) so a
// later fire cannot overlap an in-flight retry batch for the same containers.
const (
	offlineRetryWindow     = 2 * time.Hour
	offlineRetryStep       = 10 * time.Minute
	backupBatchConcurrency = 1
)

// reportFlushDelay is the debounce between the last in-flight backup batch
// finishing and the combined report push. Batches that end while another is
// still running simply enqueue; only when nothing has run for this long does
// the aggregated report go out. A var so tests can shorten it.
var reportFlushDelay = 3 * time.Minute

// runBackupBatch runs every unit concurrently, waits for all to finish, then
// enqueues its node×target status section (🟢/🔴 grid). The actual push is
// deferred to flushBackupReports so every schedule firing in the same window
// is merged into ONE Telegram message instead of one message per schedule.
func (sc *Scheduler) runBackupBatch(s store.Schedule, units []backupUnit, tids []string, name string) {
	sc.trackBackupStart()
	ctx := context.Background()
	results := make([]backup.UnitResult, len(units))
	errs := make([]error, len(units))

	// run executes one unit and records its outcome in place.
	run := func(i int, u backupUnit) {
		var (
			ur  backup.UnitResult
			err error
		)
		if u.incremental {
			ur, err = sc.backup.RunResticContainerBackupSync(ctx, u.nodeID, u.container, u.name, name, "scheduler", 8*time.Hour)
		} else {
			ur, err = sc.backup.RunBackupSync(ctx, u.nodeID, u.paths, u.container, tids, name, "scheduler")
		}
		if err != nil {
			log.Printf("scheduler: backup %s node %s: %v", s.ID, u.nodeID, err)
		}
		results[i], errs[i] = ur, err
	}

	// First pass: bounded concurrency. Backup archives can be large and every
	// unit pushes to the same storage targets; a small worker pool avoids nightly
	// stampedes while still letting slow nodes overlap with faster ones.
	limit := backupBatchConcurrency
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, u := range units {
		i, u := i, u
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			run(i, u)
		}()
	}
	wg.Wait()

	// A node that briefly dropped around the cron instant still gets backed up
	// once it reconnects, instead of losing the whole day. Units that failed for
	// any other reason (target error, etc.) are not retried here — their 🔴 is
	// already meaningful and the next fire covers them.
	sc.retryOfflineUnits(s, units, results, errs, run)

	_ = sc.store.MarkScheduleRun(ctx, s.ID, time.Now().Unix())

	// resolve target display names in schedule (target_ids) order
	hasIncremental := false
	for _, u := range units {
		if u.incremental {
			hasIncremental = true
			break
		}
	}
	targets := make([]backup.TargetInfo, 0, len(tids)+1)
	for _, tid := range tids {
		nm := tid
		typ := ""
		if len(tid) > 8 {
			nm = tid[:8]
		}
		if t, err := sc.store.GetTarget(ctx, tid); err == nil && t != nil {
			nm = t.Name
			typ = t.Type
		}
		targets = append(targets, backup.TargetInfo{ID: tid, Type: typ, Name: nm})
	}
	if hasIncremental {
		targets = append(targets, backup.TargetInfo{ID: backup.ResticIncrementalTargetID, Type: "restic", Name: "Restic增量"})
	}
	sc.queueBackupReport(backupReportSection{results: results, targets: targets})
}

// trackBackupStart registers an in-flight backup batch; the matching
// queueBackupReport unregisters it. The combined report only goes out once the
// count returns to zero, so overlapping schedule fires aggregate.
func (sc *Scheduler) trackBackupStart() {
	sc.reportMu.Lock()
	sc.backupInFlight++
	sc.reportMu.Unlock()
}

// queueBackupReport records a finished batch's report section and, when no
// batch remains in flight, (re)arms the debounce timer that flushes everything
// as one message.
func (sc *Scheduler) queueBackupReport(section backupReportSection) {
	sc.reportMu.Lock()
	defer sc.reportMu.Unlock()
	sc.backupInFlight--
	if sc.notify == nil {
		return
	}
	sc.reportPending = append(sc.reportPending, section)
	if sc.backupInFlight > 0 {
		return
	}
	if sc.reportTimer != nil {
		sc.reportTimer.Stop()
	}
	sc.reportTimer = time.AfterFunc(reportFlushDelay, sc.flushBackupReports)
}

// flushBackupReports merges every queued section into a single node×target
// report and pushes it. Results are concatenated (FormatBackupReport already
// aggregates by node) and targets are unioned by ID in first-seen order, so
// schedules pushing to different target sets still render one coherent grid.
// A no-op while another batch is still running — its queueBackupReport will
// re-arm the timer.
func (sc *Scheduler) flushBackupReports() {
	sc.reportMu.Lock()
	if sc.backupInFlight > 0 || len(sc.reportPending) == 0 {
		sc.reportMu.Unlock()
		return
	}
	pending := sc.reportPending
	sc.reportPending = nil
	sc.reportTimer = nil
	notify := sc.notify
	sc.reportMu.Unlock()
	if notify == nil {
		return
	}
	var results []backup.UnitResult
	var targets []backup.TargetInfo
	seen := make(map[string]bool)
	for _, section := range pending {
		results = append(results, section.results...)
		for _, t := range section.targets {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			targets = append(targets, t)
		}
	}
	stamp := time.Now().In(beijing).Format("2006-01-02 15:04")
	title := fmt.Sprintf("📋 计划备份报告\n%s 北京", stamp)
	notify.Notify(context.Background(), backup.FormatBackupReport(title, results, targets))
}

// retryOfflineUnits re-runs units whose last attempt failed with a transient
// connection error (ErrNodeOffline, or ErrAgentDisconnected when the agent's
// websocket dropped mid-backup), retrying each as soon as its node reconnects,
// until none remain or offlineRetryWindow elapses. results/errs are updated in
// place, so the single status report pushed afterwards reflects the post-retry
// outcome.
func (sc *Scheduler) retryOfflineUnits(s store.Schedule, units []backupUnit, results []backup.UnitResult, errs []error, run func(int, backupUnit)) {
	retryable := func(err error) bool {
		return err == backup.ErrNodeOffline || err == backup.ErrAgentDisconnected
	}
	deadline := time.Now().Add(offlineRetryWindow)
	for time.Now().Before(deadline) {
		// Retry every still-connection-failed unit whose node is back online.
		for i, u := range units {
			if retryable(errs[i]) && sc.hub.Online(u.nodeID) {
				log.Printf("scheduler: backup %s retry node %s (back online)", s.ID, u.nodeID)
				run(i, u)
			}
		}
		// Stop once no unit is still pending on a connection failure. A unit that
		// failed with a different error after retry is intentionally left alone.
		pending := false
		for i := range units {
			if retryable(errs[i]) {
				pending = true
				break
			}
		}
		if !pending {
			return
		}
		time.Sleep(offlineRetryStep)
	}
	log.Printf("scheduler: backup %s offline-retry window elapsed; leaving unreachable nodes 🔴", s.ID)
}

type containerCfg struct {
	Label      string         `json:"label"`
	NodeIDs    []string       `json:"node_ids"`   // legacy multi-node update (fallback to schedule.NodeID)
	Containers []backupTarget `json:"containers"` // selective auto-update (preferred over node_ids)
}

// upgradedImage records a successful version-tag upgrade so the Telegram report
// can show "node/name  oldTag → newTag" instead of a bare container name. nodeID
// is filled by the aggregator (runContainerUpdate); per-node outcomes leave it blank.
type upgradedImage struct {
	nodeID, name, from, to string
}

type containerUpdateStats struct {
	configured   int
	attempted    int
	succeeded    int
	offline      []string // node IDs
	unavailable  []string // node IDs
	scanned      int
	candidates   int
	unknown      int
	updated      int
	updatedNames []string // "nodeID/container" entries that were actually recreated
	upgraded     []upgradedImage
	unchanged    int
	skipped      int
	failed       int
	failures     []string // "nodeID/container: reason" or "nodeID: reason"
	nodeNames    map[string]string
}

type containerUpdateOutcome struct {
	succeeded    bool
	unavailable  bool
	scanned      int
	candidates   int
	unknown      int
	updated      int
	updatedNames []string // container names on this node
	upgraded     []upgradedImage
	unchanged    int
	skipped      int
	failed       int
	failures     []string
}

func (sc *Scheduler) runContainerUpdate(s store.Schedule) {
	ctx := context.Background()
	var cfg containerCfg
	_ = json.Unmarshal([]byte(s.Config), &cfg)

	// Prefer explicit container selection. When set, derive nodes from the list
	// and restrict updates to those names (stable across recreate). Legacy
	// schedules keep node_ids + optional label filter.
	allowByNode := map[string]map[string]struct{}{}
	var nodeIDs []string
	if len(cfg.Containers) > 0 {
		for _, c := range cfg.Containers {
			if c.NodeID == "" {
				continue
			}
			name := strings.TrimSpace(c.Name)
			if name == "" && c.Container != "" {
				name = sc.store.ContainerNameByID(ctx, c.NodeID, c.Container)
			}
			if name == "" {
				continue
			}
			if allowByNode[c.NodeID] == nil {
				allowByNode[c.NodeID] = map[string]struct{}{}
				nodeIDs = append(nodeIDs, c.NodeID)
			}
			allowByNode[c.NodeID][name] = struct{}{}
		}
	} else {
		nodeIDs = cfg.NodeIDs
		if len(nodeIDs) == 0 && s.NodeID != "" {
			nodeIDs = []string{s.NodeID}
		}
	}
	nodeIDs = uniqueStrings(nodeIDs)
	if len(nodeIDs) == 0 {
		return
	}

	// Resolve node IDs → display names once so the Telegram report can list
	// what actually happened (which node / which container) instead of bare counts.
	nodeNames := make(map[string]string, len(nodeIDs))
	for _, nid := range nodeIDs {
		if n, err := sc.store.GetNode(ctx, nid); err == nil && n != nil && n.Name != "" {
			nodeNames[nid] = n.Name
		} else {
			nodeNames[nid] = nid
		}
	}

	// Scan every online node first, then update only containers the scan confirms
	// are both running and behind their registry image.
	var wg sync.WaitGroup
	var mu sync.Mutex
	stats := containerUpdateStats{configured: len(nodeIDs), nodeNames: nodeNames}
	stamp := time.Now().Format("150405")
	for _, nid := range nodeIDs {
		if !sc.hub.Online(nid) {
			stats.offline = append(stats.offline, nid)
			continue
		}
		stats.attempted++
		nid := nid
		allow := allowByNode[nid] // nil for legacy node-wide schedules
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome := sc.runContainerUpdateNode(s.ID, nid, cfg.Label, stamp, allow)
			mu.Lock()
			defer mu.Unlock()
			if outcome.unavailable {
				stats.unavailable = append(stats.unavailable, nid)
			} else if outcome.succeeded {
				stats.succeeded++
			}
			stats.scanned += outcome.scanned
			stats.candidates += outcome.candidates
			stats.unknown += outcome.unknown
			stats.updated += outcome.updated
			for _, name := range outcome.updatedNames {
				stats.updatedNames = append(stats.updatedNames, nid+"/"+name)
			}
			for _, u := range outcome.upgraded {
				stats.upgraded = append(stats.upgraded, upgradedImage{nodeID: nid, name: u.name, from: u.from, to: u.to})
			}
			stats.unchanged += outcome.unchanged
			stats.skipped += outcome.skipped
			stats.failed += outcome.failed
			stats.failures = append(stats.failures, outcome.failures...)
		}()
	}
	wg.Wait()

	_ = sc.store.MarkScheduleRun(ctx, s.ID, time.Now().Unix())
	// Quiet success with zero updates is intentionally silent: every cron fire
	// would otherwise spam Telegram with "✅ 成功 N / 更新 0 / 失败 0".
	if sc.notify != nil && containerUpdateReportWorthSending(stats) {
		sc.notify.Notify(ctx, formatContainerUpdateReport(stats))
	}
}

func (sc *Scheduler) runContainerUpdateNode(scheduleID, nodeID, label, stamp string, allowNames map[string]struct{}) containerUpdateOutcome {
	var out containerUpdateOutcome
	node, err := sc.store.GetNode(context.Background(), nodeID)
	if err != nil {
		out.failures = append(out.failures, nodeID+": node metadata unavailable")
		return out
	}
	if !schedulerAgentVersionAtLeast(node.AgentVersion, 2, 4, 0) {
		_ = sc.store.InvalidateContainerScan(context.Background(), nodeID)
		out.failures = append(out.failures, nodeID+": agent must be upgraded to 2.4.0 for read-only update scans")
		return out
	}

	items, err := sc.scanContainerUpdates(nodeID, node.AgentVersion,
		"cscan:"+scheduleID+":"+nodeID+":"+stamp, 90*time.Second)
	if err != nil {
		_ = sc.store.InvalidateContainerScan(context.Background(), nodeID)
		if isMissingDockerSocketScanError(err.Error()) {
			out.unavailable = true
			return out
		}
		out.failures = append(out.failures, nodeID+": scan failed: "+err.Error())
		return out
	}
	out.scanned = len(items)
	if err := sc.store.UpdateContainerScan(context.Background(), nodeID, items); err != nil {
		_ = sc.store.InvalidateContainerScan(context.Background(), nodeID)
		out.failures = append(out.failures, nodeID+": scan cache update failed")
		return out
	}

	// Selective schedules only classify/update the chosen containers; the full
	// scan above still refreshes the node's inventory cache.
	classifyItems := items
	if len(allowNames) > 0 {
		classifyItems = make([]proto.ContainerScanItem, 0, len(allowNames))
		seen := make(map[string]struct{}, len(allowNames))
		for _, it := range items {
			if _, ok := allowNames[it.Name]; ok {
				classifyItems = append(classifyItems, it)
				seen[it.Name] = struct{}{}
			}
		}
		// Container names are the stable schedule key across normal recreates,
		// but Compose project/name migrations can still invalidate them. Never
		// silently turn a stale configured target into a successful no-op.
		missing := make([]string, 0)
		for name := range allowNames {
			if _, ok := seen[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		for _, name := range missing {
			out.failed++
			out.failures = append(out.failures, nodeID+"/"+name+": configured container missing from scan")
		}
	}

	type cand struct {
		id, name, suggested, from, to string
	}
	descs, part := classifyUpdateCandidates(classifyItems)
	out.candidates += part.candidates
	out.unchanged += part.unchanged
	out.skipped += part.skipped
	out.unknown += part.unknown

	var candidates []cand
	for _, d := range descs {
		id := sc.store.ContainerIDByName(context.Background(), nodeID, d.name)
		if id == "" {
			out.failed++
			out.failures = append(out.failures, nodeID+"/"+d.name+": current container id unavailable")
			continue
		}
		candidates = append(candidates, cand{id: id, name: d.name, suggested: d.suggested, from: d.from, to: d.to})
	}
	if len(candidates) == 0 {
		out.succeeded = len(out.failures) == 0
		return out
	}

	// Split: semver tag bumps use action=upgrade (rewrite compose image);
	// same-tag digest refresh uses action=update.
	var plainIDs, plainNames []string
	for _, c := range candidates {
		if c.suggested != "" {
			res, err := sc.upgradeScannedContainer(nodeID, c.id, c.suggested,
				fmt.Sprintf("cupd:%s:%s:%s:%s", scheduleID, nodeID, stamp, c.name), 30*time.Minute)
			if err != nil {
				out.failed++
				out.failures = append(out.failures, nodeID+"/"+c.name+": upgrade failed: "+err.Error())
				continue
			}
			out.updated += len(res.Updated)
			out.updatedNames = append(out.updatedNames, res.Updated...)
			for _, un := range res.Updated {
				out.upgraded = append(out.upgraded, upgradedImage{name: un, from: c.from, to: c.to})
			}
			// upgradeAction returns detail on success without always filling Updated;
			// count by name when agent reports OK with empty lists.
			if len(res.Updated) == 0 && res.OK && res.Err == "" && len(containerResultFailures(res)) == 0 {
				out.updated++
				out.updatedNames = append(out.updatedNames, c.name)
				out.upgraded = append(out.upgraded, upgradedImage{name: c.name, from: c.from, to: c.to})
			}
			out.unchanged += len(res.Unchanged)
			out.skipped += len(res.Skipped)
			for name, reason := range containerResultFailures(res) {
				out.failed++
				out.failures = append(out.failures, nodeID+"/"+name+": "+reason)
			}
			if res.Err != "" {
				out.failures = append(out.failures, nodeID+"/"+c.name+": "+res.Err)
			} else if !res.OK && len(containerResultFailures(res)) == 0 && len(res.Updated) == 0 {
				out.failures = append(out.failures, nodeID+"/"+c.name+": agent reported failure")
			}
			_ = sc.store.InvalidateContainerScanContainers(context.Background(), nodeID, []string{c.id, c.name, c.suggested})
		} else {
			plainIDs = append(plainIDs, c.id)
			plainNames = append(plainNames, c.name)
		}
	}
	if len(plainIDs) > 0 {
		res, err := sc.updateScannedContainers(nodeID, plainIDs, label,
			"cupd:"+scheduleID+":"+nodeID+":"+stamp, 30*time.Minute)
		if err != nil {
			out.failed += len(plainIDs)
			for _, name := range plainNames {
				out.failures = append(out.failures, nodeID+"/"+name+": update failed: "+err.Error())
			}
		} else {
			out.updated += len(res.Updated)
			out.updatedNames = append(out.updatedNames, res.Updated...)
			out.unchanged += len(res.Unchanged)
			out.skipped += len(res.Skipped)
			resultFailures := containerResultFailures(res)
			out.failed += len(resultFailures)
			failureNames := make([]string, 0, len(resultFailures))
			for name := range resultFailures {
				failureNames = append(failureNames, name)
			}
			sort.Strings(failureNames)
			for _, name := range failureNames {
				out.failures = append(out.failures, nodeID+"/"+name+": "+resultFailures[name])
			}
			if res.Err != "" {
				out.failures = append(out.failures, nodeID+": "+res.Err)
			} else if !res.OK && len(resultFailures) == 0 {
				out.failures = append(out.failures, nodeID+": agent reported failure")
			}
			refs := append(append(append([]string{}, plainIDs...), plainNames...), res.Updated...)
			_ = sc.store.InvalidateContainerScanContainers(context.Background(), nodeID, refs)
		}
	}
	out.succeeded = len(out.failures) == 0
	return out
}

// candDesc is a version-upgrade candidate produced by classifyUpdateCandidates,
// before the container ID is resolved against the node's inventory. from/to are
// the tag portions of the current and suggested image refs (for report display).
type candDesc struct {
	name, suggested, from, to string
}

// classifyUpdateCandidates applies the version-driven update policy to a node's
// scan results and returns the containers that should be auto-upgraded, plus the
// per-node outcome counters for skipped/unchanged/candidate/unknown items.
//
// Policy: real semver bumps carry SuggestedImage and are upgraded by rewriting
// Compose. A configured floating channel such as :latest is refreshed in place
// when its digest changes. Fixed-tag same-tag drift remains skipped so a
// deliberately version-pinned service is not overwritten after a force-push.
func classifyUpdateCandidates(items []proto.ContainerScanItem) ([]candDesc, containerUpdateOutcome) {
	var descs []candDesc
	var out containerUpdateOutcome
	for _, item := range items {
		// build/local/pinned: not auto-updated.
		if !registryEligibleUpdateType(item.UpdateType) {
			out.skipped++
			continue
		}
		switch item.HasUpdate {
		case 0:
			out.unchanged++
		case 1:
			if item.UpdateType != "latest" && item.UpdateType != "tag" {
				out.skipped++
				continue
			}
			if item.State != "" && item.State != "running" {
				out.skipped++
				continue
			}
			// Only floating tags opt into same-tag content refreshes.
			if strings.TrimSpace(item.SuggestedImage) == "" && item.UpdateType != "latest" {
				out.skipped++
				continue
			}
			out.candidates++
			descs = append(descs, candDesc{
				name:      item.Name,
				suggested: strings.TrimSpace(item.SuggestedImage),
				from:      imageTagOf(item.Image),
				to:        imageTagOf(strings.TrimSpace(item.SuggestedImage)),
			})
		default:
			out.unknown++
			out.skipped++
		}
	}
	return descs, out
}

func (sc *Scheduler) scanContainerUpdates(nodeID, agentVersion, reqID string, timeout time.Duration) ([]proto.ContainerScanItem, error) {
	ch := sc.hub.Subscribe(reqID)
	defer sc.hub.Unsubscribe(reqID)
	env, err := proto.Encode(proto.MsgContainerScan, reqID, nil)
	if err != nil {
		return nil, err
	}
	if err := sc.hub.Send(nodeID, env); err != nil {
		return nil, err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("agent disconnected")
		}
		return decodeScheduledScanResult(msg.Data, agentVersion)
	case <-time.After(timeout):
		return nil, fmt.Errorf("scan timed out")
	}
}

func decodeScheduledScanResult(data json.RawMessage, agentVersion string) ([]proto.ContainerScanItem, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty agent result")
	}
	var wire struct {
		OK    *bool                     `json:"ok"`
		Err   string                    `json:"err,omitempty"`
		Items []proto.ContainerScanItem `json:"items"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("invalid agent result: %w", err)
	}
	if wire.Err != "" {
		return nil, fmt.Errorf("%s", wire.Err)
	}
	if wire.OK != nil && !*wire.OK {
		return nil, fmt.Errorf("agent reported scan failure")
	}
	if wire.OK == nil && schedulerAgentVersionAtLeast(agentVersion, 2, 4, 0) {
		return nil, fmt.Errorf("agent scan result is missing ok")
	}
	if wire.Items == nil {
		wire.Items = []proto.ContainerScanItem{}
	}
	return wire.Items, nil
}

func (sc *Scheduler) updateScannedContainers(nodeID string, ids []string, label, reqID string, timeout time.Duration) (proto.ContainerResult, error) {
	ch := sc.hub.Subscribe(reqID)
	defer sc.hub.Unsubscribe(reqID)
	env, err := proto.Encode(proto.MsgContainerOp, reqID, proto.ContainerOpRequest{Action: "update", IDs: ids, Label: label})
	if err != nil {
		return proto.ContainerResult{}, err
	}
	if err := sc.hub.Send(nodeID, env); err != nil {
		return proto.ContainerResult{}, err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return proto.ContainerResult{}, fmt.Errorf("agent disconnected")
		}
		if len(msg.Data) == 0 {
			return proto.ContainerResult{}, fmt.Errorf("empty agent result")
		}
		var res proto.ContainerResult
		if err := json.Unmarshal(msg.Data, &res); err != nil {
			return proto.ContainerResult{}, fmt.Errorf("invalid agent result: %w", err)
		}
		return res, nil
	case <-time.After(timeout):
		return proto.ContainerResult{}, fmt.Errorf("operation timed out")
	}
}

// upgradeScannedContainer rewrites one container's compose image to newImage
// (semver tag bump) and recreates it via the agent upgrade action.
func (sc *Scheduler) upgradeScannedContainer(nodeID, id, newImage, reqID string, timeout time.Duration) (proto.ContainerResult, error) {
	ch := sc.hub.Subscribe(reqID)
	defer sc.hub.Unsubscribe(reqID)
	env, err := proto.Encode(proto.MsgContainerOp, reqID, proto.ContainerOpRequest{
		Action: "upgrade", IDs: []string{id}, NewImage: newImage,
	})
	if err != nil {
		return proto.ContainerResult{}, err
	}
	if err := sc.hub.Send(nodeID, env); err != nil {
		return proto.ContainerResult{}, err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return proto.ContainerResult{}, fmt.Errorf("agent disconnected")
		}
		if len(msg.Data) == 0 {
			return proto.ContainerResult{}, fmt.Errorf("empty agent result")
		}
		var res proto.ContainerResult
		if err := json.Unmarshal(msg.Data, &res); err != nil {
			return proto.ContainerResult{}, fmt.Errorf("invalid agent result: %w", err)
		}
		return res, nil
	case <-time.After(timeout):
		return proto.ContainerResult{}, fmt.Errorf("operation timed out")
	}
}

func schedulerAgentVersionAtLeast(version string, major, minor, patch int) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	version = strings.SplitN(strings.SplitN(version, "-", 2)[0], "+", 2)[0]
	var gotMajor, gotMinor, gotPatch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &gotMajor, &gotMinor, &gotPatch); err != nil {
		if _, err := fmt.Sscanf(version, "%d.%d", &gotMajor, &gotMinor); err != nil {
			return false
		}
	}
	if gotMajor != major {
		return gotMajor > major
	}
	if gotMinor != minor {
		return gotMinor > minor
	}
	return gotPatch >= patch
}

func containerResultFailures(res proto.ContainerResult) map[string]string {
	out := make(map[string]string, len(res.Failed))
	for name, reason := range res.Failed {
		out[name] = reason
	}
	for name, detail := range res.Details {
		trimmed := strings.TrimSpace(detail)
		lower := strings.ToLower(trimmed)
		if _, exists := out[name]; !exists && (strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "failed:")) {
			out[name] = trimmed
		}
	}
	return out
}

// containerUpdateReportWorthSending reports whether a scheduled container-update
// run produced anything the operator needs to know about. Quiet success with
// zero updates is intentionally silent — every cron fire would otherwise spam
// Telegram with "✅ 成功 N / 更新 0 / 失败 0". Push only when containers were
// actually updated, something failed, a node was offline, or Docker was
// unavailable on a node.
func containerUpdateReportWorthSending(stats containerUpdateStats) bool {
	return stats.updated > 0 ||
		stats.failed > 0 ||
		len(stats.failures) > 0 ||
		len(stats.offline) > 0 ||
		len(stats.unavailable) > 0
}

func formatContainerUpdateReport(stats containerUpdateStats) string {
	status := "✅"
	if len(stats.offline) > 0 || len(stats.failures) > 0 || stats.failed > 0 {
		status = "❌"
	} else if len(stats.unavailable) > 0 {
		status = "⚠️"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s 容器定时更新\n", status)
	fmt.Fprintf(&b, "节点 成功 %d · 离线 %d\n", stats.succeeded, len(stats.offline))
	fmt.Fprintf(&b, "容器 更新 %d · 失败 %d", stats.updated, stats.failed)

	// Append the concrete node/container lines so the operator can see what
	// actually changed, instead of an opaque count that looks the same every run.
	// When the version transition is known (a real tag upgrade), show
	// "node/name  oldTag → newTag" so the bump is visible at a glance.
	if len(stats.updatedNames) > 0 {
		transitions := make(map[string]string, len(stats.upgraded))
		for _, u := range stats.upgraded {
			if u.from == "" && u.to == "" {
				continue
			}
			transitions[u.nodeID+"/"+u.name] = u.from + " → " + u.to
		}
		names := append([]string(nil), stats.updatedNames...)
		sort.Strings(names)
		b.WriteString("\n\n更新:")
		for _, ref := range names {
			b.WriteString("\n· ")
			b.WriteString(formatNodeContainerRef(ref, stats.nodeNames))
			if t := transitions[ref]; t != "" {
				b.WriteString("  " + t)
			}
		}
	}
	if len(stats.offline) > 0 {
		ids := append([]string(nil), stats.offline...)
		sort.Strings(ids)
		b.WriteString("\n\n离线:")
		for _, id := range ids {
			b.WriteString("\n· ")
			b.WriteString(displayNodeName(id, stats.nodeNames))
		}
	}
	if len(stats.unavailable) > 0 {
		ids := append([]string(nil), stats.unavailable...)
		sort.Strings(ids)
		b.WriteString("\n\n无 Docker:")
		for _, id := range ids {
			b.WriteString("\n· ")
			b.WriteString(displayNodeName(id, stats.nodeNames))
		}
	}
	if len(stats.failures) > 0 {
		lines := append([]string(nil), stats.failures...)
		sort.Strings(lines)
		b.WriteString("\n\n失败:")
		for _, line := range lines {
			b.WriteString("\n· ")
			b.WriteString(formatFailureLine(line, stats.nodeNames))
		}
	}
	return b.String()
}

// imageTagOf returns the tag portion of an image reference: the substring after
// the last ':' that follows the last '/'. "ghcr.io/x/y:3.0.1" -> "3.0.1". Returns
// "" for refs without an explicit tag. The "last '/' precedes the tag" rule keeps
// port-bearing registries ("localhost:5000/x/y:tag") correct.
func imageTagOf(ref string) string {
	if ref == "" {
		return ""
	}
	colon := strings.LastIndex(ref, ":")
	if colon < 0 {
		return ""
	}
	if slash := strings.LastIndex(ref, "/"); colon < slash {
		return ""
	}
	return ref[colon+1:]
}

// displayNodeName maps a node ID to its panel display name when known.
func displayNodeName(nodeID string, names map[string]string) string {
	if names != nil {
		if n := names[nodeID]; n != "" {
			return n
		}
	}
	return nodeID
}

// formatNodeContainerRef rewrites "nodeID/container" using the node display name.
func formatNodeContainerRef(ref string, names map[string]string) string {
	nodeID, container, ok := strings.Cut(ref, "/")
	if !ok {
		return ref
	}
	return displayNodeName(nodeID, names) + "/" + container
}

// formatFailureLine rewrites the nodeID prefix of a failure line
// ("nodeID/container: reason" or "nodeID: reason") to the node display name.
func formatFailureLine(line string, names map[string]string) string {
	// "nodeID/container: reason"
	if slash := strings.IndexByte(line, '/'); slash > 0 {
		nodeID := line[:slash]
		return displayNodeName(nodeID, names) + line[slash:]
	}
	// "nodeID: reason"
	if colon := strings.IndexByte(line, ':'); colon > 0 {
		nodeID := line[:colon]
		return displayNodeName(nodeID, names) + line[colon:]
	}
	return line
}

// registryEligibleUpdateType reports whether a container's image can be checked
// against a remote registry. Locally built (build), purely local, and
// digest-pinned images have no comparable registry tag, so an "unknown" scan
// result for them is expected rather than a real failure. Mirrors the
// agent-side registryScanEligible classification (latest/tag/unmanaged).
func registryEligibleUpdateType(updateType string) bool {
	switch updateType {
	case "latest", "tag", "unmanaged":
		return true
	default:
		return false
	}
}

func isMissingDockerSocketScanError(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "/var/run/docker.sock") &&
		strings.Contains(message, "no such file or directory")
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, value := range in {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
