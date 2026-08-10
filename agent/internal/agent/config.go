package agent

import (
	"encoding/json"
	"os"
	"strings"
)

// FileConfig is the key=value file written by the install script.
type FileConfig struct {
	Server string // https://panel.example.com
	Token  string // enrollment token
}

// LoadConfig parses the simple key = "value" config file.
func LoadConfig(path string) (*FileConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &FileConfig{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		switch key {
		case "server":
			cfg.Server = strings.TrimRight(val, "/")
		case "token":
			cfg.Token = val
		}
	}
	return cfg, nil
}

// State persists enrollment across restarts.
type State struct {
	AgentToken string `json:"agent_token"`
	NodeID     string `json:"node_id"`
}

func statePath() string {
	if p := os.Getenv("NODEPANEL_STATE"); p != "" {
		return p
	}
	return "/var/lib/nodepanel-agent/state.json"
}

func LoadState() (*State, error) {
	b, err := os.ReadFile(statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}
	var s State
	_ = json.Unmarshal(b, &s)
	return &s, nil
}

func SaveState(s *State) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(statePath(), b, 0o600)
}
