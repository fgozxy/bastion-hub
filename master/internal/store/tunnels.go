package store

import (
	"context"
	"database/sql"
	"errors"
)

// TunnelToken persists a remotely-managed tunnel's connector token + the node it
// was provisioned on. The token is returned once by Cloudflare at creation and
// is needed to re-provision the node's cloudflared service after a reinstall.
type TunnelToken struct {
	TunnelID  string
	Token     string
	NodeID    string
	CreatedAt int64
}

// SetTunnelToken stores (or replaces) the connector token for a tunnel.
func (s *Store) SetTunnelToken(ctx context.Context, tunnelID, token, nodeID string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO tunnel_tokens(tunnel_id,token,node_id,created_at) VALUES(?,?,?,?)
		 ON CONFLICT(tunnel_id) DO UPDATE SET token=excluded.token, node_id=excluded.node_id`,
		tunnelID, token, nodeID, now())
	return err
}

// GetTunnelToken fetches the persisted token for a tunnel. Returns ErrNotFound
// when the tunnel was not created from the panel.
func (s *Store) GetTunnelToken(ctx context.Context, tunnelID string) (*TunnelToken, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT tunnel_id,token,node_id,created_at FROM tunnel_tokens WHERE tunnel_id=?`, tunnelID)
	var t TunnelToken
	if err := row.Scan(&t.TunnelID, &t.Token, &t.NodeID, &t.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// ListTunnelTokens returns all panel-created tunnels as tunnel_id → TunnelToken.
func (s *Store) ListTunnelTokens(ctx context.Context) (map[string]TunnelToken, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT tunnel_id,token,node_id,created_at FROM tunnel_tokens`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TunnelToken{}
	for rows.Next() {
		var t TunnelToken
		if err := rows.Scan(&t.TunnelID, &t.Token, &t.NodeID, &t.CreatedAt); err != nil {
			return nil, err
		}
		out[t.TunnelID] = t
	}
	return out, rows.Err()
}

// DeleteTunnelToken removes a tunnel's persisted token.
func (s *Store) DeleteTunnelToken(ctx context.Context, tunnelID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM tunnel_tokens WHERE tunnel_id=?`, tunnelID)
	return err
}
