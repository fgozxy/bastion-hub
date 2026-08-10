package store

import "context"

func (s *Store) Audit(ctx context.Context, actor, action, detail string) {
	s.DB.ExecContext(ctx,
		`INSERT INTO audit_log(ts,actor,action,detail) VALUES(?,?,?,?)`,
		now(), actor, action, detail)
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]Audit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,ts,COALESCE(actor,''),COALESCE(action,''),COALESCE(detail,'') FROM audit_log ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audit
	for rows.Next() {
		var a Audit
		if err := rows.Scan(&a.ID, &a.Ts, &a.Actor, &a.Action, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
