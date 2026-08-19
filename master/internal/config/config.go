package config

import (
	"os"
	"path/filepath"
)

// Config holds master runtime configuration.
type Config struct {
	DataDir   string
	Domain    string // public panel domain used in generated agent URLs
	Dev       bool   // enables development auth/cookie behavior
	DevAddr   string // HTTP listen address behind the reverse proxy
	DBPath    string
	AssetsDir string // agent binaries served from /dl/
}

// Default builds a config from a data dir.
func Default(dataDir, domain string) Config {
	return Config{
		DataDir:   dataDir,
		Domain:    domain,
		Dev:       false,
		DevAddr:   ":8080",
		DBPath:    filepath.Join(dataDir, "nodepanel.db"),
		AssetsDir: filepath.Join(dataDir, "assets"),
	}
}

// EnsureDirs creates the directories the master needs.
func (c Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.AssetsDir, filepath.Join(c.DataDir, "backups")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
