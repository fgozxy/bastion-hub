package store

import (
	"context"
	"encoding/json"
	"time"

	"nodepanel/shared/proto"
)

// ReplaceNodeContainers replaces the stored inventory for a node with the latest
// snapshot. Display names are kept in a separate table, so they survive this.
func (s *Store) ReplaceNodeContainers(ctx context.Context, nodeID string, list []Container) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	type imageFingerprint struct {
		image   string
		imageID string
	}
	previous := make(map[string]imageFingerprint)
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(name,''),COALESCE(image,''),COALESCE(image_id,'') FROM containers WHERE node_id=?`, nodeID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for rows.Next() {
		var name string
		var fp imageFingerprint
		if err := rows.Scan(&name, &fp.image, &fp.imageID); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return err
		}
		previous[name] = fp
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = tx.Rollback()
		return err
	}
	_ = rows.Close()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `DELETE FROM containers WHERE node_id=?`, nodeID); err != nil {
		_ = tx.Rollback()
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO containers(id,node_id,container_id,name,image,image_id,state,status,created,updated,update_type,host_ports) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	changedNames := make(map[string]struct{})
	for _, c := range list {
		cid := c.ContainerID
		if len(cid) > 12 {
			cid = cid[:12]
		}
		portsJSON, _ := json.Marshal(c.HostPorts) // nil/empty → "null"/"[]"; decode tolerates both
		if _, err := stmt.ExecContext(ctx, nodeID+"/"+cid, nodeID, c.ContainerID, c.Name, c.Image, c.ImageID, c.State, c.Status, c.Created, now, c.UpdateType, string(portsJSON)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if old, ok := previous[c.Name]; ok {
			if old.image != c.Image || old.imageID != c.ImageID {
				changedNames[c.Name] = struct{}{}
			}
		} else {
			// No previous inventory fingerprint means any legacy cache row with
			// this name cannot be proven to belong to the current container.
			changedNames[c.Name] = struct{}{}
		}
	}
	for name := range changedNames {
		if _, err := tx.ExecContext(ctx, `DELETE FROM container_scan WHERE node_id=? AND name=?`, nodeID, name); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM container_scan
		WHERE node_id=? AND NOT EXISTS (
			SELECT 1 FROM containers c
			WHERE c.node_id=container_scan.node_id AND c.name=container_scan.name
		)`, nodeID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ListContainers returns all stored containers with display name + scan cache.
func (s *Store) ListContainers(ctx context.Context) ([]Container, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.node_id, c.container_id, COALESCE(c.name,''), COALESCE(n.display_name,''),
		       COALESCE(c.image,''), COALESCE(c.image_id,''), COALESCE(c.state,''),
		       COALESCE(c.status,''), COALESCE(c.created,0), COALESCE(c.updated,0),
		       COALESCE(c.update_type,''),
		       COALESCE(sc.has_update,-1), COALESCE(sc.note,''), COALESCE(sc.scanned_at,0),
		       COALESCE(c.host_ports,'')
		FROM containers c
		JOIN nodes owner ON owner.id=c.node_id
		LEFT JOIN container_names n ON n.node_id=c.node_id AND n.name=c.name
		LEFT JOIN container_scan sc ON sc.node_id=c.node_id AND sc.name=c.name
		ORDER BY c.node_id, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Container
	for rows.Next() {
		var c Container
		var portsJSON string
		if err := rows.Scan(&c.NodeID, &c.ContainerID, &c.Name, &c.DisplayName, &c.Image, &c.ImageID, &c.State, &c.Status, &c.Created, &c.Updated, &c.UpdateType, &c.HasUpdate, &c.Note, &c.ScannedAt, &portsJSON); err != nil {
			return nil, err
		}
		c.HostPorts = decodePorts(portsJSON)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ContainersByNode returns the stored inventory for one node (with host ports),
// used by container-migration's domain pre-plan. Cheaper than ListContainers.
func (s *Store) ContainersByNode(ctx context.Context, nodeID string) ([]Container, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT container_id, COALESCE(name,''), COALESCE(host_ports,'')
		 FROM containers WHERE node_id=? ORDER BY name`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Container
	for rows.Next() {
		var c Container
		var portsJSON string
		if err := rows.Scan(&c.ContainerID, &c.Name, &portsJSON); err != nil {
			return nil, err
		}
		c.NodeID = nodeID
		c.HostPorts = decodePorts(portsJSON)
		out = append(out, c)
	}
	return out, rows.Err()
}

// decodePorts parses a stored host_ports JSON column ([]int, "null", or "").
func decodePorts(s string) []int {
	if s == "" || s == "null" {
		return nil
	}
	var p []int
	if json.Unmarshal([]byte(s), &p) == nil {
		return p
	}
	return nil
}

// UpdateContainerScan atomically replaces one node's scan cache. Deleting first
// is intentional: a successful scan is a complete snapshot, so containers
// omitted from it must not retain results from an older scan.
func (s *Store) UpdateContainerScan(ctx context.Context, nodeID string, items []proto.ContainerScanItem) error {
	now := time.Now().Unix()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM container_scan WHERE node_id=?`, nodeID); err != nil {
		_ = tx.Rollback()
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO container_scan(node_id,name,has_update,note,scanned_at) VALUES(?,?,?,?,?)
		ON CONFLICT(node_id,name) DO UPDATE SET has_update=excluded.has_update, note=excluded.note, scanned_at=excluded.scanned_at`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, it := range items {
		if _, err := stmt.ExecContext(ctx, nodeID, it.Name, it.HasUpdate, it.Note, now); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// InvalidateContainerScan clears every cached scan result for a node.
func (s *Store) InvalidateContainerScan(ctx context.Context, nodeID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM container_scan WHERE node_id=?`, nodeID)
	return err
}

// InvalidateContainerScanContainers clears cached results for selected
// containers. refs may contain stable names, full Docker IDs, or 12-char IDs.
func (s *Store) InvalidateContainerScanContainers(ctx context.Context, nodeID string, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM container_scan
			WHERE node_id=? AND (
				name=? OR name IN (
					SELECT name FROM containers
					WHERE node_id=? AND (container_id=? OR substr(container_id,1,12)=?)
				)
			)`, nodeID, ref, nodeID, ref, ref); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// SetContainerName sets a custom display name for a container (by node + name).
func (s *Store) SetContainerName(ctx context.Context, nodeID, name, displayName string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO container_names(node_id,name,display_name) VALUES(?,?,?)
		 ON CONFLICT(node_id,name) DO UPDATE SET display_name=excluded.display_name`,
		nodeID, name, displayName)
	return err
}

// ContainerIDByName resolves a container's current 64-char id from its (stable)
// name on a node. Used by the scheduler so a backup schedule keeps working after
// the container is recreated with a new id but the same name. Returns "" if no
// container with that name is currently reported for the node.
func (s *Store) ContainerIDByName(ctx context.Context, nodeID, name string) string {
	var id string
	_ = s.DB.QueryRowContext(ctx,
		`SELECT container_id FROM containers WHERE node_id=? AND name=? ORDER BY updated DESC LIMIT 1`,
		nodeID, name).Scan(&id)
	return id
}

// ContainerNameByID resolves a container's stable name from its current 64-char
// id on a node — the inverse of ContainerIDByName. Used when persisting a backup
// row so the restore view can join backups by name across container rebuilds
// (the id changes on every recreate, the name does not). Returns "" if no live
// container matches the id.
func (s *Store) ContainerNameByID(ctx context.Context, nodeID, containerID string) string {
	var name string
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(name,'') FROM containers WHERE node_id=? AND container_id=? LIMIT 1`,
		nodeID, containerID).Scan(&name)
	return name
}
