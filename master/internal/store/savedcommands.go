package store

import (
	"context"
	"time"
)

func (s *Store) CreateSavedCommand(ctx context.Context, c *SavedCommand) error {
	if c.ID == "" {
		c.ID = newID()
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().Unix()
	}
	bi := 0
	if c.Builtin {
		bi = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO saved_commands(id,name,script,builtin,created_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, script=excluded.script, builtin=excluded.builtin`,
		c.ID, c.Name, c.Script, bi, c.CreatedAt)
	return err
}

func (s *Store) ListSavedCommands(ctx context.Context) ([]SavedCommand, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,name,script,COALESCE(builtin,0),created_at FROM saved_commands ORDER BY builtin DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedCommand
	for rows.Next() {
		var c SavedCommand
		var bi int
		if err := rows.Scan(&c.ID, &c.Name, &c.Script, &bi, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Builtin = bi == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSavedCommand(ctx context.Context, id string) error {
	// Builtins are managed by the master and cannot be removed through the API.
	_, err := s.DB.ExecContext(ctx, `DELETE FROM saved_commands WHERE id=? AND builtin=0`, id)
	return err
}
