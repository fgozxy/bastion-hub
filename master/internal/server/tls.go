package server

import (
	"crypto/tls"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// runTLS serves HTTPS via Let's Encrypt (autocert) on :443 with the HTTP-01
// challenge + redirect on :80.
func runTLS(d *Deps, handler http.Handler) error {
	m := autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(d.Cfg.CertDir),
	}
	if d.Cfg.Domain != "" {
		m.HostPolicy = autocert.HostWhitelist(d.Cfg.Domain)
	}

	// :80 — ACME http-01 challenge and redirect to https.
	go func() {
		srv := &http.Server{
			Addr:              d.Cfg.HTTPAddr,
			Handler:           m.HTTPHandler(nil),
			ReadHeaderTimeout: 10 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("http(:80) stopped: %v", err)
		}
	}()

	// :443 — main app.
	tlsCfg := &tls.Config{
		GetCertificate:           m.GetCertificate,
		MinVersion:               tls.VersionTLS12,
		PreferServerCipherSuites: true,
	}
	srv := &http.Server{
		Addr:              d.Cfg.HTTPSAddr,
		Handler:           handler,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("NodePanel master listening on https (domain=%s)", d.Cfg.Domain)
	return srv.ListenAndServeTLS("", "")
}
