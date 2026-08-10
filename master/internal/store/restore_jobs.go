package store

import "context"

// RestoreJob is one (backup → target node) restore task, persisted so live
// progress and terminal outcomes survive a page refresh and form a history.
// Status is the tri-state running | ok | partial | failed (partial = data was
// restored but the container was intentionally not rebuilt, e.g. a same-name
// container already exists on the target).
type RestoreJob struct {
	ID           string `json:"id"`
	BackupID     string `json:"backup_id"`
	Container    string `json:"container"`
	Image        string `json:"image"`
	OriginNode   string `json:"origin_node"`
	TargetNode   string `json:"target_node"`
	Status       string `json:"status"` // running | ok | partial | failed
	Stage        string `json:"stage"`  // download | recreate | "" (final)
	Detail       string `json:"detail"`
	Error        string `json:"error"`
	Recreated    bool   `json:"recreated"`
	BytesTotal   int64  `json:"bytes_total"`
	BytesDone    int64  `json:"bytes_done"`
	Percent      int64  `json:"percent"`
	AgentVersion string `json:"agent_version"`
	Actor        string `json:"actor"`
	StartedAt    int64  `json:"started_at"`
	FinishedAt   int64  `json:"finished_at"`
}

func (s *Store) CreateRestoreJob(ctx context.Context, j *RestoreJob) error {
	if j.ID == "" {
		j.ID = newID()
	}
	if j.StartedAt == 0 {
		j.StartedAt = now()
	}
	rec := int64(0)
	if j.Recreated {
		rec = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO restore_jobs(id,backup_id,container,image,origin_node,target_node,status,stage,detail,error,
			recreated,bytes_total,bytes_done,percent,agent_version,actor,started_at,finished_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.BackupID, j.Container, j.Image, j.OriginNode, j.TargetNode, j.Status, j.Stage, j.Detail, j.Error,
		rec, j.BytesTotal, j.BytesDone, j.Percent, j.AgentVersion, j.Actor, j.StartedAt, j.FinishedAt)
	return err
}

// UpdateRestoreJob rewrites the mutable terminal columns of a job.
func (s *Store) UpdateRestoreJob(ctx context.Context, j *RestoreJob) error {
	rec := int64(0)
	if j.Recreated {
		rec = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE restore_jobs SET status=?, stage=?, detail=?, error=?, recreated=?, finished_at=? WHERE id=?`,
		j.Status, j.Stage, j.Detail, j.Error, rec, j.FinishedAt, j.ID)
	return err
}

// UpdateRestoreJobProgress updates the streaming-progress columns of a running job.
func (s *Store) UpdateRestoreJobProgress(ctx context.Context, id, stage string, done, total, percent int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE restore_jobs SET stage=?, bytes_done=?, bytes_total=?, percent=? WHERE id=?`,
		stage, done, total, percent, id)
	return err
}

const restoreJobCols = `id,backup_id,COALESCE(container,''),COALESCE(image,''),COALESCE(origin_node,''),target_node,
	status,COALESCE(stage,''),COALESCE(detail,''),COALESCE(error,''),recreated,
	bytes_total,bytes_done,percent,COALESCE(agent_version,''),COALESCE(actor,''),started_at,finished_at`

func scanRestoreJob(row interface{ Scan(...any) error }) (RestoreJob, error) {
	var j RestoreJob
	var rec int64
	err := row.Scan(&j.ID, &j.BackupID, &j.Container, &j.Image, &j.OriginNode, &j.TargetNode,
		&j.Status, &j.Stage, &j.Detail, &j.Error, &rec,
		&j.BytesTotal, &j.BytesDone, &j.Percent, &j.AgentVersion, &j.Actor, &j.StartedAt, &j.FinishedAt)
	j.Recreated = rec != 0
	return j, err
}

// ListRestoreJobs returns the most recent restore jobs (newest first).
func (s *Store) ListRestoreJobs(ctx context.Context, limit int) ([]RestoreJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+restoreJobCols+` FROM restore_jobs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RestoreJob
	for rows.Next() {
		j, err := scanRestoreJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
