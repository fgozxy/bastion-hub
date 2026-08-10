package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (*User, error) {
	u := &User{ID: newID(), Username: username, PasswordHash: passwordHash, CreatedAt: now()}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO users(id,username,password_hash,created_at) VALUES(?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id,username,password_hash,created_at FROM users WHERE username=?`, username)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id,username,password_hash,created_at FROM users WHERE id=?`, id)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) UpdateCredentials(ctx context.Context, userID, username, passwordHash string) error {
	if passwordHash != "" {
		_, err := s.DB.ExecContext(ctx, `UPDATE users SET username=?, password_hash=? WHERE id=?`, username, passwordHash, userID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET username=? WHERE id=?`, username, userID)
	return err
}
