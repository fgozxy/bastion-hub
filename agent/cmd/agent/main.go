// NodePanel agent entry point.
package main

import (
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"

	"nodepanel/agent/internal/agent"
)

func main() {
	cfg := flag.String("c", "/etc/nodepanel-agent/config.conf", "config file path")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	// pprof on loopback only, for memory-leak diagnosis. No external exposure;
	// a failed listen just logs and never affects the agent's main loop.
	go func() { log.Println("pprof listen:", http.ListenAndServe("127.0.0.1:6060", nil)) }()

	if err := agent.Run(*cfg); err != nil {
		log.Fatalf("agent: %v", err)
	}
}
