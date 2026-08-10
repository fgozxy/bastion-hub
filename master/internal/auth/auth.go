// Package auth handles user authentication and session middleware.
package auth

import (
	"context"
	"errors"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"nodepanel/master/internal/store"
)

const (
	CookieName = "np_session"
	sessionTTL = 7 * 24 * 3600
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
)

type ctxKey string

const userKey ctxKey = "user"

// EnsureAdmin creates the initial admin user when no users exist.
func EnsureAdmin(ctx context.Context, s *store.Store, username, password string) error {
	n, err := s.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if username == "" {
		username = "admin"
	}
	if password == "" {
		password = "admin" // first-run default; user is prompted to change it
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.CreateUser(ctx, username, string(hash))
	return err
}

// Login validates credentials and returns a session token.
func Login(ctx context.Context, s *store.Store, username, password string) (string, error) {
	u, err := s.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", ErrInvalidCredentials
		}
		return "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return "", ErrInvalidCredentials
	}
	return s.CreateSession(ctx, u.ID, sessionTTL)
}

// ChangeCredentials updates username and (optionally) password.
func ChangeCredentials(ctx context.Context, s *store.Store, userID, username, newPassword string) error {
	hash := ""
	if newPassword != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		hash = string(h)
	}
	return s.UpdateCredentials(ctx, userID, username, hash)
}

// Middleware validates the session cookie (or ?token= query) and loads the user id.
func Middleware(s *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := sessionToken(r)
			if tok == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			uid, err := s.GetSession(r.Context(), tok)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), userKey, uid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if q := r.URL.Query().Get("token"); q != "" {
		return q
	}
	if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
		return h[7:]
	}
	return ""
}

// UserID extracts the authenticated user id from context.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userKey).(string)
	return v
}

// SetSessionCookie writes the session cookie on the response.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionTTL,
	})
}

// ClearSessionCookie clears the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
	})
}

// IsLoggedIn reports whether the request carries a valid session.
func IsLoggedIn(s *store.Store, r *http.Request) bool {
	tok := sessionToken(r)
	if tok == "" {
		return false
	}
	_, err := s.GetSession(r.Context(), tok)
	return err == nil
}
