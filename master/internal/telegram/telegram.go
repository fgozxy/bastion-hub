// Package telegram sends notifications via the Telegram Bot API.
package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nodepanel/master/internal/store"
)

type Config struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

type Service struct {
	Store  *store.Store
	client *http.Client
}

func New(s *store.Store) *Service {
	return &Service{Store: s, client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *Service) Load(ctx context.Context) Config {
	raw, _ := s.Store.GetSetting(ctx, "telegram")
	var c Config
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

func (s *Service) Save(ctx context.Context, c Config) error {
	b, _ := json.Marshal(c)
	return s.Store.SetSetting(ctx, "telegram", string(b))
}

// Notify implements backup.Notifier.
func (s *Service) Notify(ctx context.Context, msg string) {
	c := s.Load(ctx)
	if c.BotToken == "" || c.ChatID == "" {
		return
	}
	_ = s.send(ctx, c.BotToken, c.ChatID, msg)
}

func (s *Service) send(ctx context.Context, botToken, chatID, msg string) error {
	endpoint := "https://api.telegram.org/bot" + botToken + "/sendMessage"
	form := url.Values{"chat_id": {chatID}, "text": {msg}}
	// Messages that explicitly carry HTML <pre> need parse_mode=HTML. Plain
	// notifications and current backup reports are sent as plain text.
	if strings.Contains(msg, "<pre>") {
		form.Set("parse_mode", "HTML")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errTG(resp.StatusCode)
	}
	return nil
}

// Test sends a test message using the provided (or stored) config.
func (s *Service) Test(ctx context.Context, botToken, chatID string) error {
	if botToken == "" || chatID == "" {
		c := s.Load(ctx)
		botToken, chatID = c.BotToken, c.ChatID
	}
	return s.send(ctx, botToken, chatID, "✅ NodePanel Telegram 通知测试成功")
}

type tgErr int

func (tgErr) Error() string { return "telegram api error" }
func errTG(code int) error  { return tgErr(code) }
