// Package komari is a thin client for the Komari monitor admin API. The 探针
// (probe) integration uses it to add/list monitored clients from NodePanel
// without modifying the Komari project itself — we only call its HTTP API with
// an admin API key (Authorization: Bearer <key>).
package komari

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nodepanel/master/internal/store"
)

// DefaultInstallURL is the official komari-agent one-click install script.
const DefaultInstallURL = "https://raw.githubusercontent.com/komari-monitor/komari-agent/master/install.sh"

// Client talks to one Komari instance's admin API.
type Client struct {
	baseURL string
	apiKey  string
	hc      *http.Client
}

// New builds a client. baseURL is the Komari panel URL (e.g. https://komari.example.com).
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Valid reports whether both base URL and API key are configured.
func (c *Client) Valid() bool { return c.baseURL != "" && c.apiKey != "" }

// BaseURL returns the configured panel URL (used to show the target in the UI).
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("komari %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(data))
	}
	// Komari wraps responses in {status, message, ...}; surface non-success.
	var env struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &env) == nil && env.Status != "" && env.Status != "success" {
		msg := env.Message
		if msg == "" {
			msg = env.Status
		}
		return nil, fmt.Errorf("komari: %s", msg)
	}
	return data, nil
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// ClientInfo is one Komari monitored client (node).
type ClientInfo struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	Ipv4 string `json:"ipv4"`
}

// ListClients returns all monitored clients. Used to exclude already-joined nodes.
func (c *Client) ListClients(ctx context.Context) ([]ClientInfo, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/admin/client/list", nil)
	if err != nil {
		return nil, err
	}
	var out []ClientInfo
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddClient creates a monitored client with the given name and returns its uuid
// + the agent token used to enroll the komari-agent on the node.
func (c *Client) AddClient(ctx context.Context, name string) (uuid, token string, err error) {
	data, err := c.do(ctx, http.MethodPost, "/api/admin/client/add", map[string]string{"name": name})
	if err != nil {
		return "", "", err
	}
	var r struct {
		Status string `json:"status"`
		UUID   string `json:"uuid"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", "", err
	}
	if r.UUID == "" || r.Token == "" {
		return "", "", fmt.Errorf("komari: add %q returned no uuid/token", name)
	}
	return r.UUID, r.Token, nil
}

// RemoveClient deletes a monitored client. Best-effort rollback when the agent
// install on a node fails after the Komari client was already created.
func (c *Client) RemoveClient(ctx context.Context, uuid string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/admin/client/"+uuid+"/remove", nil)
	return err
}

// Config is the persisted Komari integration setting.
type Config struct {
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	InstallURL string `json:"install_url"`
}

// LoadConfig reads the 'komari' setting, applying the default install URL.
func LoadConfig(ctx context.Context, st *store.Store) Config {
	raw, _ := st.GetSetting(ctx, "komari")
	var c Config
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	if c.InstallURL == "" {
		c.InstallURL = DefaultInstallURL
	}
	return c
}

// Client builds an API client from the config.
func (c Config) Client() *Client { return New(c.BaseURL, c.APIKey) }
