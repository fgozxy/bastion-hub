package store

import "context"

// MigrateJob is one cross-node container migration task, persisted so live
// progress and terminal outcomes survive a page refresh and form a history.
// Status is running | ok | failed | partial (partial = container was migrated
// and is running on the target, but the domain re-point failed; the source is
// retained in that case so the operator can fix Cloudflare and re-run).
type MigrateJob struct {
	ID            string `json:"id"`
	Container     string `json:"container"`
	Image         string `json:"image"`
	SourceNode    string `json:"source_node"`
	TargetNode    string `json:"target_node"`
	BackupID      string `json:"backup_id"`
	Status        string `json:"status"` // running | ok | failed | partial
	Stage         string `json:"stage"`  // backup | preflight | restore | domain | cleanup | "" (final)
	Detail        string `json:"detail"`
	Error         string `json:"error"`
	Domains       string `json:"domains,omitempty"`        // comma-joined hostnames moved (or attempted)
	PortsRemapped string `json:"ports_remapped,omitempty"` // human note of old→new port remaps
	DomainMoved   bool   `json:"domain_moved"`
	SourceRemoved bool   `json:"source_removed"`
	BytesTotal    int64  `json:"bytes_total"`
	BytesDone     int64  `json:"bytes_done"`
	Percent       int64  `json:"percent"`
	AgentVersion  string `json:"agent_version"`
	Actor         string `json:"actor"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at"`
}

func (s *Store) CreateMigrateJob(ctx context.Context, j *MigrateJob) error {
	if j.ID == "" {
		j.ID = newID()
	}
	if j.StartedAt == 0 {
		j.StartedAt = now()
	}
	dm, sr := int64(0), int64(0)
	if j.DomainMoved {
		dm = 1
	}
	if j.SourceRemoved {
		sr = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO migrate_jobs(id,container,image,source_node,target_node,backup_id,status,stage,detail,error,
			domains,ports_remapped,domain_moved,source_removed,bytes_total,bytes_done,percent,agent_version,actor,
			started_at,finished_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Container, j.Image, j.SourceNode, j.TargetNode, j.BackupID, j.Status, j.Stage, j.Detail, j.Error,
		j.Domains, j.PortsRemapped, dm, sr, j.BytesTotal, j.BytesDone, j.Percent, j.AgentVersion, j.Actor,
		j.StartedAt, j.FinishedAt)
	return err
}

// UpdateMigrateJob rewrites the mutable terminal columns of a job.
func (s *Store) UpdateMigrateJob(ctx context.Context, j *MigrateJob) error {
	dm, sr := int64(0), int64(0)
	if j.DomainMoved {
		dm = 1
	}
	if j.SourceRemoved {
		sr = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE migrate_jobs SET status=?, stage=?, detail=?, error=?, domains=?, ports_remapped=?,
			domain_moved=?, source_removed=?, finished_at=? WHERE id=?`,
		j.Status, j.Stage, j.Detail, j.Error, j.Domains, j.PortsRemapped, dm, sr, j.FinishedAt, j.ID)
	return err
}

// UpdateMigrateJobProgress updates the streaming-progress columns of a running job.
func (s *Store) UpdateMigrateJobProgress(ctx context.Context, id, stage string, done, total, percent int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE migrate_jobs SET stage=?, bytes_done=?, bytes_total=?, percent=? WHERE id=?`,
		stage, done, total, percent, id)
	return err
}

const migrateJobCols = `id,COALESCE(container,''),COALESCE(image,''),source_node,target_node,COALESCE(backup_id,''),
	status,COALESCE(stage,''),COALESCE(detail,''),COALESCE(error,''),COALESCE(domains,''),COALESCE(ports_remapped,''),
	domain_moved,source_removed,bytes_total,bytes_done,percent,COALESCE(agent_version,''),COALESCE(actor,''),
	started_at,finished_at`

func scanMigrateJob(row interface{ Scan(...any) error }) (MigrateJob, error) {
	var j MigrateJob
	var dm, sr int64
	err := row.Scan(&j.ID, &j.Container, &j.Image, &j.SourceNode, &j.TargetNode, &j.BackupID,
		&j.Status, &j.Stage, &j.Detail, &j.Error, &j.Domains, &j.PortsRemapped, &dm, &sr,
		&j.BytesTotal, &j.BytesDone, &j.Percent, &j.AgentVersion, &j.Actor, &j.StartedAt, &j.FinishedAt)
	j.DomainMoved = dm != 0
	j.SourceRemoved = sr != 0
	return j, err
}

// ListMigrateJobs returns the most recent migration jobs (newest first).
func (s *Store) ListMigrateJobs(ctx context.Context, limit int) ([]MigrateJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+migrateJobCols+` FROM migrate_jobs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MigrateJob
	for rows.Next() {
		j, err := scanMigrateJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
