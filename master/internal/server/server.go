// Package server wires all services into a chi router and runs the HTTP(S) server.
package server

import (
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"nodepanel/master/internal/agentapi"
	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/authapi"
	"nodepanel/master/internal/backup"
	"nodepanel/master/internal/commands"
	"nodepanel/master/internal/config"
	"nodepanel/master/internal/container"
	"nodepanel/master/internal/credentials"
	"nodepanel/master/internal/dashboard"
	"nodepanel/master/internal/health"
	"nodepanel/master/internal/mesh"
	"nodepanel/master/internal/nodes"
	"nodepanel/master/internal/settings"
	"nodepanel/master/internal/store"
	"nodepanel/master/internal/webassets"
)

// Deps bundles all services for route mounting.
type Deps struct {
	Cfg         config.Config
	Store       *store.Store
	Nodes       *nodes.Service
	AgentAPI    *agentapi.Service
	Commands    *commands.Service
	Credentials *credentials.Service
	Mesh        *mesh.Service
	Backup      *backup.Service
	Dashboard   *dashboard.Service
	Settings    *settings.Service
	Container   *container.Service
	Health      *health.Service
	AuthAPI     *authapi.Service
}

// Routes builds the HTTP router.
func Routes(d *Deps) http.Handler {
	r := chi.NewRouter()

	// --- public (no auth) ---
	r.Get("/install.sh", d.AgentAPI.InstallScript)
	r.Get("/dl/{name}", d.AgentAPI.ServeBinary)
	r.Post("/api/agent/enroll", d.AgentAPI.Enroll)
	r.Post("/api/agent/report-egress", d.AgentAPI.ReportEgress)
	r.Get("/agent/ws", d.AgentAPI.AgentWS)
	r.Post("/api/agent/upload", d.AgentAPI.Upload)
	r.Get("/api/agent/dl", d.AgentAPI.ServeStaged)
	r.Post("/api/auth/login", d.AuthAPI.Login)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// --- authenticated API ---
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(d.Store))

		r.Post("/api/auth/logout", d.AuthAPI.Logout)
		r.Get("/api/auth/me", d.AuthAPI.Me)
		r.Get("/api/ws", d.AuthAPI.BrowserWS)

		r.Get("/api/dashboard", d.Dashboard.Stats)

		r.Get("/api/nodes", d.Nodes.List)
		r.Post("/api/nodes", d.Nodes.Create)
		r.Patch("/api/nodes/{id}", d.Nodes.Rename)
		r.Put("/api/nodes/{id}/base-domain", d.Nodes.SetBaseDomain)
		r.Put("/api/nodes/{id}/ingress-type", d.Nodes.SetIngressType)
		r.Post("/api/nodes/{id}/regenerate", d.Nodes.Regenerate)
		r.Post("/api/nodes/{id}/update-agent", d.Nodes.UpdateAgent)
		r.Post("/api/nodes/update-agents", d.Nodes.UpdateAgents)
		r.Delete("/api/nodes/{id}", d.Nodes.Delete)
		r.Post("/api/nodes/firewall/status", d.Nodes.FirewallStatus)
		r.Post("/api/nodes/firewall/toggle", d.Nodes.FirewallToggle)
		r.Post("/api/nodes/firewall/ports", d.Nodes.FirewallPorts)

		// 探针 (Komari) — list joinable nodes / batch-enroll them.
		r.Get("/api/nodes/probe/candidates", d.Nodes.ProbeCandidates)
		r.Post("/api/nodes/probe/join", d.Nodes.ProbeJoin)

		// 健康监控 (Netdata) — per-node status, batch install/uninstall, metrics, alert rules.
		r.Get("/api/health", d.Health.Status)
		r.Post("/api/health/install", d.Health.Install)
		r.Post("/api/health/uninstall", d.Health.Uninstall)
		r.Get("/api/health/template", d.Health.GetTemplate)
		r.Put("/api/health/template", d.Health.PutTemplate)
		r.Post("/api/health/template/reset", d.Health.ResetTemplate)
		r.Get("/api/health/metrics", d.Health.Metrics)
		r.Get("/api/health/alerts", d.Health.ListAlerts)
		r.Put("/api/health/alerts", d.Health.PutAlert)
		r.Delete("/api/health/alerts/{id}", d.Health.DeleteAlert)
		r.Post("/api/health/test-fetch", d.Health.TestFetch)

		r.Post("/api/commands", d.Commands.Run)
		r.Get("/api/commands", d.Commands.List)
		r.Get("/api/commands/saved", d.Commands.ListSaved)
		r.Post("/api/commands/saved", d.Commands.CreateSaved)
		r.Delete("/api/commands/saved/{id}", d.Commands.DeleteSaved)
		r.Get("/api/commands/{id}", d.Commands.Get)

		r.Get("/api/credentials", d.Credentials.List)
		r.Post("/api/credentials", d.Credentials.Create)
		r.Post("/api/credentials/{id}/bind", d.Credentials.Bind)
		r.Post("/api/credentials/{id}/test", d.Credentials.Test)
		r.Delete("/api/credentials/{id}", d.Credentials.Delete)
		r.Post("/api/credentials/scan/{nodeID}", d.Credentials.ScanFromNode)
		r.Post("/api/credentials/scan-multi", d.Credentials.ScanFromNodes)
		r.Post("/api/credentials/import", d.Credentials.ImportFromNode)

		r.Post("/api/mesh/provision", d.Mesh.Provision)
		r.Get("/api/mesh/status", d.Mesh.Status)

		r.Get("/api/backups", d.Backup.List)
		r.Post("/api/backups/now", d.Backup.BackupNow)
		r.Post("/api/backups/{id}/restore", d.Backup.Restore)
		r.Delete("/api/backups/{id}", d.Backup.Delete)
		r.Get("/api/backups/containers/restore", d.Backup.RestoreContainersList)
		r.Post("/api/backups/containers/restore", d.Backup.RestoreContainers)

		r.Get("/api/restore/jobs", d.Backup.ListRestoreJobs)
		r.Get("/api/restore/container-backups", d.Backup.ContainerBackupsByName)
		r.Post("/api/restore/preflight", d.Backup.Preflight)

		r.Post("/api/containers/migrate", d.Backup.Migrate)
		r.Post("/api/containers/migrate/domain-plan", d.Backup.DomainPlan)
		r.Get("/api/migrate/jobs", d.Backup.ListMigrateJobs)

		r.Get("/api/targets", d.Settings.ListTargets)
		r.Post("/api/targets", d.Settings.CreateTarget)
		r.Put("/api/targets/{id}", d.Settings.UpdateTarget)
		r.Delete("/api/targets/{id}", d.Settings.DeleteTarget)
		r.Post("/api/targets/{id}/test", d.Settings.TestTarget)
		r.Post("/api/targets/github/resolve", d.Settings.GithubResolve)
		r.Post("/api/targets/github/repos", d.Settings.GithubRepos)
		r.Post("/api/targets/vps/list", d.Settings.VpsList)
		r.Post("/api/targets/onedrive/device", d.Settings.OnedriveDevice)
		r.Post("/api/targets/onedrive/poll", d.Settings.OnedrivePoll)

		r.Get("/api/schedules", d.Settings.ListSchedules)
		r.Post("/api/schedules", d.Settings.CreateSchedule)
		r.Put("/api/schedules/{id}", d.Settings.UpdateSchedule)
		r.Delete("/api/schedules/{id}", d.Settings.DeleteSchedule)

		r.Get("/api/settings", d.Settings.GetAll)
		r.Put("/api/settings/account", d.Settings.PutAccount)
		r.Put("/api/settings/telegram", d.Settings.PutTelegram)
		r.Post("/api/settings/telegram/test", d.Settings.TestTelegram)
		r.Put("/api/settings/retention", d.Settings.PutRetention)
		r.Put("/api/settings/excludes", d.Settings.PutExcludes)
		r.Put("/api/settings/cloudflare", d.Settings.PutCloudflare)
		r.Put("/api/settings/container-monitor", d.Settings.PutContainerMonitor)
		r.Put("/api/settings/komari", d.Settings.PutKomari)
		r.Post("/api/settings/komari/test", d.Settings.TestKomari)
		r.Post("/api/settings/cloudflare/test", d.Settings.TestCloudflare)
		r.Put("/api/settings/github", d.Settings.GithubSaveConfig)
		r.Post("/api/settings/github/push-project", d.Settings.GithubPushProject)

		r.Post("/api/container/update", d.Container.Update)
		r.Get("/api/containers", d.Container.List)
		r.Post("/api/containers/action", d.Container.Action)
		r.Post("/api/containers/scan-updates", d.Container.ScanUpdates)
		r.Put("/api/containers/name", d.Container.SetName)

	})

	// --- SPA (embedded frontend) ---
	r.NotFound(spaHandler())

	return r
}

// spaHandler serves the embedded frontend with HTML5 history fallback.
func spaHandler() http.HandlerFunc {
	sub, err := fs.Sub(webassets.FS, "dist")
	if err != nil {
		return func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	}
	fileServer := http.FileServer(http.FS(sub))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// fallback to index.html for client-side routing
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	}
}

// Run starts the HTTP server. Public HTTPS is terminated by the reverse proxy.
func Run(d *Deps) error {
	srv := &http.Server{
		Addr:              d.Cfg.DevAddr,
		Handler:           Routes(d),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}
