package store

import (
	"context"
	"database/sql"
	"errors"
)

// CreateSession persists a session token.
func (s *Store) CreateSession(ctx context.Context, userID string, ttlSec int64) (string, error) {
	tok := newID()
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO sessions(token,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		tok, userID, now(), now()+ttlSec)
	if err != nil {
		return "", err
	}
	return tok, nil
}

// GetSession returns the user id for a valid, unexpired session.
func (s *Store) GetSession(ctx context.Context, token string) (string, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE token=? AND expires_at>?`, token, now())
	var uid string
	if err := row.Scan(&uid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return uid, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token=?`, token)
	return err
}

// PruneSessions removes expired sessions.
func (s *Store) PruneSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<?`, now())
	return err
}
