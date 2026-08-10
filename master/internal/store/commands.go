package store

import "context"

func (s *Store) CreateCommand(ctx context.Context, c *Command) error {
	if c.ID == "" {
		c.ID = newID()
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO commands(id,node_ids,cmd,status,exit_code,author,created_at,finished_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		c.ID, c.NodeIDs, c.Cmd, c.Status, c.ExitCode, c.Author, c.CreatedAt, c.FinishedAt)
	return err
}

func (s *Store) FinishCommand(ctx context.Context, id string, status string, exit int) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE commands SET status=?, exit_code=?, finished_at=? WHERE id=?`,
		status, exit, now(), id)
	return err
}

func (s *Store) AppendCommandLine(ctx context.Context, commandID, nodeID string, seq int, stream, data string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO command_lines(command_id,node_id,seq,stream,data) VALUES(?,?,?,?,?)`,
		commandID, nodeID, seq, stream, data)
	return err
}

func (s *Store) ListCommands(ctx context.Context, limit int) ([]Command, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,COALESCE(node_ids,''),COALESCE(cmd,''),COALESCE(status,''),COALESCE(exit_code,0),
		 COALESCE(author,''),created_at,COALESCE(finished_at,0) FROM commands ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		var c Command
		if err := rows.Scan(&c.ID, &c.NodeIDs, &c.Cmd, &c.Status, &c.ExitCode, &c.Author, &c.CreatedAt, &c.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCommand(ctx context.Context, id string) (*Command, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id,COALESCE(node_ids,''),COALESCE(cmd,''),COALESCE(status,''),COALESCE(exit_code,0),
		 COALESCE(author,''),created_at,COALESCE(finished_at,0) FROM commands WHERE id=?`, id)
	var c Command
	if err := row.Scan(&c.ID, &c.NodeIDs, &c.Cmd, &c.Status, &c.ExitCode, &c.Author, &c.CreatedAt, &c.FinishedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) GetCommandLines(ctx context.Context, id string) ([]CommandLine, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT COALESCE(node_id,''),seq,COALESCE(stream,''),COALESCE(data,'') FROM command_lines WHERE command_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandLine
	for rows.Next() {
		var l CommandLine
		if err := rows.Scan(&l.NodeID, &l.Seq, &l.Stream, &l.Data); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}
