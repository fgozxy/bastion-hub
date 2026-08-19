// Package store is the SQLite persistence layer for the master.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the master database.
type Store struct {
	DB *sql.DB
}

// Open opens (or creates) the SQLite database at path and runs migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite serializes writes; 1 conn avoids lock errors
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enrollment_token TEXT UNIQUE NOT NULL,
			agent_token TEXT UNIQUE,
			status TEXT NOT NULL DEFAULT 'offline',
			hostname TEXT,
			os TEXT,
			arch TEXT,
			kernel TEXT,
			ipv4 TEXT,
			ipv6 TEXT,
			country_code TEXT,
			country TEXT,
			agent_version TEXT,
			last_seen INTEGER,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS credentials (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			pub_key TEXT,
			priv_key TEXT,
			fingerprint TEXT,
			kind TEXT,
			source TEXT,
			node_id TEXT,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS backups (
			id TEXT PRIMARY KEY,
			node_id TEXT NOT NULL,
			name TEXT,
			paths TEXT,
			size INTEGER,
			target TEXT,
			stage_path TEXT,
			status TEXT,
			error TEXT,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS backup_targets (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			config TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS schedules (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			node_id TEXT,
			config TEXT,
			cron TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_run INTEGER,
			next_run INTEGER,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS commands (
			id TEXT PRIMARY KEY,
			node_ids TEXT,
			cmd TEXT,
			status TEXT,
			exit_code INTEGER,
			author TEXT,
			created_at INTEGER NOT NULL,
			finished_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS command_lines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			command_id TEXT NOT NULL,
			node_id TEXT,
			seq INTEGER NOT NULL,
			stream TEXT,
			data TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS metrics (
			node_id TEXT NOT NULL,
			ts INTEGER NOT NULL,
			cpu REAL,
			mem_used INTEGER,
			mem_total INTEGER,
			disk_used INTEGER,
			disk_total INTEGER,
			load1 REAL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metrics_node_ts ON metrics(node_id, ts)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			actor TEXT,
			action TEXT,
			detail TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS saved_commands (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			script TEXT NOT NULL,
			builtin INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS containers (
			id TEXT PRIMARY KEY,
			node_id TEXT NOT NULL,
			container_id TEXT NOT NULL,
			name TEXT,
			image TEXT,
			image_id TEXT,
			state TEXT,
			status TEXT,
			created INTEGER,
			updated INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_containers_node ON containers(node_id)`,
		`CREATE TABLE IF NOT EXISTS container_names (
			node_id TEXT NOT NULL,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			PRIMARY KEY (node_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS container_scan (
			node_id TEXT NOT NULL,
			name TEXT NOT NULL,
			has_update INTEGER NOT NULL DEFAULT -1,
			note TEXT,
			scanned_at INTEGER,
			PRIMARY KEY (node_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS restore_jobs (
			id TEXT PRIMARY KEY,
			backup_id TEXT NOT NULL,
			container TEXT,
			image TEXT,
			origin_node TEXT,
			target_node TEXT NOT NULL,
			status TEXT NOT NULL,
			stage TEXT,
			detail TEXT,
			error TEXT,
			recreated INTEGER NOT NULL DEFAULT 0,
			bytes_total INTEGER NOT NULL DEFAULT 0,
			bytes_done INTEGER NOT NULL DEFAULT 0,
			percent INTEGER NOT NULL DEFAULT 0,
			agent_version TEXT,
			actor TEXT,
			started_at INTEGER NOT NULL,
			finished_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_restore_jobs_target ON restore_jobs(target_node, started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_restore_jobs_backup ON restore_jobs(backup_id)`,
		// health_nodes marks which nodes have Netdata enabled in the health panel.
		`CREATE TABLE IF NOT EXISTS health_nodes (
			node_id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0,
			installed_at INTEGER
		)`,
		// health_alerts are per-node metric thresholds the master evaluates against
		// polled Netdata samples. Supports load/iowait/swap (the metrics Komari
		// could not alert on). breach_since/last_notified drive sustained-window
		// gating + announce-once-per-outage dedup.
		`CREATE TABLE IF NOT EXISTS health_alerts (
			id TEXT PRIMARY KEY,
			node_id TEXT NOT NULL,
			metric TEXT NOT NULL,
			threshold REAL NOT NULL,
			window_sec INTEGER NOT NULL DEFAULT 60,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_notified INTEGER,
			breach_since INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_health_alerts_node ON health_alerts(node_id)`,
		// agent_egress stores the last known public IPv4 of each node, reported
		// over the short HTTPS path (can go via CF). The host-side UFW sync
		// script reads this table to keep 8443 locked to real agent IPs only.
		`CREATE TABLE IF NOT EXISTS agent_egress (
			node_id TEXT PRIMARY KEY,
			ip TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, st := range stmts {
		if _, err := s.DB.Exec(st); err != nil {
			return fmt.Errorf("migrate: %w (stmt: %s)", err, st)
		}
	}
	// Additive column migrations for tables created before the column existed.
	if err := s.addColumnIfMissing("backups", "container", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("nodes", "ssh_port", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("containers", "update_type", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("backups", "manifest", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("backups", "container_name", "TEXT"); err != nil {
		return err
	}
	// Backfill container_name for existing backup rows whose container_id still
	// matches a current live container. Rows whose container has been rebuilt
	// away (id no longer present) keep empty — they can't be attributed to any
	// name and don't belong in the restore view anyway. Idempotent: the WHERE
	// skips rows already filled, so re-runs on later starts are a no-op.
	if _, err := s.DB.Exec(`UPDATE backups
SET container_name = (
    SELECT c.name FROM containers c
    WHERE c.node_id = backups.node_id AND c.container_id = backups.container
)
WHERE (container_name IS NULL OR container_name = '')
  AND container != ''
  AND EXISTS (
      SELECT 1 FROM containers c
      WHERE c.node_id = backups.node_id AND c.container_id = backups.container
  )`); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("health_nodes", "cores", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

// addColumnIfMissing adds a column to an existing table if it isn't present.
func (s *Store) addColumnIfMissing(table, column, decl string) error {
	rows, err := s.DB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // already present
		}
	}
	_, err = s.DB.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

// now is a small helper for unix timestamps.
func now() int64 { return time.Now().Unix() }
