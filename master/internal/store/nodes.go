package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

// CreateNode inserts a new node with the given enrollment token.
func (s *Store) CreateNode(ctx context.Context, name, enrollmentToken string) (*Node, error) {
	n := &Node{
		ID:              newID(),
		Name:            name,
		EnrollmentToken: enrollmentToken,
		Status:          "pending",
		CreatedAt:       now(),
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO nodes(id,name,enrollment_token,status,created_at) VALUES(?,?,?,?,?)`,
		n.ID, n.Name, n.EnrollmentToken, n.Status, n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// ListNodes returns all nodes.
func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,name,COALESCE(agent_token,''),status,COALESCE(hostname,''),COALESCE(os,''),
			COALESCE(arch,''),COALESCE(kernel,''),COALESCE(ipv4,''),COALESCE(ipv6,''),
			COALESCE(country_code,''),COALESCE(country,''),COALESCE(agent_version,''),
			COALESCE(last_seen,0),created_at,COALESCE(ssh_port,'') FROM nodes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.AgentToken, &n.Status, &n.Hostname, &n.OS,
			&n.Arch, &n.Kernel, &n.IPv4, &n.IPv6, &n.CountryCode, &n.Country,
			&n.AgentVersion, &n.LastSeen, &n.CreatedAt, &n.SshPort); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNode fetches a node by id.
func (s *Store) GetNode(ctx context.Context, id string) (*Node, error) {
	return s.getNode(ctx, `SELECT id,name,COALESCE(agent_token,''),status,COALESCE(hostname,''),
		COALESCE(os,''),COALESCE(arch,''),COALESCE(kernel,''),COALESCE(ipv4,''),COALESCE(ipv6,''),
		COALESCE(country_code,''),COALESCE(country,''),COALESCE(agent_version,''),
		COALESCE(last_seen,0),created_at,COALESCE(ssh_port,'') FROM nodes WHERE id=?`, id)
}

// GetNodeByEnrollment looks up a node by its enrollment token (used at agent enroll).
func (s *Store) GetNodeByEnrollment(ctx context.Context, token string) (*Node, error) {
	return s.getNode(ctx, `SELECT id,name,COALESCE(agent_token,''),status,COALESCE(hostname,''),
		COALESCE(os,''),COALESCE(arch,''),COALESCE(kernel,''),COALESCE(ipv4,''),COALESCE(ipv6,''),
		COALESCE(country_code,''),COALESCE(country,''),COALESCE(agent_version,''),
		COALESCE(last_seen,0),created_at,COALESCE(ssh_port,'') FROM nodes WHERE enrollment_token=?`, token)
}

// GetNodeByAgentToken looks up a node by its long-lived agent token (used at WS connect).
func (s *Store) GetNodeByAgentToken(ctx context.Context, token string) (*Node, error) {
	return s.getNode(ctx, `SELECT id,name,COALESCE(agent_token,''),status,COALESCE(hostname,''),
		COALESCE(os,''),COALESCE(arch,''),COALESCE(kernel,''),COALESCE(ipv4,''),COALESCE(ipv6,''),
		COALESCE(country_code,''),COALESCE(country,''),COALESCE(agent_version,''),
		COALESCE(last_seen,0),created_at,COALESCE(ssh_port,'') FROM nodes WHERE agent_token=?`, token)
}

func (s *Store) getNode(ctx context.Context, q string, args ...any) (*Node, error) {
	row := s.DB.QueryRowContext(ctx, q, args...)
	var n Node
	var last sql.NullInt64
	err := row.Scan(&n.ID, &n.Name, &n.AgentToken, &n.Status, &n.Hostname, &n.OS, &n.Arch,
		&n.Kernel, &n.IPv4, &n.IPv6, &n.CountryCode, &n.Country, &n.AgentVersion, &last, &n.CreatedAt, &n.SshPort)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if last.Valid {
		n.LastSeen = last.Int64
	}
	return &n, nil
}

// SetAgentToken records the long-lived agent token issued at enroll.
func (s *Store) SetAgentToken(ctx context.Context, nodeID, token string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nodes SET agent_token=? WHERE id=?`, token, nodeID)
	return err
}

// RegenerateEnrollment issues a new enrollment token (old becomes invalid).
func (s *Store) RegenerateEnrollment(ctx context.Context, nodeID, token string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nodes SET enrollment_token=? WHERE id=?`, token, nodeID)
	return err
}

// RenameNode updates the display name and the admin-configured SSH port.
func (s *Store) RenameNode(ctx context.Context, nodeID, name, sshPort string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nodes SET name=?, ssh_port=? WHERE id=?`, name, sshPort, nodeID)
	return err
}

// DeleteNode removes a node and its ephemeral container inventory. Keeping
// inventory rows after the owning node is gone pollutes container counts and
// can make old workloads look actionable in the UI.
func (s *Store) DeleteNode(ctx context.Context, nodeID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, table := range []string{"container_scan", "container_names", "containers"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE node_id=?`, nodeID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, nodeID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// NodeSeen marks a node online with the latest data from hello/metrics.
func (s *Store) NodeSeen(ctx context.Context, n *Node) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nodes SET status='online', hostname=?, os=?, arch=?, kernel=?,
		ipv4=?, ipv6=?, agent_version=?, last_seen=? WHERE id=?`,
		n.Hostname, n.OS, n.Arch, n.Kernel, n.IPv4, n.IPv6, n.AgentVersion, time.Now().Unix(), n.ID)
	return err
}

// NodeHeartbeat marks a connected node online without overwriting its hardware
// and IP metadata. Agents send metrics/containers much more often than hello,
// so this keeps status/last_seen accurate after reconnects and stale closes.
func (s *Store) NodeHeartbeat(ctx context.Context, nodeID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nodes SET status='online', last_seen=? WHERE id=?`, time.Now().Unix(), nodeID)
	return err
}

// NodeOffline marks a node offline.
func (s *Store) NodeOffline(ctx context.Context, nodeID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nodes SET status='offline' WHERE id=?`, nodeID)
	return err
}

// SetNodeGeo stores the resolved country for a node's IP.
func (s *Store) SetNodeGeo(ctx context.Context, nodeID, code, country string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nodes SET country_code=?, country=? WHERE id=?`, code, country, nodeID)
	return err
}

// AgentEgress is one node's last reported public egress IPv4.
type AgentEgress struct {
	NodeID    string `json:"node_id"`
	Name      string `json:"name,omitempty"`
	IP        string `json:"ip"`
	UpdatedAt int64  `json:"updated_at"`
}

// UpsertAgentEgress records the public egress IP a node reported (or that we
// observed on its short HTTPS request). Used by the host UFW sync for 8443.
func (s *Store) UpsertAgentEgress(ctx context.Context, nodeID, ip string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO agent_egress(node_id, ip, updated_at) VALUES(?,?,?)
		ON CONFLICT(node_id) DO UPDATE SET ip=excluded.ip, updated_at=excluded.updated_at`,
		nodeID, ip, time.Now().Unix())
	return err
}

// ListAgentEgress returns every stored egress IP joined with node names.
func (s *Store) ListAgentEgress(ctx context.Context) ([]AgentEgress, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT e.node_id, COALESCE(n.name,''), e.ip, e.updated_at
		FROM agent_egress e
		LEFT JOIN nodes n ON n.id = e.node_id
		ORDER BY n.name, e.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentEgress
	for rows.Next() {
		var e AgentEgress
		if err := rows.Scan(&e.NodeID, &e.Name, &e.IP, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
