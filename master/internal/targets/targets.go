// Package targets implements backup storage backends (GitHub, OneDrive, VPS/SFTP).
package targets

import (
	"context"
	"encoding/json"
	"fmt"

	"nodepanel/master/internal/store"
)

// Uploader is the interface every backup backend implements.
type Uploader interface {
	Push(ctx context.Context, localPath, remoteName string) error
	Pull(ctx context.Context, remoteName, localPath string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, remoteName string) error
	Test(ctx context.Context) error
}

// ConfigSaver persists a rotated/updated target config back to the store.
type ConfigSaver func(targetID string, newConfigJSON string) error

// New builds the Uploader for a target.
func New(t *store.BackupTarget, saver ConfigSaver) (Uploader, error) {
	switch t.Type {
	case "github":
		var c GithubConfig
		if err := json.Unmarshal([]byte(t.Config), &c); err != nil {
			return nil, err
		}
		return &Github{cfg: c}, nil
	case "onedrive":
		var c OnedriveConfig
		if err := json.Unmarshal([]byte(t.Config), &c); err != nil {
			return nil, err
		}
		return &Onedrive{cfg: c, id: t.ID, saver: saver}, nil
	case "vps":
		var c VPSConfig
		if err := json.Unmarshal([]byte(t.Config), &c); err != nil {
			return nil, err
		}
		return &VPS{cfg: c}, nil
	case "s3":
		var c S3Config
		if err := json.Unmarshal([]byte(t.Config), &c); err != nil {
			return nil, err
		}
		return &S3{cfg: c}, nil
	default:
		return nil, fmt.Errorf("unknown target type %q", t.Type)
	}
}
