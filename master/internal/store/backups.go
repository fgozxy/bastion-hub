package store

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) CreateBackup(ctx context.Context, b *Backup) error {
	if b.ID == "" {
		b.ID = newID()
	}
	if b.CreatedAt == 0 {
		b.CreatedAt = now()
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO backups(id,node_id,name,paths,container,container_name,size,target,stage_path,status,error,manifest,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.NodeID, b.Name, b.Paths, b.Container, b.ContainerName, b.Size, b.Target, b.StagePath, b.Status, b.Error, b.Manifest, b.CreatedAt)
	return err
}

func (s *Store) UpdateBackup(ctx context.Context, b *Backup) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE backups SET size=?, status=?, error=?, stage_path=?, manifest=? WHERE id=?`,
		b.Size, b.Status, b.Error, b.StagePath, b.Manifest, b.ID)
	return err
}

const backupCols = `id,node_id,COALESCE(name,''),COALESCE(paths,''),COALESCE(container,''),COALESCE(container_name,''),COALESCE(size,0),COALESCE(target,''),
			COALESCE(stage_path,''),COALESCE(status,''),COALESCE(error,''),COALESCE(manifest,''),created_at`

func scanBackup(row interface{ Scan(...any) error }) (Backup, error) {
	var b Backup
	err := row.Scan(&b.ID, &b.NodeID, &b.Name, &b.Paths, &b.Container, &b.ContainerName, &b.Size, &b.Target, &b.StagePath, &b.Status, &b.Error, &b.Manifest, &b.CreatedAt)
	return b, err
}

func (s *Store) ListBackups(ctx context.Context, nodeID string) ([]Backup, error) {
	q := `SELECT ` + backupCols + ` FROM backups`
	args := []any{}
	if nodeID != "" {
		q += ` WHERE node_id=?`
		args = append(args, nodeID)
	}
	q += ` ORDER BY created_at DESC LIMIT 500`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetBackup(ctx context.Context, id string) (*Backup, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+backupCols+` FROM backups WHERE id=?`, id)
	b, err := scanBackup(row)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) DeleteBackup(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM backups WHERE id=?`, id)
	return err
}

// ReapStaleBackups finalizes backups still in the "running" state that are older
// than maxAge, and returns the rows it reaped.
//
// A "running" row older than maxAge cannot belong to a live job: both runBackup
// and RunResticContainerBackupSync cap a single job at 8h and write a terminal
// status (ok/failed) themselves before returning. So a running row past that
// window is an orphan — the goroutine that would have finalized it died when the
// master restarted mid-backup (deploy / crash). Those orphans pollute the backup
// list, appear as never-finishing jobs in the UI, and can turn a node 🔴 in the
// report. Marking them failed (not deleted) keeps the audit trail; the reaper
// also evicts any partial stage file. The UPDATE is guarded by status='running'
// so a job that legitimately finished between the SELECT and UPDATE is untouched.
func (s *Store) ReapStaleBackups(ctx context.Context, maxAge time.Duration) ([]Backup, error) {
	cutoff := now() - int64(maxAge.Seconds())
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+backupCols+` FROM backups WHERE status='running' AND created_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	var reaped []Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		reaped = append(reaped, b)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	note := fmt.Sprintf("reaped: stuck in running >%s (master likely restarted mid-backup)", maxAge)
	for _, b := range reaped {
		_, _ = s.DB.ExecContext(ctx,
			`UPDATE backups SET status='failed', error=? WHERE id=? AND status='running'`,
			note, b.ID)
	}
	return reaped, nil
}

// ListBackupsForRetention returns backups older/newer for a node ordered oldest-first.
func (s *Store) ListBackupsForRetention(ctx context.Context, nodeID string) ([]Backup, error) {
	return s.ListBackups(ctx, nodeID)
}

// ContainerBackupRow is one restorable container backup with its container
// name/image resolved by joining the live containers inventory (and custom
// display names). Used by the restore view to dedupe/group by container. Only
// containers that still exist in the live inventory (容器板块) are returned —
// backups of deleted/rebuilt-away containers are excluded so the restore view
// mirrors the containers page.
type ContainerBackupRow struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	Container   string `json:"container"`
	Size        int64  `json:"size"`
	Target      string `json:"target"`
	CreatedAt   int64  `json:"created_at"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Image       string `json:"image"`
}

// ListContainerBackupsForRestore returns every successful container backup
// (newest first) for containers that still exist in the live inventory, with
// name/image joined in for the restore view's per-container dedup. Backups of
// containers that have since been deleted (or rebuilt under a new container id)
// are excluded — the restore view only shows containers that appear in the
// containers page.
func (s *Store) ListContainerBackupsForRestore(ctx context.Context) ([]ContainerBackupRow, error) {
	q := `SELECT b.id, b.node_id, COALESCE(b.container,''), COALESCE(b.size,0), COALESCE(b.target,''), b.created_at,
			COALESCE(c.name,''), COALESCE(n.display_name,''), COALESCE(c.image,'')
		FROM backups b
		JOIN containers c ON c.node_id=b.node_id
			AND (c.name=b.container_name
				OR (COALESCE(b.container_name,'')='' AND c.container_id=b.container))
		LEFT JOIN container_names n ON n.node_id=c.node_id AND n.name=c.name
		WHERE b.container!='' AND b.status='ok' AND b.target!='restic-incremental'
			AND c.name IS NOT NULL AND c.name!=''
		ORDER BY b.created_at DESC`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContainerBackupRow
	for rows.Next() {
		var r ContainerBackupRow
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Container, &r.Size, &r.Target, &r.CreatedAt, &r.Name, &r.DisplayName, &r.Image); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListContainerBackupsByName returns every successful backup for one container
// name (newest first), joined with the live container inventory for image/
// display name. Used by the restore view's expandable per-container history so a
// user can pick an older point-in-time snapshot instead of only the newest.
func (s *Store) ListContainerBackupsByName(ctx context.Context, name string) ([]ContainerBackupRow, error) {
	q := `SELECT b.id, b.node_id, COALESCE(b.container,''), COALESCE(b.size,0), COALESCE(b.target,''), b.created_at,
			COALESCE(c.name,''), COALESCE(n.display_name,''), COALESCE(c.image,'')
		FROM backups b
		LEFT JOIN containers c ON c.node_id=b.node_id AND c.name=b.container_name
		LEFT JOIN container_names n ON n.node_id=c.node_id AND n.name=c.name
		WHERE b.container_name=? AND b.status='ok' AND b.target!='restic-incremental'
		ORDER BY b.created_at DESC`
	rows, err := s.DB.QueryContext(ctx, q, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContainerBackupRow
	for rows.Next() {
		var r ContainerBackupRow
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Container, &r.Size, &r.Target, &r.CreatedAt, &r.Name, &r.DisplayName, &r.Image); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
