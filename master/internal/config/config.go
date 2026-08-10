package config

import (
	"os"
	"path/filepath"
)

// Config holds master runtime configuration.
type Config struct {
	DataDir   string
	Domain    string // panel domain, e.g. panel.example.com
	Dev       bool   // plain HTTP dev mode (no TLS)
	DevAddr   string // dev listen address, e.g. :8080
	HTTPAddr  string // :80 for autocert http-01 + redirect
	HTTPSAddr string // :443
	CertDir   string // autocert cache
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
		HTTPAddr:  ":80",
		HTTPSAddr: ":443",
		CertDir:   filepath.Join(dataDir, "certs"),
		DBPath:    filepath.Join(dataDir, "nodepanel.db"),
		AssetsDir: filepath.Join(dataDir, "assets"),
	}
}

// EnsureDirs creates the directories the master needs.
func (c Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.CertDir, c.AssetsDir, filepath.Join(c.DataDir, "backups")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
