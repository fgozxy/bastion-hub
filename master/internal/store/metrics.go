package store

import "context"

// RecordMetric stores a metric sample.
func (s *Store) RecordMetric(ctx context.Context, m Metric) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO metrics(node_id,ts,cpu,mem_used,mem_total,disk_used,disk_total,load1)
		 VALUES(?,?,?,?,?,?,?,?)`,
		m.NodeID, m.Ts, m.CPU, m.MemUsed, m.MemTotal, m.DiskUsed, m.DiskTotal, m.Load1)
	return err
}

// RecentMetrics returns the last n samples for a node (chronological).
func (s *Store) RecentMetrics(ctx context.Context, nodeID string, limit int) ([]Metric, error) {
	if limit <= 0 || limit > 1000 {
		limit = 60
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT node_id,ts,cpu,mem_used,mem_total,disk_used,disk_total,load1 FROM metrics
		 WHERE node_id=? ORDER BY ts DESC LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Metric
	for rows.Next() {
		var m Metric
		if err := rows.Scan(&m.NodeID, &m.Ts, &m.CPU, &m.MemUsed, &m.MemTotal, &m.DiskUsed, &m.DiskTotal, &m.Load1); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	// reverse to chronological
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// LatestMetrics returns the most recent sample per node (map).
func (s *Store) LatestMetrics(ctx context.Context) (map[string]Metric, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT node_id,ts,cpu,mem_used,mem_total,disk_used,disk_total,load1 FROM metrics
		 WHERE ts IN (SELECT MAX(ts) FROM metrics GROUP BY node_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Metric{}
	for rows.Next() {
		var m Metric
		if err := rows.Scan(&m.NodeID, &m.Ts, &m.CPU, &m.MemUsed, &m.MemTotal, &m.DiskUsed, &m.DiskTotal, &m.Load1); err != nil {
			return nil, err
		}
		out[m.NodeID] = m
	}
	return out, rows.Err()
}

// PruneMetrics deletes samples older than maxAge seconds.
func (s *Store) PruneMetrics(ctx context.Context, beforeTs int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM metrics WHERE ts<?`, beforeTs)
	return err
}
