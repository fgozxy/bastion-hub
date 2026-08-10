// Package authapi implements login/logout/profile and the browser websocket.
package authapi

import (
	"net/http"

	"github.com/gorilla/websocket"

	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/browserhub"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Service struct {
	Store   *store.Store
	Browser *browserhub.Hub
	Dev     bool // controls cookie Secure flag
}

// Login POST /api/auth/login {username,password}
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	tok, err := auth.Login(r.Context(), s.Store, body.Username, body.Password)
	if err != nil {
		httpx.Err(w, 401, "用户名或密码错误")
		return
	}
	// Mark the cookie Secure when the request arrived over HTTPS (i.e. behind a
	// TLS-terminating reverse proxy that sets X-Forwarded-Proto), even though the
	// master itself listens on plain HTTP.
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	auth.SetSessionCookie(w, tok, secure)
	s.Store.Audit(r.Context(), body.Username, "auth.login", "")
	httpx.OK(w, map[string]string{"ok": "1"})
}

// Logout POST /api/auth/logout
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	auth.ClearSessionCookie(w)
	httpx.OK(w, map[string]string{"ok": "1"})
}

// Me GET /api/auth/me
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	uid := auth.UserID(r.Context())
	if uid == "" {
		httpx.OK(w, map[string]any{"authenticated": false})
		return
	}
	u, err := s.Store.GetUserByID(r.Context(), uid)
	if err != nil {
		httpx.OK(w, map[string]any{"authenticated": false})
		return
	}
	httpx.OK(w, map[string]any{"authenticated": true, "username": u.Username})
}

// BrowserWS GET /api/ws
func (s *Service) BrowserWS(w http.ResponseWriter, r *http.Request) {
	if !auth.IsLoggedIn(s.Store, r) {
		httpx.Err(w, 401, "unauthorized")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	go s.Browser.ServeBrowser(conn)
}
