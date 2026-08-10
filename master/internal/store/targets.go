package store

import "context"

func (s *Store) CreateTarget(ctx context.Context, t *BackupTarget) error {
	if t.ID == "" {
		t.ID = newID()
	}
	t.CreatedAt = now()
	en := 0
	if t.Enabled {
		en = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO backup_targets(id,type,name,config,enabled,created_at) VALUES(?,?,?,?,?,?)`,
		t.ID, t.Type, t.Name, t.Config, en, t.CreatedAt)
	return err
}

func (s *Store) UpdateTarget(ctx context.Context, t *BackupTarget) error {
	en := 0
	if t.Enabled {
		en = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE backup_targets SET type=?, name=?, config=?, enabled=? WHERE id=?`,
		t.Type, t.Name, t.Config, en, t.ID)
	return err
}

func (s *Store) ListTargets(ctx context.Context) ([]BackupTarget, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,type,name,COALESCE(config,''),enabled,created_at FROM backup_targets ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupTarget
	for rows.Next() {
		var t BackupTarget
		var en int
		if err := rows.Scan(&t.ID, &t.Type, &t.Name, &t.Config, &en, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Enabled = en == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTarget(ctx context.Context, id string) (*BackupTarget, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id,type,name,COALESCE(config,''),enabled,created_at FROM backup_targets WHERE id=?`, id)
	var t BackupTarget
	var en int
	if err := row.Scan(&t.ID, &t.Type, &t.Name, &t.Config, &en, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.Enabled = en == 1
	return &t, nil
}

func (s *Store) DeleteTarget(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM backup_targets WHERE id=?`, id)
	return err
}

// UpdateTargetConfig updates only the JSON config (used to persist rotated tokens).
func (s *Store) UpdateTargetConfig(ctx context.Context, id, config string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE backup_targets SET config=? WHERE id=?`, config, id)
	return err
}
