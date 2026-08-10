package store

import (
	"context"
	"database/sql"
	"errors"
)

// EnableHealthNode marks a node as Netdata-enabled (installed + watched). Upsert.
func (s *Store) EnableHealthNode(ctx context.Context, nodeID string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO health_nodes(node_id, enabled, installed_at) VALUES(?,1,?)
		 ON CONFLICT(node_id) DO UPDATE SET enabled=1, installed_at=excluded.installed_at`,
		nodeID, now())
	return err
}

// SetHealthNodeCores records the node's CPU core count (from Netdata /api/v1/info
// at install time), used to scale the default load alert (cores × 2).
func (s *Store) SetHealthNodeCores(ctx context.Context, nodeID string, cores int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE health_nodes SET cores=? WHERE node_id=?`, cores, nodeID)
	return err
}

// DisableHealthNode turns monitoring off for a node (keeps the row / installed_at).
func (s *Store) DisableHealthNode(ctx context.Context, nodeID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE health_nodes SET enabled=0 WHERE node_id=?`, nodeID)
	return err
}

// UninstallHealthNode marks a node's Netdata as removed: stop watching AND clear
// installed_at so the node reads as "not installed" again. Unlike DisableHealthNode
// (which just pauses polling while Netdata keeps running), this is the bookkeeping
// side of actually removing Netdata from the host — freeing its memory/CPU on
// low-spec nodes. Alert rows are intentionally kept so a later reinstall restores
// the user's thresholds (SeedDefaultAlerts is a no-op when rules already exist).
func (s *Store) UninstallHealthNode(ctx context.Context, nodeID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE health_nodes SET enabled=0, installed_at=0 WHERE node_id=?`, nodeID)
	return err
}

// GetHealthNode returns the health state for one node, or ErrNotFound.
func (s *Store) GetHealthNode(ctx context.Context, nodeID string) (*HealthNode, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT node_id, enabled, COALESCE(installed_at,0), COALESCE(cores,0) FROM health_nodes WHERE node_id=?`, nodeID)
	var h HealthNode
	var enabled int
	err := row.Scan(&h.NodeID, &enabled, &h.InstalledAt, &h.Cores)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	h.Enabled = enabled == 1
	return &h, nil
}

// ListHealthNodes returns all health-enabled node rows.
func (s *Store) ListHealthNodes(ctx context.Context) ([]HealthNode, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT node_id, enabled, COALESCE(installed_at,0), COALESCE(cores,0) FROM health_nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HealthNode
	for rows.Next() {
		var h HealthNode
		var enabled int
		if err := rows.Scan(&h.NodeID, &enabled, &h.InstalledAt, &h.Cores); err != nil {
			return nil, err
		}
		h.Enabled = enabled == 1
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListHealthAlerts returns the alert rules for one node.
func (s *Store) ListHealthAlerts(ctx context.Context, nodeID string) ([]HealthAlert, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, node_id, metric, threshold, window_sec, enabled, COALESCE(last_notified,0), COALESCE(breach_since,0)
		 FROM health_alerts WHERE node_id=? ORDER BY metric`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlerts(rows)
}

// ListAllEnabledHealthAlerts returns every enabled alert rule (for the poller).
func (s *Store) ListAllEnabledHealthAlerts(ctx context.Context) ([]HealthAlert, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, node_id, metric, threshold, window_sec, enabled, COALESCE(last_notified,0), COALESCE(breach_since,0)
		 FROM health_alerts WHERE enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlerts(rows)
}

func scanAlerts(rows *sql.Rows) ([]HealthAlert, error) {
	var out []HealthAlert
	for rows.Next() {
		var a HealthAlert
		var enabled int
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Metric, &a.Threshold, &a.WindowSec,
			&enabled, &a.LastNotified, &a.BreachSince); err != nil {
			return nil, err
		}
		a.Enabled = enabled == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// PutHealthAlert upserts an alert rule by id.
func (s *Store) PutHealthAlert(ctx context.Context, a *HealthAlert) error {
	if a.ID == "" {
		a.ID = newID()
	}
	if a.WindowSec <= 0 {
		a.WindowSec = 60
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO health_alerts(id,node_id,metric,threshold,window_sec,enabled,last_notified,breach_since)
		 VALUES(?,?,?,?,?,?,NULL,NULL)
		 ON CONFLICT(id) DO UPDATE SET node_id=excluded.node_id, metric=excluded.metric,
		   threshold=excluded.threshold, window_sec=excluded.window_sec, enabled=excluded.enabled`,
		a.ID, a.NodeID, a.Metric, a.Threshold, a.WindowSec, enabled)
	if err != nil {
		return err
	}
	a.Enabled = enabled == 1
	return nil
}

// DeleteHealthAlert removes an alert rule.
func (s *Store) DeleteHealthAlert(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM health_alerts WHERE id=?`, id)
	return err
}

// TouchHealthAlertState updates the poller's per-alert breach/notified state.
// Pass breachSince=0 to clear (recovered); lastNotified is set only when notifying.
func (s *Store) TouchHealthAlertState(ctx context.Context, id string, breachSince, lastNotified int64) error {
	if lastNotified > 0 {
		_, err := s.DB.ExecContext(ctx,
			`UPDATE health_alerts SET breach_since=?, last_notified=? WHERE id=?`, breachSince, lastNotified, id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE health_alerts SET breach_since=? WHERE id=?`, breachSince, id)
	return err
}

// SeedDefaultAlerts inserts the given default alert rules for a node ONLY if the
// node has no alerts yet — so re-installing / re-running doesn't pile up dupes,
// and users keep any manual rules they've already added.
func (s *Store) SeedDefaultAlerts(ctx context.Context, nodeID string, alerts []HealthAlert) error {
	var existing int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM health_alerts WHERE node_id=?`, nodeID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	for _, a := range alerts {
		a.ID = newID()
		a.NodeID = nodeID
		a.Enabled = true
		if a.WindowSec <= 0 {
			a.WindowSec = 60
		}
		if _, err := s.DB.ExecContext(ctx,
			`INSERT INTO health_alerts(id,node_id,metric,threshold,window_sec,enabled,last_notified,breach_since)
			 VALUES(?,?,?,?,?,1,NULL,NULL)`,
			a.ID, a.NodeID, a.Metric, a.Threshold, a.WindowSec); err != nil {
			return err
		}
	}
	return nil
}

// ResetNodeAlertsToDefault deletes every alert for a node and re-inserts the
// given defaults (used by "恢复默认" in the template editor).
func (s *Store) ResetNodeAlertsToDefault(ctx context.Context, nodeID string, alerts []HealthAlert) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM health_alerts WHERE node_id=?`, nodeID); err != nil {
		return err
	}
	for _, a := range alerts {
		a.ID = newID()
		a.NodeID = nodeID
		a.Enabled = true
		if a.WindowSec <= 0 {
			a.WindowSec = 60
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO health_alerts(id,node_id,metric,threshold,window_sec,enabled,last_notified,breach_since)
			 VALUES(?,?,?,?,?,1,NULL,NULL)`,
			a.ID, a.NodeID, a.Metric, a.Threshold, a.WindowSec); err != nil {
			return err
		}
	}
	return tx.Commit()
}
