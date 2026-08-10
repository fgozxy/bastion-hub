// Package backup orchestrates directory backup/restore across nodes and
// storage targets, with retention enforcement.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/browserhub"
	"nodepanel/master/internal/config"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/master/internal/targets"
	"nodepanel/shared/proto"
)

// Notifier sends a human-readable notification (e.g. Telegram).
type Notifier interface {
	Notify(ctx context.Context, msg string)
}

type Service struct {
	Store   *store.Store
	Hub     *agenthub.Hub
	Browser *browserhub.Hub
	Cfg     config.Config
	Notify  Notifier
}

const ResticIncrementalTargetID = "restic-incremental"

func (s *Service) baseURL() string {
	if u, _ := s.Store.GetSetting(context.Background(), "public_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	if s.Cfg.Domain != "" {
		return "https://" + s.Cfg.Domain
	}
	return "http://localhost" + s.Cfg.DevAddr
}

func (s *Service) saver() targets.ConfigSaver {
	return func(targetID, configJSON string) error {
		return s.Store.UpdateTargetConfig(context.Background(), targetID, configJSON)
	}
}

// BackupNow POST /api/backups/now {node_id, paths, container, target_id, name}
func (s *Service) BackupNow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID    string   `json:"node_id"`
		Paths     []string `json:"paths"`
		Container string   `json:"container"`
		TargetID  string   `json:"target_id"`
		TargetIDs []string `json:"target_ids"`
		Name      string   `json:"name"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" || (len(body.Paths) == 0 && body.Container == "") {
		httpx.Err(w, 400, "node_id and (paths or container) are required")
		return
	}
	var targetIDs []string
	seenTarget := map[string]bool{}
	for _, tid := range append(body.TargetIDs, body.TargetID) {
		tid = strings.TrimSpace(tid)
		if tid == "" || seenTarget[tid] {
			continue
		}
		seenTarget[tid] = true
		targetIDs = append(targetIDs, tid)
	}
	id, err := s.TriggerBackup(r.Context(), body.NodeID, body.Paths, body.Container, targetIDs, body.Name, "admin")
	if err != nil {
		status := 500
		if err == ErrNodeOffline {
			status = 409
		} else if err == ErrNodeNotFound {
			status = 404
		}
		httpx.Err(w, status, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"id": id})
}

// ErrNodeOffline / ErrNodeNotFound are returned by TriggerBackup.
var (
	ErrNodeOffline  = httpErrorConflict("node is offline")
	ErrNodeNotFound = httpErrorNotFound("node not found")
	// ErrAgentDisconnected marks a backup that died because the agent's
	// websocket dropped mid-flight. The scheduler treats it like
	// ErrNodeOffline — transient, retry once the node reconnects.
	ErrAgentDisconnected = errors.New("agent disconnected")
)

// Restore job statuses (tri-state terminal + running).
const (
	statusRunning = "running"
	statusOK      = "ok"      // data restored AND container recreated
	statusPartial = "partial" // data restored, container intentionally not rebuilt
	statusFailed  = "failed"  // download/extract failed, OR recreate requested+failed
)

// versionAtLeast reports whether a semver-ish "major.minor" string is >= maj.min.
// Empty/unparseable versions parse as 0.0.
func versionAtLeast(v string, maj, min int) bool {
	var a, b int
	if _, err := fmt.Sscanf(v, "%d.%d", &a, &b); err != nil {
		return false
	}
	return a > maj || (a == maj && b >= min)
}

// imageFromManifest extracts the image ref from a backup's manifest JSON (cosmetic).
func imageFromManifest(manifest string) string {
	if manifest == "" {
		return ""
	}
	var m proto.BackupManifest
	if json.Unmarshal([]byte(manifest), &m) == nil {
		return m.Image
	}
	return ""
}

type httpError struct {
	msg    string
	status int
}

func (e httpError) Error() string          { return e.msg }
func httpErrorConflict(s string) httpError { return httpError{s, 409} }
func httpErrorNotFound(s string) httpError { return httpError{s, 404} }

// TriggerBackup creates and starts a backup job (used by the HTTP "backup now"
// handler). The job runs to completion in a background goroutine and, on
// completion, pushes a one-row node×target status report to the notifier. The
// scheduler does not use this — it calls RunBackupSync so it can aggregate a
// whole schedule's units into a single report.
//
// targetIDs may hold several storage targets — the single archive is pushed to
// each (tar once, push many). If container != "", the agent backs up that
// container's volumes + config instead of the given paths.
func (s *Service) TriggerBackup(ctx context.Context, nodeID string, paths []string, container string, targetIDs []string, name, actor string) (string, error) {
	node, tgts, b, err := s.createBackupJob(ctx, nodeID, paths, container, targetIDs, name, actor)
	if err != nil {
		return "", err
	}
	go func() {
		ur, _ := s.runBackup(b, node, tgts, paths, container)
		if s.Notify == nil {
			return
		}
		title := "✅ 手动备份成功"
		if !ur.TarOK {
			title = "❌ 手动备份失败"
		} else {
			for _, ok := range ur.TargetOK {
				if !ok {
					title = "⚠️ 手动备份部分成功"
					break
				}
			}
		}
		title = fmt.Sprintf("%s · %s（%s）", title, b.Name, humanSize(b.Size))
		tis := make([]TargetInfo, len(tgts))
		for i, t := range tgts {
			tis[i] = TargetInfo{ID: t.ID, Type: t.Type, Name: t.Name}
		}
		s.Notify.Notify(context.Background(), FormatBackupReport(title, []UnitResult{ur}, tis))
	}()
	return b.ID, nil
}

// RunBackupSync creates and runs a single backup job to completion synchronously
// and returns its per-target outcome WITHOUT notifying — the scheduler aggregates
// many units (an entire schedule) into one report. A node that is offline or
// missing still yields a UnitResult (every target 🔴) so the report shows it.
func (s *Service) RunBackupSync(ctx context.Context, nodeID string, paths []string, container string, targetIDs []string, name, actor string) (UnitResult, error) {
	node, tgts, b, err := s.createBackupJob(ctx, nodeID, paths, container, targetIDs, name, actor)
	if err != nil {
		nm := ""
		if node != nil {
			nm = node.Name
		}
		return UnitResult{NodeID: nodeID, NodeName: nm, TarOK: false, Err: err.Error(), TargetOK: zeroTargetOK(tgts, targetIDs), TargetIDs: append([]string(nil), targetIDs...)}, err
	}
	return s.runBackup(b, node, tgts, paths, container)
}

// RunResticContainerBackupSync runs the node-local container-restic wrapper for
// one configured container. It keeps NodePanel as the scheduler/status source by
// persisting a backup row and returning a UnitResult, but the bytes are written
// by restic on the node instead of being tarred through the master.
func (s *Service) RunResticContainerBackupSync(ctx context.Context, nodeID, containerID, containerName, name, actor string, timeout time.Duration) (UnitResult, error) {
	node, err := s.Store.GetNode(ctx, nodeID)
	if err != nil {
		return UnitResult{NodeID: nodeID, TarOK: false, Err: ErrNodeNotFound.Error(), TargetOK: map[string]bool{ResticIncrementalTargetID: false}, TargetIDs: []string{ResticIncrementalTargetID}}, ErrNodeNotFound
	}
	ur := UnitResult{NodeID: node.ID, NodeName: node.Name, TarOK: false, TargetOK: map[string]bool{ResticIncrementalTargetID: false}, TargetIDs: []string{ResticIncrementalTargetID}}
	if !s.Hub.Online(node.ID) {
		ur.Err = ErrNodeOffline.Error()
		return ur, ErrNodeOffline
	}
	if containerName == "" {
		containerName = containerID
	}
	if name == "" {
		name = "restic-container-" + time.Now().Format("20060102-150405")
	}
	pathsJSON, _ := json.Marshal([]string{"restic:/root/container-restic"})
	b := &store.Backup{
		ID: uuid.NewString(), NodeID: node.ID, Name: name, Paths: string(pathsJSON), Container: containerID,
		ContainerName: containerName, Target: ResticIncrementalTargetID, StagePath: "", Status: "running",
	}
	if err := s.Store.CreateBackup(ctx, b); err != nil {
		ur.Err = err.Error()
		return ur, err
	}
	s.Store.Audit(ctx, actor, "backup.restic.start", node.Name+"/"+containerName)

	if timeout <= 0 {
		timeout = 8 * time.Hour
	}
	cmd := fmt.Sprintf("CONTAINER_RESTIC_BASE=%s %s backup %s",
		shellQuote("/root/container-restic"),
		shellQuote("/root/container-restic/container-restic.sh"),
		shellQuote(containerName),
	)
	out, exit, err := s.execAgentSync(ctx, node.ID, "restic-backup", cmd, timeout)
	if err != nil {
		b.Status = "failed"
		b.Error = err.Error()
		ur.Err = b.Error
		if err == agenthub.ErrOffline {
			ur.Err = ErrNodeOffline.Error()
			_ = s.Store.DeleteBackup(context.Background(), b.ID)
			return ur, ErrNodeOffline
		}
		if err == ErrAgentDisconnected {
			// Transient: let the scheduler retry once the node reconnects.
			return ur, ErrAgentDisconnected
		}
	} else if exit != 0 {
		b.Status = "failed"
		b.Error = strings.TrimSpace(tailString(out, 1600))
		if b.Error == "" {
			b.Error = fmt.Sprintf("restic exit %d", exit)
		} else {
			b.Error = fmt.Sprintf("restic exit %d: %s", exit, b.Error)
		}
		ur.Err = b.Error
	} else {
		b.Status = "ok"
		b.Size = parseResticAddedBytes(out)
		ur.TarOK = true
		ur.TargetOK[ResticIncrementalTargetID] = true
	}
	_ = s.Store.UpdateBackup(context.Background(), b)
	s.broadcast(b)
	if b.Status != "ok" {
		return ur, fmt.Errorf("%s", ur.Err)
	}
	return ur, nil
}

func (s *Service) execAgentSync(ctx context.Context, nodeID, purpose, cmd string, timeout time.Duration) (string, int, error) {
	reqID := purpose + ":" + nodeID + ":" + uuid.NewString()
	ch := s.Hub.Subscribe(reqID)
	defer s.Hub.Unsubscribe(reqID)
	env, err := proto.Encode(proto.MsgExec, reqID, proto.ExecRequest{Cmd: cmd, Timeout: int(timeout.Seconds())})
	if err != nil {
		return "", -1, err
	}
	if err := s.Hub.Send(nodeID, env); err != nil {
		return "", -1, err
	}
	timer := time.NewTimer(timeout + 8*time.Second)
	defer timer.Stop()
	var sb strings.Builder
	exit := 0
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				// The agent's websocket dropped mid-exec: the command's
				// outcome is unknown, so this must NOT masquerade as a
				// clean exit 0 (that marked interrupted restic backups OK).
				return sb.String(), exit, ErrAgentDisconnected
			}
			if msg.Type != proto.MsgExecOutput {
				continue
			}
			var out proto.ExecOutput
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &out)
			}
			if out.Data != "" {
				sb.WriteString(out.Data)
			}
			if out.Done {
				return sb.String(), out.Exit, nil
			}
		case <-timer.C:
			return sb.String(), exit, fmt.Errorf("执行超时")
		case <-ctx.Done():
			return sb.String(), exit, ctx.Err()
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func tailString(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func parseResticAddedBytes(out string) int64 {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "Added to the repository:") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f != "repository:" || i+2 >= len(fields) {
				continue
			}
			v, err := strconv.ParseFloat(strings.Trim(fields[i+1], "(),"), 64)
			if err != nil {
				return 0
			}
			return int64(v * resticUnitMultiplier(strings.Trim(fields[i+2], "(),")))
		}
	}
	return 0
}

func resticUnitMultiplier(unit string) float64 {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "b":
		return 1
	case "kib":
		return 1024
	case "mib":
		return 1024 * 1024
	case "gib":
		return 1024 * 1024 * 1024
	case "tib":
		return 1024 * 1024 * 1024 * 1024
	case "kb":
		return 1000
	case "mb":
		return 1000 * 1000
	case "gb":
		return 1000 * 1000 * 1000
	case "tb":
		return 1000 * 1000 * 1000 * 1000
	default:
		return 0
	}
}

// createBackupJob validates the node, resolves targets, persists a "running"
// backup row, and audits the start. Shared by TriggerBackup (async) and
// RunBackupSync (sync). On a validation error the node is still returned when
// known (non-nil for ErrNodeOffline) so callers can name it in a failure report.
func (s *Service) createBackupJob(ctx context.Context, nodeID string, paths []string, container string, targetIDs []string, name, actor string) (*store.Node, []*store.BackupTarget, *store.Backup, error) {
	node, err := s.Store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, nil, nil, ErrNodeNotFound
	}
	if !s.Hub.Online(node.ID) {
		return node, nil, nil, ErrNodeOffline
	}
	var tgts []*store.BackupTarget
	for _, tid := range targetIDs {
		if t, _ := s.Store.GetTarget(ctx, tid); t != nil {
			tgts = append(tgts, t)
		}
	}
	bid := uuid.NewString()
	stage := filepath.Join(s.Cfg.DataDir, "backups", node.ID, bid+".tar.gz")
	pathsJSON, _ := json.Marshal(paths)
	if name == "" {
		name = backupDefaultName(container)
	}
	// Persist the stable container name so the restore view can join backups by
	// name after the container is recreated with a new id (the id changes on
	// every recreate; ContainerIDByName already decouples schedules — this
	// extends the same idea to backup records).
	cname := ""
	if container != "" {
		cname = s.Store.ContainerNameByID(ctx, node.ID, container)
	}
	b := &store.Backup{
		ID: bid, NodeID: node.ID, Name: name, Paths: string(pathsJSON),
		Container: container, ContainerName: cname, Target: strings.Join(targetIDs, ","), StagePath: stage, Status: "running",
	}
	if err := s.Store.CreateBackup(ctx, b); err != nil {
		return node, tgts, nil, err
	}
	s.Store.Audit(ctx, actor, "backup.start", node.Name)
	return node, tgts, b, nil
}

func backupDefaultName(container string) string {
	if container != "" {
		return "container-" + time.Now().Format("20060102-150405")
	}
	return "backup-" + time.Now().Format("20060102-150405")
}

func directS3UploadConfig(node *store.Node, tgts []*store.BackupTarget, remoteName string) (string, *proto.S3UploadConfig) {
	if node == nil || !versionAtLeast(node.AgentVersion, 2, 2) || len(tgts) != 1 || tgts[0].Type != "s3" {
		return "", nil
	}
	var c targets.S3Config
	if err := json.Unmarshal([]byte(tgts[0].Config), &c); err != nil {
		return "", nil
	}
	if c.Endpoint == "" || c.Bucket == "" {
		return "", nil
	}
	return tgts[0].ID, &proto.S3UploadConfig{
		Endpoint:           c.Endpoint,
		AccessKey:          c.AccessKey,
		SecretKey:          c.SecretKey,
		Bucket:             c.Bucket,
		Object:             targets.S3ObjectName(c, remoteName),
		Region:             c.Region,
		PathStyle:          c.PathStyle,
		Secure:             c.Secure,
		InsecureSkipVerify: c.InsecureSkipVerify,
	}
}

// zeroTargetOK returns a map with every target id mapped to false — used when no
// archive is produced (node offline/missing), so every report cell shows 🔴.
func zeroTargetOK(tgts []*store.BackupTarget, targetIDs []string) map[string]bool {
	m := map[string]bool{}
	for _, t := range tgts {
		m[t.ID] = false
	}
	for _, id := range targetIDs {
		m[id] = false
	}
	return m
}

func splitTargetIDs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range strings.Split(s, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// runBackup runs one backup job to completion: it streams the agent's archive
// onto local staging, then pushes that single archive to every target (best
// effort; one archive, many targets). It returns a per-target outcome for the
// notifier and never notifies itself — TriggerBackup (manual) or the scheduler
// (batched) own the report. The error is the archive-stage failure (e.g.
// ErrAgentDisconnected), nil once an archive exists — per-target push failures
// are reported only via UnitResult.TargetOK.
func (s *Service) runBackup(b *store.Backup, node *store.Node, tgts []*store.BackupTarget, paths []string, container string) (UnitResult, error) {
	ctx := context.Background()
	targetIDs := splitTargetIDs(b.Target)
	ur := UnitResult{NodeID: node.ID, NodeName: node.Name, TargetOK: map[string]bool{}, TargetIDs: targetIDs}
	for _, t := range tgts {
		ur.TargetOK[t.ID] = false
	}

	remoteName := b.NodeID + "/" + b.ID + ".tar.gz"
	directTargetID, directS3 := directS3UploadConfig(node, tgts, remoteName)
	upload := s.baseURL() + "/api/agent/upload?id=" + b.ID + "&token=" + url.QueryEscape(node.AgentToken)
	// Host-path prefixes to skip (e.g. nodepanel shedding its circular backups/
	// agents/ dirs). Read once per backup from the global backup_excludes setting.
	var excludes []string
	if raw, _ := s.Store.GetSetting(ctx, "backup_excludes"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &excludes)
	}
	runAgentBackup := func(reqID string, s3Upload *proto.S3UploadConfig) (proto.BackupResult, error) {
		ch := s.Hub.Subscribe(reqID)
		env, _ := proto.Encode(proto.MsgBackup, reqID, proto.BackupRequest{
			Paths: paths, Container: container, Upload: upload, Token: node.AgentToken, Exclude: excludes, S3Upload: s3Upload,
		})
		if err := s.Hub.Send(node.ID, env); err != nil {
			s.Hub.Unsubscribe(reqID)
			return proto.BackupResult{}, fmt.Errorf("node unreachable: %w", err)
		}

		var result proto.BackupResult
		select {
		case msg, ok := <-ch:
			s.Hub.Unsubscribe(reqID)
			if !ok {
				return result, ErrAgentDisconnected
			}
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &result)
			}
		case <-time.After(8 * time.Hour):
			s.Hub.Unsubscribe(reqID)
			return result, fmt.Errorf("timeout")
		}
		if !result.OK {
			if result.Err != "" {
				return result, fmt.Errorf("%s", result.Err)
			}
			return result, fmt.Errorf("agent backup failed")
		}
		return result, nil
	}

	result, runErr := runAgentBackup("backup:"+b.ID, directS3)
	if runErr != nil && directS3 != nil {
		// Some nodes cannot reach a private MinIO endpoint directly. Keep the job
		// reliable by falling back to the old agent -> master -> target path.
		directTargetID = ""
		_ = os.Remove(b.StagePath)
		result, runErr = runAgentBackup("backup:"+b.ID+":fallback", nil)
	}

	if runErr != nil {
		b.Status = "failed"
		b.Error = runErr.Error()
	} else {
		if result.Size > 0 {
			b.Size = result.Size
		} else if fi, err := os.Stat(b.StagePath); err == nil {
			b.Size = fi.Size()
		}
		b.Status = "ok"
		if len(result.Manifest) > 0 {
			b.Manifest = string(result.Manifest) // footprint for preflight
		}
	}
	_ = s.Store.UpdateBackup(ctx, b)

	if b.Status == "ok" {
		if directTargetID != "" {
			ur.TargetOK[directTargetID] = true
			s.applyRetention(ctx, b.NodeID)
			ur.TarOK = true
			s.broadcast(b)
			return ur, nil
		}
		// Push to every target independently. Each target has its own global
		// single-flight limiter and retry loop, so a schedule can fan out across
		// targets without stampeding the same SFTP/S3/GitHub/Graph endpoint.
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			pushErrs []string
		)
		for _, t := range tgts {
			t := t
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.pushArchiveReliable(ctx, t, b.StagePath, remoteName)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					pushErrs = append(pushErrs, t.Name+": "+err.Error())
					ur.TargetOK[t.ID] = false
					return
				}
				ur.TargetOK[t.ID] = true
			}()
		}
		wg.Wait()
		if len(pushErrs) > 0 {
			b.Error = "部分目标推送失败：" + strings.Join(pushErrs, "；")
			_ = s.Store.UpdateBackup(ctx, b)
		}
		// At least one target now holds the archive, so the local stage is just
		// the upload source. Evict it to stop unbounded accumulation (this was
		// leaking tens of GB onto the master disk) — ensureStaged() pulls it
		// back from a target on restore. Keep it only if every target failed
		// (len(pushErrs) == len(tgts)), since then no remote copy exists yet.
		if len(tgts) > 0 && len(pushErrs) < len(tgts) {
			_ = os.Remove(b.StagePath)
		}
		s.applyRetention(ctx, b.NodeID)
		ur.TarOK = true
	} else {
		ur.Err = b.Error
		// No usable archive was produced (agent/timeout failure). Drop the
		// partial stage so it doesn't pile up — nothing was pushed anywhere.
		_ = os.Remove(b.StagePath)
	}
	s.broadcast(b)
	return ur, runErr
}

// Restore POST /api/backups/{id}/restore {node_id, dest}
func (s *Service) Restore(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		NodeID string `json:"node_id"`
		Dest   string `json:"dest"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" || body.Dest == "" {
		httpx.Err(w, 400, "node_id and dest are required")
		return
	}
	b, err := s.Store.GetBackup(r.Context(), id)
	if err != nil {
		httpx.Err(w, 404, "backup not found")
		return
	}
	destNode, err := s.Store.GetNode(r.Context(), body.NodeID)
	if err != nil {
		httpx.Err(w, 404, "target node not found")
		return
	}
	if !s.Hub.Online(destNode.ID) {
		httpx.Err(w, 409, "target node is offline")
		return
	}
	// ensure staged archive exists locally (pull from a target if evicted)
	if err := s.ensureStaged(r.Context(), b); err != nil {
		httpx.Err(w, 400, err.Error())
		return
	}
	go s.runRestore(b, destNode, body.Dest)
	httpx.OK(w, map[string]string{"id": id})
}

// ensureStaged makes sure the backup's local staged archive exists, pulling it
// back from one of its storage targets if it was evicted locally. b.Target is a
// comma-joined list of target IDs (one archive pushed to every target), so we
// try each until one serves the file — this fixes the earlier bug where the
// whole comma-joined string was passed to GetTarget (which matches a single id),
// so every multi-target restore failed once the stage file was gone.
func (s *Service) ensureStaged(ctx context.Context, b *store.Backup) error {
	if b.StagePath == "" {
		return fmt.Errorf("backup has no staged archive")
	}
	if _, err := os.Stat(b.StagePath); err == nil {
		return nil
	}
	if b.Target == "" {
		return fmt.Errorf("staged archive missing and no target to pull from")
	}
	var lastErr error
	for _, tid := range strings.Split(b.Target, ",") {
		tid = strings.TrimSpace(tid)
		if tid == "" {
			continue
		}
		tg, err := s.Store.GetTarget(ctx, tid)
		if tg == nil || err != nil {
			lastErr = fmt.Errorf("target %s unavailable", tid)
			continue
		}
		up, err := targets.New(tg, s.saver())
		if err != nil {
			lastErr = err
			continue
		}
		_ = os.MkdirAll(filepath.Dir(b.StagePath), 0o755)
		if err := up.Pull(ctx, b.NodeID+"/"+b.ID+".tar.gz", b.StagePath); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no target could serve the archive")
	}
	return lastErr
}

func (s *Service) runRestore(b *store.Backup, destNode *store.Node, dest string) {
	ctx := context.Background()
	reqID := "restore:" + b.ID + ":" + destNode.ID
	ch := s.Hub.Subscribe(reqID)
	download := s.baseURL() + "/api/agent/dl?id=" + b.ID + "&token=" + url.QueryEscape(destNode.AgentToken)
	env, _ := proto.Encode(proto.MsgRestore, reqID, proto.RestoreRequest{
		Download: download, Token: destNode.AgentToken, Dest: dest,
	})
	if err := s.Hub.Send(destNode.ID, env); err != nil {
		s.Hub.Unsubscribe(reqID)
		if s.Notify != nil {
			s.Notify.Notify(ctx, "❌ 恢复失败: "+destNode.Name+" — 节点离线")
		}
		return
	}
	var result proto.RestoreResult
	select {
	case msg, ok := <-ch:
		s.Hub.Unsubscribe(reqID)
		if ok && len(msg.Data) > 0 {
			_ = json.Unmarshal(msg.Data, &result)
		}
	case <-time.After(8 * time.Hour):
		s.Hub.Unsubscribe(reqID)
	}
	if s.Notify != nil {
		if result.OK {
			s.Notify.Notify(ctx, "✅ 恢复成功: "+b.Name+" -> "+destNode.Name+":"+dest)
		} else {
			s.Notify.Notify(ctx, "❌ 恢复失败: "+b.Name+" -> "+destNode.Name+" — "+result.Err)
		}
	}
}

// restoreItem is one deduplicated container in the restore view: the best
// (newest, then largest) backup for a container name, merged across nodes.
type restoreItem struct {
	Container       string   `json:"container"`
	DisplayName     string   `json:"display_name"`
	Image           string   `json:"image"`
	OriginNodeID    string   `json:"origin_node_id"`
	OriginNodeName  string   `json:"origin_node_name"`
	BackupID        string   `json:"backup_id"`
	Size            int64    `json:"size"`
	CreatedAt       int64    `json:"created_at"`
	Targets         []string `json:"targets"`
	SnapshotsMerged int      `json:"snapshots_merged"`
	SourceNodes     []string `json:"source_nodes"`
}

// RestoreContainersList GET /api/backups/containers/restore — the container
// restore view: every restorable container, deduped by name (merged across
// nodes), keeping only the newest/most-complete backup per container.
func (s *Service) RestoreContainersList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListContainerBackupsForRestore(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	tgts, _ := s.Store.ListTargets(r.Context())
	targetName := map[string]string{}
	for _, t := range tgts {
		targetName[t.ID] = t.Name
	}

	nodeNames := map[string]string{}
	nodeName := func(id string) string {
		if n, ok := nodeNames[id]; ok {
			return n
		}
		name := shortID(id)
		if nd, err := s.Store.GetNode(r.Context(), id); err == nil {
			name = nd.Name
		}
		nodeNames[id] = name
		return name
	}
	keyOf := func(row store.ContainerBackupRow) string {
		return row.Name // ListContainerBackupsForRestore only returns live containers
	}
	displayOf := func(row store.ContainerBackupRow) string {
		if row.DisplayName != "" {
			return row.DisplayName
		}
		return row.Name
	}

	type group struct {
		best     store.ContainerBackupRow
		merged   int
		srcNodes map[string]bool
	}
	groups := map[string]*group{}
	var order []string
	for _, row := range rows { // already DESC by created_at
		k := keyOf(row)
		g, ok := groups[k]
		if !ok {
			g = &group{best: row, srcNodes: map[string]bool{row.NodeID: true}}
			groups[k] = g
			order = append(order, k)
			continue
		}
		g.merged++
		g.srcNodes[row.NodeID] = true
		// rows are newest-first; only replace the best on a timestamp tie, by
		// larger size (more complete data).
		if row.CreatedAt == g.best.CreatedAt && row.Size > g.best.Size {
			g.best = row
		}
	}

	out := make([]restoreItem, 0, len(order))
	for _, k := range order {
		g := groups[k]
		best := g.best
		var tnames []string
		for _, tid := range strings.Split(best.Target, ",") {
			tid = strings.TrimSpace(tid)
			if tid == "" {
				continue
			}
			if n := targetName[tid]; n != "" {
				tnames = append(tnames, n)
			} else {
				tnames = append(tnames, shortID(tid))
			}
		}
		var src []string
		for id := range g.srcNodes {
			src = append(src, nodeName(id))
		}
		sort.Strings(src)
		out = append(out, restoreItem{
			Container: k, DisplayName: displayOf(best), Image: best.Image,
			OriginNodeID: best.NodeID, OriginNodeName: nodeName(best.NodeID),
			BackupID: best.ID, Size: best.Size, CreatedAt: best.CreatedAt,
			Targets: tnames, SnapshotsMerged: g.merged + 1, SourceNodes: src,
		})
	}
	httpx.OK(w, out)
}

// ContainerBackupsByName GET /api/restore/container-backups?name=X — every
// successful snapshot of one container (newest first), joined with image /
// display name. Powers the restore view's expandable per-container history so a
// user can pick an older point-in-time backup instead of only the newest.
func (s *Service) ContainerBackupsByName(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		httpx.Err(w, 400, "name is required")
		return
	}
	rows, err := s.Store.ListContainerBackupsByName(r.Context(), name)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	tgts, _ := s.Store.ListTargets(r.Context())
	targetName := map[string]string{}
	for _, t := range tgts {
		targetName[t.ID] = t.Name
	}
	nodeName := func(id string) string {
		if n, err := s.Store.GetNode(r.Context(), id); err == nil {
			return n.Name
		}
		return shortID(id)
	}
	type snapshot struct {
		store.ContainerBackupRow
		OriginNodeName string   `json:"origin_node_name"`
		Targets        []string `json:"targets"`
	}
	out := make([]snapshot, 0, len(rows))
	for _, row := range rows {
		var tnames []string
		for _, tid := range strings.Split(row.Target, ",") {
			tid = strings.TrimSpace(tid)
			if tid == "" {
				continue
			}
			if n := targetName[tid]; n != "" {
				tnames = append(tnames, n)
			} else {
				tnames = append(tnames, shortID(tid))
			}
		}
		out = append(out, snapshot{ContainerBackupRow: row, OriginNodeName: nodeName(row.NodeID), Targets: tnames})
	}
	httpx.OK(w, out)
}

// ListRestoreJobs GET /api/restore/jobs — recent restore jobs (history), with
// target/origin node names resolved. Survives page refresh (persisted).
func (s *Service) ListRestoreJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Store.ListRestoreJobs(r.Context(), 200)
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
		store.RestoreJob
		TargetNodeName string `json:"target_node_name"`
		OriginNodeName string `json:"origin_node_name"`
	}
	out := make([]jobOut, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobOut{RestoreJob: j, TargetNodeName: nodeName(j.TargetNode), OriginNodeName: nodeName(j.OriginNode)})
	}
	httpx.OK(w, out)
}

// Preflight POST /api/restore/preflight {backup_ids, node_ids} — asks each
// target node (agent >= 1.8.0) to feasibility-check the union of the selected
// backups' footprints (bound ports / bind paths / image / disk) WITHOUT touching
// data, so the UI can warn about port remaps and path overwrites before the
// user commits. Older agents report supports_preflight=false. The preflight is
// advisory: ports/paths may change between check and create (the agent's
// start-failure remap fallback remains the source of truth).
func (s *Service) Preflight(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BackupIDs []string `json:"backup_ids"`
		NodeIDs   []string `json:"node_ids"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.BackupIDs) == 0 || len(body.NodeIDs) == 0 {
		httpx.Err(w, 400, "backup_ids and node_ids are required")
		return
	}

	// Gather each backup's manifest footprint; track which had no manifest.
	type binfo struct {
		id         string
		items      []proto.PreflightItem
		noManifest bool
	}
	var infos []binfo
	for _, bid := range body.BackupIDs {
		b, err := s.Store.GetBackup(r.Context(), bid)
		if err != nil {
			continue
		}
		bi := binfo{id: bid}
		if b.Manifest != "" {
			var m proto.BackupManifest
			if json.Unmarshal([]byte(b.Manifest), &m) == nil {
				bi.items = append(bi.items, m.Ports...)
				bi.items = append(bi.items, m.Binds...)
				bi.items = append(bi.items, proto.PreflightItem{Image: m.Image, Size: m.Size})
			} else {
				bi.noManifest = true
			}
		} else {
			bi.noManifest = true
		}
		infos = append(infos, bi)
	}

	// Union items across backups: dedup ports by proto+port, binds by path; keep
	// image/size items as-is (the agent aggregates them for the disk/image check).
	var items []proto.PreflightItem
	seenPort := map[string]bool{}
	seenBind := map[string]bool{}
	for _, bi := range infos {
		for _, it := range bi.items {
			switch {
			case it.HostPort != "":
				k := it.Proto + "/" + it.HostPort
				if !seenPort[k] {
					seenPort[k] = true
					items = append(items, proto.PreflightItem{HostPort: it.HostPort, Proto: it.Proto})
				}
			case it.BindPath != "":
				if !seenBind[it.BindPath] {
					seenBind[it.BindPath] = true
					items = append(items, proto.PreflightItem{BindPath: it.BindPath})
				}
			default:
				items = append(items, it) // image / size
			}
		}
	}

	type nodeReport struct {
		NodeID            string `json:"node_id"`
		Name              string `json:"name"`
		AgentVersion      string `json:"agent_version"`
		Online            bool   `json:"online"`
		SupportsPreflight bool   `json:"supports_preflight"`
		proto.PreflightResult
	}
	reports := make([]nodeReport, len(body.NodeIDs))
	var wg sync.WaitGroup
	for i, nid := range body.NodeIDs {
		i, nid := i, nid
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep := nodeReport{NodeID: nid}
			n, err := s.Store.GetNode(r.Context(), nid)
			if err == nil {
				rep.Name = n.Name
				rep.AgentVersion = n.AgentVersion
				rep.Online = s.Hub.Online(nid)
			}
			if n == nil || err != nil || !rep.Online || !versionAtLeast(rep.AgentVersion, 1, 8) {
				rep.SupportsPreflight = false
				reports[i] = rep
				return
			}
			rep.SupportsPreflight = true
			reqID := "preflight:" + nid + ":" + uuid.NewString()
			env, _ := proto.Encode(proto.MsgRestorePreflight, reqID, proto.RestorePreflightRequest{Items: items})
			msg, _ := s.Hub.RequestOne(nid, env, 10*time.Second)
			if msg != nil && len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &rep.PreflightResult)
			}
			reports[i] = rep
		}()
	}
	wg.Wait()

	var noManifest []string
	for _, bi := range infos {
		if bi.noManifest {
			noManifest = append(noManifest, bi.id)
		}
	}
	httpx.OK(w, map[string]any{"nodes": reports, "backups_without_manifest": noManifest})
}

type restoreTask struct {
	b    *store.Backup
	name string
}

// RestoreContainers POST /api/backups/containers/restore {node_ids, backup_ids, auto_pull}
// Restores each selected container backup to every selected target node
// (cross-node DR), recreating the container there. Backward compatible with the
// old single-field {node_id}. One RestoreJob row is created per (backup × node).
// Progress streams as "restore.progress"; terminal state as "restore.update"
// (both keyed by job_id); a "restore.jobs" tick refreshes the history list.
func (s *Service) RestoreContainers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID    string   `json:"node_id"`  // backward compat (single node)
		NodeIDs   []string `json:"node_ids"` // multi-target (preferred)
		BackupIDs []string `json:"backup_ids"`
		AutoPull  bool     `json:"auto_pull"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.BackupIDs) == 0 {
		httpx.Err(w, 400, "backup_ids are required")
		return
	}
	nodeIDs := body.NodeIDs
	if len(nodeIDs) == 0 && body.NodeID != "" {
		nodeIDs = []string{body.NodeID}
	}
	if len(nodeIDs) == 0 {
		httpx.Err(w, 400, "node_ids are required")
		return
	}
	var targets []*store.Node
	for _, id := range nodeIDs {
		n, err := s.Store.GetNode(r.Context(), id)
		if err != nil {
			httpx.Err(w, 404, "target node not found: "+id)
			return
		}
		if !s.Hub.Online(n.ID) {
			httpx.Err(w, 409, "target node offline: "+n.Name)
			return
		}
		targets = append(targets, n)
	}
	// resolve a human label (container name) per backup for progress/history
	nameOf := map[string]string{}
	if all, err := s.Store.ListContainerBackupsForRestore(r.Context()); err == nil {
		for _, rr := range all {
			nm := rr.DisplayName
			if nm == "" {
				nm = rr.Name
			}
			nameOf[rr.ID] = nm
		}
	}
	var tasks []restoreTask
	for _, bid := range body.BackupIDs {
		b, err := s.Store.GetBackup(r.Context(), bid)
		if err != nil {
			continue
		}
		nm := nameOf[bid]
		if nm == "" {
			nm = shortID(b.Container)
		}
		tasks = append(tasks, restoreTask{b: b, name: nm})
	}
	if len(tasks) == 0 {
		httpx.Err(w, 404, "no matching backups")
		return
	}
	actor := auth.UserID(r.Context())
	go s.fanOutRestore(targets, tasks, body.AutoPull, actor)
	httpx.OK(w, map[string]int{"started": len(tasks) * len(targets)})
}

// fanOutRestore stages each backup once (shared across targets), then restores
// every task to every target node. Targets run concurrently (bounded); each
// target runs its task list sequentially (shared disk/bandwidth on that host).
func (s *Service) fanOutRestore(targets []*store.Node, tasks []restoreTask, autoPull bool, actor string) {
	ctx := context.Background()
	var stageMu sync.Mutex
	staged := map[string]bool{}
	ensure := func(b *store.Backup) bool {
		stageMu.Lock()
		defer stageMu.Unlock()
		if staged[b.ID] {
			return true
		}
		if err := s.ensureStaged(ctx, b); err != nil {
			return false
		}
		staged[b.ID] = true
		return true
	}

	type nodeTally struct{ ok, fail int }
	tallies := make([]nodeTally, len(targets))
	sem := make(chan struct{}, restoreConcurrency(len(targets)))
	var wg sync.WaitGroup
	for i, t := range targets {
		i, t := i, t
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			tallies[i].ok, tallies[i].fail = s.runNodeRestore(ctx, t, tasks, autoPull, actor, ensure)
		}()
	}
	wg.Wait()

	var ok, fail int
	for _, tn := range tallies {
		ok += tn.ok
		fail += tn.fail
	}
	s.Store.Audit(ctx, actor, "container.restore",
		fmt.Sprintf("%d containers × %d nodes (ok=%d fail=%d)", len(tasks), len(targets), ok, fail))
	if s.Notify != nil {
		status := "✅"
		if fail > 0 {
			status = "⚠️"
		}
		s.Notify.Notify(ctx, fmt.Sprintf("%s 容器恢复 -> %d 节点：成功 %d，失败 %d", status, len(targets), ok, fail))
	}
}

// restoreConcurrency caps simultaneous per-node restore goroutines.
func restoreConcurrency(n int) int {
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

// runNodeRestore runs this node's task list sequentially, recreating each
// container from its backup. Returns (ok, fail) tallies.
func (s *Service) runNodeRestore(ctx context.Context, target *store.Node, tasks []restoreTask, autoPull bool, actor string, ensure func(*store.Backup) bool) (ok, fail int) {
	for _, t := range tasks {
		b := t.b
		job := &store.RestoreJob{
			BackupID: b.ID, Container: t.name, Image: imageFromManifest(b.Manifest),
			OriginNode: b.NodeID, TargetNode: target.ID,
			Status: statusRunning, AgentVersion: target.AgentVersion, Actor: actor,
		}
		_ = s.Store.CreateRestoreJob(ctx, job)
		s.broadcastRestoreJob(job, "restore.update")

		if !ensure(b) {
			fail++
			s.finalizeRestoreJob(ctx, job, statusFailed, "", "staged archive unavailable")
			continue
		}
		reqID := "crestore:" + b.ID + ":" + target.ID
		ch := s.Hub.Subscribe(reqID)
		// Dest only holds the container.json snapshot; volume data is restored
		// to the original mount source paths (read from the archive).
		dest := "/tmp/np-restore-" + b.ID
		download := s.baseURL() + "/api/agent/dl?id=" + b.ID + "&token=" + url.QueryEscape(target.AgentToken)
		env, _ := proto.Encode(proto.MsgRestore, reqID, proto.RestoreRequest{
			Download: download, Token: target.AgentToken, Dest: dest,
			Recreate: true, AutoPull: autoPull,
		})
		if err := s.Hub.Send(target.ID, env); err != nil {
			s.Hub.Unsubscribe(reqID)
			fail++
			s.finalizeRestoreJob(ctx, job, statusFailed, "", "node unreachable: "+err.Error())
			continue
		}
		result, timedOut := s.drainRestore(ctx, reqID, ch, job)
		s.Hub.Unsubscribe(reqID)
		if timedOut {
			fail++
			s.finalizeRestoreJob(ctx, job, statusFailed, "", "恢复超时（2h）")
			continue
		}
		status, detail, errStr := mapRestoreResult(result)
		job.Recreated = result.Recreated
		if status == statusOK {
			ok++
		} else {
			fail++
		}
		s.finalizeRestoreJob(ctx, job, status, detail, errStr)
	}
	return ok, fail
}

// drainRestore consumes streamed MsgRestoreProgress and the terminal
// MsgRestoreResult for a restore request, persisting progress onto the job row
// and broadcasting it. Old agents (<1.8.0) send no progress, so the loop just
// blocks until the single result arrives (or the 2h timeout fires).
func (s *Service) drainRestore(ctx context.Context, reqID string, ch chan *proto.Envelope, job *store.RestoreJob) (proto.RestoreResult, bool) {
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
				_ = s.Store.UpdateRestoreJobProgress(ctx, job.ID, p.Stage, p.BytesDone, p.BytesTotal, int64(p.Percent))
				job.Stage, job.BytesDone, job.BytesTotal, job.Percent = p.Stage, p.BytesDone, p.BytesTotal, int64(p.Percent)
				s.broadcastRestoreJob(job, "restore.progress")
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

// mapRestoreResult turns an agent RestoreResult into a tri-state job status.
//
//	OK=false                          -> failed (download/extract, or recreate
//	                                     requested+failed on a 1.8.0 agent)
//	OK=true & Recreated=true          -> ok     (data restored AND rebuilt)
//	OK=true & Recreated=false & Err!="" -> failed (old 1.7.0 agent that left
//	                                     OK=true on a recreate failure)
//	OK=true & Recreated=false & Err=""  -> partial (intentional data-only)
func mapRestoreResult(result proto.RestoreResult) (status, detail, errStr string) {
	if !result.OK {
		return statusFailed, result.Detail, result.Err
	}
	if result.Recreated {
		return statusOK, result.Detail, ""
	}
	if result.Err != "" {
		return statusFailed, result.Detail, result.Err
	}
	return statusPartial, result.Detail, ""
}

// finalizeRestoreJob sets the terminal columns, persists, and broadcasts the
// outcome + a history refresh tick.
func (s *Service) finalizeRestoreJob(ctx context.Context, job *store.RestoreJob, status, detail, errStr string) {
	job.Status = status
	job.Stage = ""
	job.Detail = detail
	job.Error = errStr
	job.FinishedAt = time.Now().Unix()
	_ = s.Store.UpdateRestoreJob(ctx, job)
	s.broadcastRestoreJob(job, "restore.update")
	s.Browser.Broadcast(browserhub.NewOut("restore.jobs", nil))
}

// broadcastRestoreJob emits a restore job state event keyed by job_id. Used for
// both streaming progress ("restore.progress") and terminal state ("restore.update").
func (s *Service) broadcastRestoreJob(job *store.RestoreJob, event string) {
	s.Browser.Broadcast(browserhub.NewOut(event, map[string]any{
		"job_id": job.ID, "backup_id": job.BackupID, "container": job.Container,
		"image": job.Image, "target_node": job.TargetNode, "status": job.Status,
		"stage": job.Stage, "detail": job.Detail, "error": job.Error,
		"recreated": job.Recreated, "bytes_total": job.BytesTotal,
		"bytes_done": job.BytesDone, "percent": job.Percent,
		"agent_version": job.AgentVersion, "started_at": job.StartedAt, "finished_at": job.FinishedAt,
	}))
}

// shortID returns the first 8 chars of an id (display fallback when no name).
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// TargetInfo pairs a storage target id with its display name for a report.
type TargetInfo struct {
	ID   string
	Type string
	Name string
}

// UnitResult is the outcome of one backup unit (one container or one path-set on
// one node), broken down per storage target so callers can build a node×target
// report. TarOK=false (the archive itself failed) means every target failed.
type UnitResult struct {
	NodeID    string
	NodeName  string
	TarOK     bool
	Err       string
	TargetOK  map[string]bool // targetID -> archive pushed to this target ok
	TargetIDs []string        // target ids this unit was meant to satisfy; empty means all report targets
}

// FormatBackupReport renders an aligned "备份节点 / 存储目标" table: node names in
// the left column, a per-node status right-aligned in the right column, so the
// status column lines up regardless of name length. Status is three-valued so a
// single flaky target no longer turns a whole node red:
//
//	🟢      — every backup cell (unit × target) on that node succeeded
//	🟡 X/Y  — some but not all cells succeeded (e.g. one target blipped for one unit)
//	🔴 0/Y  — nothing succeeded (archive failed for every unit, or node was offline)
//
// The table is a full-width <pre> block: ASCII is widened to full-width so every
// rune is a uniform 2 cells, and columns are padded by rune count — which aligns
// stably across Telegram clients (they measure CJK width inconsistently). The
// title and the failure list sit outside <pre> as normal text. Multiple units on
// one node collapse into one row; the failure list is omitted when all are 🟢.
func FormatBackupReport(title string, units []UnitResult, targets []TargetInfo) string {
	cols := targets
	if len(cols) == 0 {
		cols = []TargetInfo{{ID: "", Name: "备份"}}
	}

	type nodeAgg struct {
		name      string
		count     int
		cellOK    int // successful (unit × target) pushes on this node
		cellTotal int // total (unit × target) cells attempted
	}
	order := make([]string, 0, len(units))
	nodes := map[string]*nodeAgg{}
	for _, u := range units {
		a, ok := nodes[u.NodeID]
		if !ok {
			a = &nodeAgg{}
			nodes[u.NodeID] = a
			order = append(order, u.NodeID)
		}
		a.count++
		if u.NodeName != "" {
			a.name = u.NodeName
		}
		// One cell per storage target this unit was pushed to; a cell needs both
		// the archive (TarOK) and that target's push to count as OK. A unit with no
		// applicable storage target (restic-incremental writes to its own repo, so
		// none of the report's storage targets apply) still counts as one cell on
		// TarOK, so its failure is reflected instead of silently dropped.
		applicable := 0
		for _, t := range cols {
			if t.ID == "" || !unitTargetApplies(u, t.ID) {
				continue
			}
			applicable++
			a.cellTotal++
			if u.TarOK && u.TargetOK[t.ID] {
				a.cellOK++
			}
		}
		if applicable == 0 {
			a.cellTotal++
			if u.TarOK {
				a.cellOK++
			}
		}
	}
	rows := make([]nodeRow, 0, len(order))
	for _, id := range order {
		a := nodes[id]
		name := a.name
		if name == "" {
			name = shortID(id)
		}
		if a.count > 1 {
			name = fmt.Sprintf("%s（%d）", name, a.count)
		}
		rows = append(rows, nodeRow{name, nodeStatus(a.cellOK, a.cellTotal)})
	}

	// A short, deduplicated list of failure reasons — only when something failed,
	// so a fully-🟢 report stays as just the table above.
	var notes []string
	seen := map[string]bool{}
	for _, u := range units {
		var reason string
		if !u.TarOK {
			reason = truncate(u.Err, 60)
			if reason == "" {
				reason = "归档失败"
			}
		} else {
			var bad []string
			for _, t := range targets {
				if unitTargetApplies(u, t.ID) && !u.TargetOK[t.ID] {
					bad = append(bad, t.Name)
				}
			}
			if len(bad) == 0 {
				continue // fully successful — nothing to explain
			}
			reason = "推送失败：" + strings.Join(bad, "、")
		}
		key := u.NodeID + "|" + reason
		if seen[key] {
			continue
		}
		seen[key] = true
		nm := u.NodeName
		if nm == "" {
			nm = shortID(u.NodeID)
		}
		notes = append(notes, nm+"："+reason)
	}

	var msg strings.Builder
	if title != "" {
		msg.WriteString(html.EscapeString(title))
		msg.WriteString("\n")
	}
	msg.WriteString(formatNodeTable(rows)) // <pre>…</pre>, already HTML-safe
	if len(notes) > 0 {
		var f strings.Builder
		f.WriteString("\n\n🔴 失败原因：")
		limit := len(notes)
		if limit > 5 {
			limit = 5
		}
		for i := 0; i < limit; i++ {
			f.WriteString("\n• " + notes[i])
		}
		if len(notes) > 5 {
			f.WriteString(fmt.Sprintf("\n…等共 %d 条", len(notes)))
		}
		msg.WriteString(html.EscapeString(f.String()))
	}
	return msg.String()
}

func unitTargetApplies(u UnitResult, targetID string) bool {
	if targetID == "" || len(u.TargetIDs) == 0 {
		return true
	}
	for _, id := range u.TargetIDs {
		if id == targetID {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// nodeRow is one line of the backup report table: a node label and its single
// status emoji.
type nodeRow struct{ name, status string }

// nodeStatus renders a node's right-column status from its successful/total
// backup cells. Green only when everything succeeded; amber with the fraction
// when some but not all did, so one flaky target no longer reads as a full red
// node; red with 0/N only when nothing succeeded.
func nodeStatus(ok, total int) string {
	if total <= 0 || ok == total {
		return "🟢"
	}
	if ok == 0 {
		return fmt.Sprintf("🔴 0/%d", total)
	}
	return fmt.Sprintf("🟡 %d/%d", ok, total)
}

// formatNodeTable renders the aligned node×status table as a full-width <pre>
// block. Node names form a left column (right-padded); the status emoji forms a
// right column (left-padded) so every emoji lands in the same display column.
// Because every rune is full-width (2 cells) after conversion, padding by rune
// count aligns columns consistently across Telegram clients.
func formatNodeTable(rows []nodeRow) string {
	const (
		headerLeft  = "备份节点"
		headerRight = "存储目标"
	)
	leftW := runeLen(headerLeft)
	for _, r := range rows {
		if w := runeLen(r.name); w > leftW {
			leftW = w
		}
	}
	// The status column now carries "🟡 X/Y" / "🔴 0/N" which are wider than the
	// header, so size the right column to the widest cell and left-pad so every
	// emoji still lands in the same display column.
	rightW := runeLen(headerRight)
	for _, r := range rows {
		if w := runeLen(r.status); w > rightW {
			rightW = w
		}
	}

	var b strings.Builder
	b.WriteString("<pre>")
	writeRow := func(left, right string) {
		b.WriteString(padRightFW(left, leftW))
		b.WriteString("　") // one full-width gap between columns
		b.WriteString(padLeftFW(right, rightW))
	}
	writeRow(headerLeft, headerRight)
	for _, r := range rows {
		b.WriteString("\n")
		writeRow(r.name, r.status)
	}
	b.WriteString("</pre>")
	return b.String()
}

// padRightFW / padLeftFW convert s to full-width, then pad with full-width
// spaces (U+3000) up to w runes. Every rune is 2 cells, so w runes = 2w cells
// uniformly — alignment by rune count is stable. Note toFullwidth leaves the
// rune count unchanged (ASCII just remaps 1:1 to full-width), so padding is
// computed against the original length.
func padRightFW(s string, w int) string {
	fw := toFullwidth(s)
	if n := runeLen(s); n < w {
		fw += strings.Repeat("　", w-n)
	}
	return fw
}

func padLeftFW(s string, w int) string {
	fw := toFullwidth(s)
	if n := runeLen(s); n < w {
		fw = strings.Repeat("　", w-n) + fw
	}
	return fw
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// toFullwidth widens ASCII printable chars to full-width (U+FF01-FF5E) and
// spaces to the full-width space (U+3000), so every rune is a uniform 2 cells
// in a monospace <pre>. CJK and emoji already occupy 2 cells and pass through
// unchanged. This also turns & < > into their full-width forms, making a <pre>
// body HTML-safe without separate escaping.
func toFullwidth(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ':
			b.WriteRune('　')
		case r >= '!' && r <= '~':
			b.WriteRune(r + 0xFEE0) // U+0021..007E -> U+FF01..FF5E
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type retentionCfg struct {
	KeepCount int `json:"keep_count"`
	KeepDays  int `json:"keep_days"`
}

func (s *Service) applyRetention(ctx context.Context, nodeID string) {
	raw, _ := s.Store.GetSetting(ctx, "backup_retention")
	var rc retentionCfg
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &rc)
	}
	// 0 in a dimension means "no limit for that dimension"; both 0 = keep forever.
	if rc.KeepCount == 0 && rc.KeepDays == 0 {
		return
	}
	list, err := s.Store.ListBackups(ctx, nodeID) // already DESC by created_at
	if err != nil {
		return
	}
	now := time.Now().Unix()
	// Keep the newest keep_count versions PER CONTAINER, not per node. A node
	// backs up many containers, so a per-node row count would silently evict
	// whole containers' backups once the node exceeds keep_count entries — and
	// deleteRemote wipes them from every target while the backup report still
	// shows 🟢. Grouping by container makes keep_count mean "N versions of each
	// container", independent of how many containers the node has. keep_days
	// still applies per-backup regardless of container. list is DESC by
	// created_at, so the first keep_count seen for a key are the newest.
	kept := map[string]int{}
	for _, b := range list {
		b := b
		// Container IDs are volatile — every rebuild or image update mints a new
		// one, so keying by b.Container hands each incarnation its own keep_count
		// allowance and target storage grows with every recreate. Prefer the
		// stable container name; fall back to the raw ID only for rows that
		// predate the container_name column.
		key := b.ContainerName
		if key == "" {
			key = b.Container
		}
		if key == "" {
			// Path backup (no container). Keying by b.ID made every row its own
			// group, so keep_count could NEVER evict a path backup and nightly
			// dir archives would pile up on all targets forever. Scheduled dir
			// backups share the schedule's fixed name, so node|name groups one
			// nightly series; fall back to b.ID for ad-hoc unnamed backups.
			if b.Name != "" {
				key = b.NodeID + "|" + b.Name
			} else {
				key = b.ID
			}
		}
		keepByDays := rc.KeepDays > 0 && (now-b.CreatedAt) <= int64(rc.KeepDays)*86400
		keepByCount := rc.KeepCount > 0 && kept[key] < rc.KeepCount
		if keepByDays || keepByCount {
			kept[key]++
			continue
		}
		s.deleteRemoteAsync(b)
		_ = os.Remove(b.StagePath)
		_ = s.Store.DeleteBackup(ctx, b.ID)
	}
}

var retentionDeleteSem = make(chan struct{}, 1)

func (s *Service) deleteRemoteAsync(b store.Backup) {
	go func() {
		retentionDeleteSem <- struct{}{}
		defer func() { <-retentionDeleteSem }()
		s.deleteRemote(context.Background(), b)
	}()
}

// deleteRemote removes the archive from every target this backup was pushed to
// (b.Target is a comma-joined list of target IDs).
func (s *Service) deleteRemote(ctx context.Context, b store.Backup) {
	for _, tid := range strings.Split(b.Target, ",") {
		tid = strings.TrimSpace(tid)
		if tid == "" {
			continue
		}
		if tg, _ := s.Store.GetTarget(ctx, tid); tg != nil {
			if up, err := targets.New(tg, s.saver()); err == nil {
				_ = up.Delete(ctx, b.NodeID+"/"+b.ID+".tar.gz")
			}
		}
	}
}

// List GET /api/backups?node_id=
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	list, err := s.Store.ListBackups(r.Context(), nodeID)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, list)
}

// Delete DELETE /api/backups/{id}
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	b, err := s.Store.GetBackup(r.Context(), id)
	if err == nil {
		s.deleteRemote(r.Context(), *b)
		_ = os.Remove(b.StagePath)
	}
	_ = s.Store.DeleteBackup(r.Context(), id)
	httpx.OK(w, map[string]string{"ok": "1"})
}

func (s *Service) broadcast(b *store.Backup) {
	s.Browser.Broadcast(browserhub.NewOut("backup.update", map[string]any{
		"id": b.ID, "node_id": b.NodeID, "name": b.Name, "size": b.Size,
		"status": b.Status, "error": b.Error, "created_at": b.CreatedAt,
	}))
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
