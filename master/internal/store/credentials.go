package store

import "context"

func (s *Store) CreateCredential(ctx context.Context, c *Credential) error {
	if c.ID == "" {
		c.ID = newID()
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO credentials(id,name,pub_key,priv_key,fingerprint,kind,source,node_id,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.PubKey, c.PrivKey, c.Fingerprint, c.Kind, c.Source, c.NodeID, c.CreatedAt)
	return err
}

func (s *Store) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id,name,COALESCE(pub_key,''),COALESCE(priv_key,''),COALESCE(fingerprint,''),
			COALESCE(kind,''),COALESCE(source,''),COALESCE(node_id,''),created_at
		 FROM credentials ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.Name, &c.PubKey, &c.PrivKey, &c.Fingerprint, &c.Kind, &c.Source, &c.NodeID, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) BindCredentialNode(ctx context.Context, id, nodeID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE credentials SET node_id=? WHERE id=?`, nodeID, id)
	return err
}

func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM credentials WHERE id=?`, id)
	return err
}
