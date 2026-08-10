package store

import "context"

func (s *Store) CreateSchedule(ctx context.Context, sc *Schedule) error {
	if sc.ID == "" {
		sc.ID = newID()
	}
	sc.CreatedAt = now()
	en := 0
	if sc.Enabled {
		en = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO schedules(id,type,node_id,config,cron,enabled,last_run,next_run,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		sc.ID, sc.Type, sc.NodeID, sc.Config, sc.Cron, en, sc.LastRun, sc.NextRun, sc.CreatedAt)
	return err
}

func (s *Store) UpdateSchedule(ctx context.Context, sc *Schedule) error {
	en := 0
	if sc.Enabled {
		en = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE schedules SET type=?, node_id=?, config=?, cron=?, enabled=? WHERE id=?`,
		sc.Type, sc.NodeID, sc.Config, sc.Cron, en, sc.ID)
	return err
}

func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,type,COALESCE(node_id,''),COALESCE(config,''),COALESCE(cron,''),enabled,
		 COALESCE(last_run,0),COALESCE(next_run,0),created_at FROM schedules ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var en int
		if err := rows.Scan(&sc.ID, &sc.Type, &sc.NodeID, &sc.Config, &sc.Cron, &en, &sc.LastRun, &sc.NextRun, &sc.CreatedAt); err != nil {
			return nil, err
		}
		sc.Enabled = en == 1
		out = append(out, sc)
	}
	return out, nil
}

func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM schedules WHERE id=?`, id)
	return err
}

func (s *Store) MarkScheduleRun(ctx context.Context, id string, ts int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE schedules SET last_run=? WHERE id=?`, ts, id)
	return err
}
