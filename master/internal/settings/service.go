// Package settings implements the settings, backup-target and schedule
// management endpoints.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/master/internal/targets"
	"nodepanel/master/internal/telegram"
)

type Service struct {
	Store *store.Store
	TG    *telegram.Service
}

// --- general settings ---

// GetAll GET /api/settings
func (s *Service) GetAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all, _ := s.Store.AllSettings(ctx)
	out := map[string]any{}
	for k, v := range all {
		// Never expose a legacy disaster-recovery key left by an older version.
		if k == "peer_key" || k == "cloudflare" || k == "komari" {
			continue
		}
		var parsed any
		if json.Unmarshal([]byte(v), &parsed) == nil {
			out[k] = parsed
		} else {
			out[k] = v
		}
	}
	if uid := auth.UserID(ctx); uid != "" {
		if u, err := s.Store.GetUserByID(ctx, uid); err == nil {
			out["account"] = map[string]string{"username": u.Username}
		}
	}
	if out["account"] == nil {
		out["account"] = map[string]string{"username": ""}
	}
	httpx.OK(w, out)
}

// PutAccount PUT /api/settings/account {username, new_password}
func (s *Service) PutAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		NewPassword string `json:"new_password"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	uid := auth.UserID(r.Context())
	if err := auth.ChangeCredentials(r.Context(), s.Store, uid, body.Username, body.NewPassword); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), body.Username, "account.update", "")
	httpx.OK(w, map[string]string{"ok": "1"})
}

// PutTelegram PUT /api/settings/telegram {bot_token, chat_id}
func (s *Service) PutTelegram(w http.ResponseWriter, r *http.Request) {
	var c telegram.Config
	if err := httpx.ReadJSON(r, &c); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	if err := s.TG.Save(r.Context(), c); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// TestTelegram POST /api/settings/telegram/test
func (s *Service) TestTelegram(w http.ResponseWriter, r *http.Request) {
	var body telegram.Config
	_ = httpx.ReadJSON(r, &body)
	if err := s.TG.Test(r.Context(), body.BotToken, body.ChatID); err != nil {
		httpx.Err(w, 502, "telegram failed: "+err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// PutRetention PUT /api/settings/retention {keep_count, keep_days}
func (s *Service) PutRetention(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeepCount int `json:"keep_count"`
		KeepDays  int `json:"keep_days"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	b, _ := json.Marshal(body)
	_ = s.Store.SetSetting(r.Context(), "backup_retention", string(b))
	httpx.OK(w, map[string]string{"ok": "1"})
}

// PutExcludes PUT /api/settings/excludes {excludes: [...]}
// Host-path prefixes the agent skips when tarring a container's volumes — used
// to shed circular/bloated dirs (e.g. nodepanel's /var/lib/nodepanel/backups).
func (s *Service) PutExcludes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Excludes []string `json:"excludes"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	b, _ := json.Marshal(body.Excludes)
	_ = s.Store.SetSetting(r.Context(), "backup_excludes", string(b))
	httpx.OK(w, map[string]string{"ok": "1"})
}

// PutContainerMonitor PUT /api/settings/container-monitor {enabled, interval_seconds}
// Stores the container-health monitor config: on/off + scan frequency in
// seconds (floored at 30s, the agent's own report period). The monitor service
// reads this setting on each loop, so changes take effect within one cycle.
func (s *Service) PutContainerMonitor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled         bool `json:"enabled"`
		IntervalSeconds int  `json:"interval_seconds"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	if body.IntervalSeconds < 30 {
		body.IntervalSeconds = 60
	}
	b, _ := json.Marshal(map[string]any{"enabled": body.Enabled, "interval_seconds": body.IntervalSeconds})
	_ = s.Store.SetSetting(r.Context(), "container_monitor", string(b))
	httpx.OK(w, map[string]string{"ok": "1"})
}

// --- targets ---

// ListTargets GET /api/targets
func (s *Service) ListTargets(w http.ResponseWriter, r *http.Request) {
	list, _ := s.Store.ListTargets(r.Context())
	httpx.OK(w, list)
}

// CreateTarget POST /api/targets {type,name,config}
func (s *Service) CreateTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type    string          `json:"type"`
		Name    string          `json:"name"`
		Config  json.RawMessage `json:"config"`
		Enabled bool            `json:"enabled"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Type == "" {
		httpx.Err(w, 400, "invalid body")
		return
	}
	t := &store.BackupTarget{Type: body.Type, Name: body.Name, Config: string(body.Config), Enabled: true}
	// GitHub: with just a token (+ optional repo name), auto-derive the owner and
	// ensure the private repo exists, so the user doesn't fill owner/repo/branch.
	if err := s.enrichGithub(r.Context(), t, body.Config); err != nil {
		httpx.Err(w, 400, err.Error())
		return
	}
	if err := s.Store.CreateTarget(r.Context(), t); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, t)
}

// enrichGithub auto-derives owner + ensures the repo exists for a github target
// whose config has no owner (token-only entry). Mutates t.Config on success.
func (s *Service) enrichGithub(ctx context.Context, t *store.BackupTarget, raw json.RawMessage) error {
	if t.Type != "github" {
		return nil
	}
	var gc targets.GithubConfig
	if err := json.Unmarshal(raw, &gc); err != nil {
		return nil
	}
	if strings.TrimSpace(gc.Owner) != "" {
		return nil
	}
	owner, branch, err := targets.ResolveGithub(ctx, gc.Token, gc.Repo)
	if err != nil {
		return err
	}
	gc.Owner = owner
	if gc.Repo == "" {
		gc.Repo = "nodepanel-backups"
	}
	if gc.Branch == "" {
		gc.Branch = branch
	}
	if b, err := json.Marshal(gc); err == nil {
		t.Config = string(b)
	}
	return nil
}

// UpdateTarget PUT /api/targets/{id}
func (s *Service) UpdateTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Type    string          `json:"type"`
		Name    string          `json:"name"`
		Config  json.RawMessage `json:"config"`
		Enabled bool            `json:"enabled"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	t := &store.BackupTarget{ID: id, Type: body.Type, Name: body.Name, Config: string(body.Config), Enabled: body.Enabled}
	if err := s.enrichGithub(r.Context(), t, body.Config); err != nil {
		httpx.Err(w, 400, err.Error())
		return
	}
	if err := s.Store.UpdateTarget(r.Context(), t); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// DeleteTarget DELETE /api/targets/{id}
func (s *Service) DeleteTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = s.Store.DeleteTarget(r.Context(), id)
	httpx.OK(w, map[string]string{"ok": "1"})
}

// TestTarget POST /api/targets/{id}/test
func (s *Service) TestTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := s.Store.GetTarget(r.Context(), id)
	if err != nil {
		httpx.Err(w, 404, "target not found")
		return
	}
	up, err := targets.New(t, nil)
	if err != nil {
		httpx.Err(w, 400, err.Error())
		return
	}
	if err := up.Test(r.Context()); err != nil {
		httpx.Err(w, 502, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// GithubResolve POST /api/targets/github/resolve {token, repo}
// Validates the PAT, derives the owner, and ensures a private repo exists — so
// the user only needs to paste a token (+ optional repo name).
func (s *Service) GithubResolve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Repo  string `json:"repo"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Token == "" {
		httpx.Err(w, 400, "token required")
		return
	}
	owner, branch, err := targets.ResolveGithub(r.Context(), body.Token, body.Repo)
	if err != nil {
		httpx.Err(w, 502, err.Error())
		return
	}
	repo := body.Repo
	if repo == "" {
		repo = "nodepanel-backups"
	}
	httpx.OK(w, map[string]string{"owner": owner, "repo": repo, "branch": branch})
}

// GithubPushProject POST /api/settings/github/push-project {token, owner, repo, branch, force}
// Pushes the NodePanel project source to the chosen GitHub repo so other servers
// can clone & deploy it. Uses git; the PAT is inline on the push URL only.
func (s *Service) GithubPushProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token  string `json:"token"`
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Force  bool   `json:"force"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Token == "" || body.Owner == "" || body.Repo == "" {
		httpx.Err(w, 400, "token、owner、repo 必填")
		return
	}
	if body.Branch == "" {
		body.Branch = "main"
	}
	dir, _ := s.Store.GetSetting(r.Context(), "project_dir")
	if dir == "" {
		dir = "/root/nodepanel"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	log, err := gitPushProject(ctx, dir, body.Token, body.Owner, body.Repo, body.Branch, body.Force)
	if err != nil {
		httpx.OK(w, map[string]any{"ok": false, "branch": body.Branch, "log": log, "error": err.Error()})
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "branch": body.Branch, "log": log})
}

// GithubSaveConfig PUT /api/settings/github {token, owner, repo, branch, force}
// Persists the GitHub integration config (including the PAT) under the
// "github_project" key so the upload form can be reused without re-entering it.
// Returned verbatim by GetAll; the PAT is treated like the Telegram bot token.
func (s *Service) GithubSaveConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token  string `json:"token"`
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Force  bool   `json:"force"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	b, _ := json.Marshal(body)
	if err := s.Store.SetSetting(r.Context(), "github_project", string(b)); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// GithubRepos POST /api/targets/github/repos {token} — lists pushable repos for
// the picker (no manual owner/repo entry).
func (s *Service) GithubRepos(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Token == "" {
		httpx.Err(w, 400, "token required")
		return
	}
	repos, err := targets.ListRepos(r.Context(), body.Token)
	if err != nil {
		httpx.Err(w, 502, err.Error())
		return
	}
	if repos == nil {
		repos = []targets.RepoInfo{}
	}
	httpx.OK(w, repos)
}

// VpsList POST /api/targets/vps/list {host,port,user,password,key_pem,path} —
// lists remote directory entries so the UI can browse/select a base_dir.
func (s *Service) VpsList(w http.ResponseWriter, r *http.Request) {
	var body struct {
		targets.VPSConfig
		Path string `json:"path"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Host == "" {
		httpx.Err(w, 400, "host required")
		return
	}
	res, err := targets.BrowseVPS(body.VPSConfig, body.Path)
	if err != nil {
		httpx.Err(w, 502, "连接/列目录失败："+err.Error())
		return
	}
	if res.Entries == nil {
		res.Entries = []targets.DirEntry{}
	}
	if res.Mounts == nil {
		res.Mounts = []targets.FSDisk{}
	}
	httpx.OK(w, res)
}

// OnedriveDevice POST /api/targets/onedrive/device {client_id}
func (s *Service) OnedriveDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID string `json:"client_id"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.ClientID == "" {
		httpx.Err(w, 400, "client_id required")
		return
	}
	dc, err := targets.RequestDeviceCode(r.Context(), body.ClientID)
	if err != nil {
		httpx.Err(w, 502, err.Error())
		return
	}
	httpx.OK(w, dc)
}

// OnedrivePoll POST /api/targets/onedrive/poll {client_id, device_code}
func (s *Service) OnedrivePoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID   string `json:"client_id"`
		DeviceCode string `json:"device_code"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	cfg, pending, err := targets.PollOnce(r.Context(), body.ClientID, body.DeviceCode)
	if err != nil {
		httpx.Err(w, 502, err.Error())
		return
	}
	if pending {
		httpx.OK(w, map[string]string{"status": "pending"})
		return
	}
	httpx.OK(w, map[string]any{"status": "ok", "config": cfg})
}

// --- schedules ---

// ListSchedules GET /api/schedules
func (s *Service) ListSchedules(w http.ResponseWriter, r *http.Request) {
	list, _ := s.Store.ListSchedules(r.Context())
	httpx.OK(w, list)
}

// CreateSchedule POST /api/schedules {type,node_id,config,days[],hour,minute,enabled}
func (s *Service) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type    string          `json:"type"`
		NodeID  string          `json:"node_id"`
		Config  json.RawMessage `json:"config"`
		Days    []int           `json:"days"`
		Hour    int             `json:"hour"`
		Minute  int             `json:"minute"`
		Cron    string          `json:"cron"` // optional explicit
		Enabled bool            `json:"enabled"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Type == "" {
		httpx.Err(w, 400, "invalid body")
		return
	}
	cronSpec := body.Cron
	if cronSpec == "" {
		cronSpec = buildCron(body.Days, body.Hour, body.Minute)
	}
	sc := &store.Schedule{
		Type: body.Type, NodeID: body.NodeID, Config: string(body.Config),
		Cron: cronSpec, Enabled: body.Enabled || true,
	}
	if err := s.Store.CreateSchedule(r.Context(), sc); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), scheduleAuditActor(r.Context()), "schedule.create",
		fmt.Sprintf("id=%s type=%s cron=%s enabled=%v", sc.ID, sc.Type, sc.Cron, sc.Enabled))
	httpx.OK(w, sc)
}

// UpdateSchedule PUT /api/schedules/{id}
func (s *Service) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Type    string          `json:"type"`
		NodeID  string          `json:"node_id"`
		Config  json.RawMessage `json:"config"`
		Days    []int           `json:"days"`
		Hour    int             `json:"hour"`
		Minute  int             `json:"minute"`
		Cron    string          `json:"cron"`
		Enabled bool            `json:"enabled"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	cronSpec := body.Cron
	if cronSpec == "" {
		cronSpec = buildCron(body.Days, body.Hour, body.Minute)
	}
	sc := &store.Schedule{
		ID: id, Type: body.Type, NodeID: body.NodeID, Config: string(body.Config),
		Cron: cronSpec, Enabled: body.Enabled,
	}
	if err := s.Store.UpdateSchedule(r.Context(), sc); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), scheduleAuditActor(r.Context()), "schedule.update",
		fmt.Sprintf("id=%s type=%s cron=%s enabled=%v", sc.ID, sc.Type, sc.Cron, sc.Enabled))
	httpx.OK(w, map[string]string{"ok": "1"})
}

// DeleteSchedule DELETE /api/schedules/{id}
func (s *Service) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = s.Store.DeleteSchedule(r.Context(), id)
	s.Store.Audit(r.Context(), scheduleAuditActor(r.Context()), "schedule.delete",
		fmt.Sprintf("id=%s", id))
	httpx.OK(w, map[string]string{"ok": "1"})
}

func scheduleAuditActor(ctx context.Context) string {
	if actor := auth.UserID(ctx); actor != "" {
		return actor
	}
	return "unknown"
}

func buildCron(days []int, hour, minute int) string {
	if len(days) == 0 {
		return fmt.Sprintf("%d %d * * *", minute, hour)
	}
	parts := make([]string, 0, len(days))
	for _, d := range days {
		parts = append(parts, strconv.Itoa(d))
	}
	return fmt.Sprintf("%d %d * * %s", minute, hour, strings.Join(parts, ","))
}
