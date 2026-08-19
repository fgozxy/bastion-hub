// NodePanel master server entry point.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"nodepanel/master/internal/agentapi"
	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/authapi"
	"nodepanel/master/internal/backup"
	"nodepanel/master/internal/browserhub"
	"nodepanel/master/internal/commands"
	"nodepanel/master/internal/config"
	"nodepanel/master/internal/container"
	"nodepanel/master/internal/credentials"
	"nodepanel/master/internal/dashboard"
	"nodepanel/master/internal/geoip"
	"nodepanel/master/internal/health"
	"nodepanel/master/internal/mesh"
	"nodepanel/master/internal/monitor"
	"nodepanel/master/internal/nodes"
	"nodepanel/master/internal/scheduler"
	"nodepanel/master/internal/server"
	"nodepanel/master/internal/settings"
	"nodepanel/master/internal/store"
	"nodepanel/master/internal/telegram"
)

func main() {
	var (
		dataDir   = flag.String("data-dir", "/var/lib/nodepanel", "data directory")
		domain    = flag.String("domain", "", "panel domain (e.g. panel.example.com)")
		dev       = flag.Bool("dev", false, "plain HTTP dev mode")
		devAddr   = flag.String("dev-addr", ":8080", "dev listen address")
		adminUser = flag.String("admin-user", "", "initial admin username (first run)")
		adminPass = flag.String("admin-pass", "", "initial admin password (first run)")
	)
	flag.Parse()

	cfg := config.Default(*dataDir, *domain)
	cfg.Dev = *dev
	cfg.DevAddr = *devAddr
	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("ensure dirs: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.DB.Close()

	if err := auth.EnsureAdmin(context.Background(), st, *adminUser, *adminPass); err != nil {
		log.Fatalf("ensure admin: %v", err)
	}
	if err := commands.SeedBuiltins(context.Background(), st); err != nil {
		log.Fatalf("seed builtin commands: %v", err)
	}

	geo := geoip.New()
	browser := browserhub.New()

	agentAPI := &agentapi.Service{Store: st, Geo: geo, Browser: browser, Cfg: cfg}
	hub := agenthub.New(agentAPI.NewHubHandlers())
	agentAPI.Hub = hub

	tg := telegram.New(st)
	backupSvc := &backup.Service{Store: st, Hub: hub, Browser: browser, Cfg: cfg, Notify: tg}

	sched := scheduler.New(st, backupSvc, hub, tg)
	sched.Start()

	// Stale-"running" reaper: finalizes backup rows an interrupted master (deploy
	// / crash) left in "running" forever, so they stop polluting the list and
	// turning nodes 🔴 in the report. Threshold lives in the 'backup_reaper'
	// setting (default 120m); scans every 5m and once on boot.
	backupSvc.StartReaper()

	// Container-health monitor: scans the inventory and pushes a Telegram alert
	// when a container enters exited/dead/restarting. Frequency/on-off come from
	// the 'container_monitor' setting.
	monitor.New(st, tg).Start()

	nodesSvc := &nodes.Service{Store: st, Hub: hub, Browser: browser, Geo: geo, Cfg: cfg}

	// Health monitor (Netdata backend): polls each enabled node's local Netdata
	// via the agent and evaluates alert thresholds → Telegram. Disabled by default
	// until nodes are enrolled via the 健康监控 panel.
	healthSvc := health.New(st, hub, nodesSvc, tg)
	healthSvc.Start()
	meshSvc := &mesh.Service{Store: st, Hub: hub}
	// Auto mesh provisioning (5-min sync of mesh keys + the port-22022 IP
	// allowlist firewall across online nodes). Toggle off with
	// NODEPANEL_MESH_AUTO=0 to stop the panel from (re)applying the SSH
	// source-IP restriction on managed nodes; the manual /api/mesh/* endpoints
	// remain available either way.
	if os.Getenv("NODEPANEL_MESH_AUTO") != "0" {
		meshSvc.StartAutoProvision(context.Background())
	}

	deps := &server.Deps{
		Cfg:         cfg,
		Store:       st,
		Nodes:       nodesSvc,
		AgentAPI:    agentAPI,
		Commands:    &commands.Service{Store: st, Hub: hub, Browser: browser},
		Credentials: &credentials.Service{Store: st, Hub: hub},
		Mesh:        meshSvc,
		Backup:      backupSvc,
		Dashboard:   &dashboard.Service{Store: st},
		Settings:    &settings.Service{Store: st, TG: tg},
		Container:   &container.Service{Hub: hub, Store: st},
		Health:      healthSvc,
		AuthAPI:     &authapi.Service{Store: st, Browser: browser, Dev: *dev},
	}

	// background maintenance
	go maintenance(st)

	if err := server.Run(deps); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func maintenance(st *store.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		ctx := context.Background()
		_ = st.PruneSessions(ctx)
		_ = st.PruneMetrics(ctx, time.Now().Add(-7*24*time.Hour).Unix())
	}
}
