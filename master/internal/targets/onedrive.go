package targets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// OnedriveConfig stores a Microsoft Graph connection (device-code flow).
type OnedriveConfig struct {
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
	Folder       string `json:"folder"` // e.g. "NodePanel/backups"
}

const graphScope = "files.readwrite offline_access"

type Onedrive struct {
	cfg   OnedriveConfig
	id    string
	saver ConfigSaver

	mu     sync.Mutex
	access string
	expiry time.Time
}

func (o *Onedrive) folder() string {
	f := strings.TrimPrefix(o.cfg.Folder, "/")
	if f == "" {
		f = "NodePanel/backups"
	}
	return f
}

// DeviceCodeResponse is returned by the device-code endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message"`
}

// RequestDeviceCode starts the device-code flow for a client id.
func RequestDeviceCode(ctx context.Context, clientID string) (*DeviceCodeResponse, error) {
	form := url.Values{
		"client_id": {clientID},
		"scope":     {graphScope},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/common/oauth2/v2.0/devicecode",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var dc DeviceCodeResponse
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("devicecode %d: %s", resp.StatusCode, string(b))
	}
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, err
	}
	return &dc, nil
}

// PollOnce performs a single token-poll attempt for the device-code flow.
// Returns (config, pending, error). pending=true means still awaiting authorization.
func PollOnce(ctx context.Context, clientID, deviceCode string) (OnedriveConfig, bool, error) {
	form := url.Values{
		"client_id":   {clientID},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/common/oauth2/v2.0/token",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return OnedriveConfig{}, false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	_ = json.Unmarshal(body, &tok)
	if tok.Error == "authorization_pending" || tok.Error == "slow_down" {
		return OnedriveConfig{}, true, nil
	}
	if tok.Error != "" {
		return OnedriveConfig{}, false, fmt.Errorf("%s", tok.Error)
	}
	if tok.RefreshToken != "" {
		return OnedriveConfig{ClientID: clientID, RefreshToken: tok.RefreshToken}, false, nil
	}
	return OnedriveConfig{}, false, fmt.Errorf("unexpected response")
}

// PollDeviceToken blocks (polling) until the user authorizes or it expires.
func PollDeviceToken(ctx context.Context, clientID, deviceCode string, interval, expiresIn int) (OnedriveConfig, error) {
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if interval <= 0 {
		interval = 5
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return OnedriveConfig{}, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		form := url.Values{
			"client_id":   {clientID},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode},
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://login.microsoftonline.com/common/oauth2/v2.0/token",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var tok struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Error        string `json:"error"`
		}
		_ = json.Unmarshal(body, &tok)
		if tok.Error == "authorization_pending" {
			continue
		}
		if tok.Error != "" {
			return OnedriveConfig{}, fmt.Errorf("%s", tok.Error)
		}
		if tok.RefreshToken != "" {
			return OnedriveConfig{ClientID: clientID, RefreshToken: tok.RefreshToken}, nil
		}
	}
	return OnedriveConfig{}, fmt.Errorf("device code expired")
}

// token returns a valid access token, refreshing as needed.
func (o *Onedrive) token(ctx context.Context) (string, error) {
	o.mu.Lock()
	if o.access != "" && time.Now().Before(o.expiry.Add(-60*time.Second)) {
		t := o.access
		o.mu.Unlock()
		return t, nil
	}
	o.mu.Unlock()

	form := url.Values{
		"client_id":     {o.cfg.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {o.cfg.RefreshToken},
		"scope":         {graphScope},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/common/oauth2/v2.0/token",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.Error != "" {
		return "", fmt.Errorf("onedrive token: %s", tok.Error)
	}
	o.mu.Lock()
	o.access = tok.AccessToken
	o.expirySet(tok.ExpiresIn)
	rotated := ""
	if tok.RefreshToken != "" && tok.RefreshToken != o.cfg.RefreshToken {
		o.cfg.RefreshToken = tok.RefreshToken
		rotated = tok.RefreshToken
	}
	o.mu.Unlock()
	if rotated != "" && o.saver != nil {
		b, _ := json.Marshal(o.cfg)
		_ = o.saver(o.id, string(b))
	}
	return tok.AccessToken, nil
}

func (o *Onedrive) expirySet(secs int) {
	if secs <= 0 {
		secs = 3600
	}
	o.expiry = time.Now().Add(time.Duration(secs) * time.Second)
}

func (o *Onedrive) do(ctx context.Context, method, url string, body io.Reader, ctype string) (*http.Response, error) {
	tok, err := o.token(ctx)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, method, url, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return http.DefaultClient.Do(req)
}

func (o *Onedrive) itemURL(remoteName string) string {
	return "https://graph.microsoft.com/v1.0/me/drive/root:/" + url.PathEscape(o.folder()) + "/" + url.PathEscape(remoteName) + ":/content"
}

func (o *Onedrive) Push(ctx context.Context, localPath, remoteName string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, _ := f.Stat()
	// create upload session for robustness (handles any size)
	createURL := "https://graph.microsoft.com/v1.0/me/drive/root:/" + url.PathEscape(o.folder()) + "/" + url.PathEscape(remoteName) + ":/createUploadSession"
	resp, err := o.do(ctx, http.MethodPost, createURL, strings.NewReader(`{}`), "application/json")
	if err != nil {
		return err
	}
	var sess struct {
		UploadURL string `json:"uploadUrl"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()
	if sess.UploadURL == "" {
		return fmt.Errorf("onedrive: no upload url (status %d)", resp.StatusCode)
	}

	// Resumable chunked upload. 16 MiB chunks must be a multiple of the 320 KiB
	// Graph requirement (they are) and keep a multi-GB upload to a few hundred
	// requests. Each chunk is retried so one transient PUT can't abort the whole
	// 11 GB upload — the upload session is resumable for 24h.
	const chunk = 16 * 1024 * 1024
	buf := make([]byte, chunk)
	total := fi.Size()
	for off := int64(0); off < total; off += chunk {
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if n == 0 {
			break
		}
		end := off + int64(n) - 1
		if cerr := putOneDriveChunk(ctx, sess.UploadURL, buf[:n], off, end, total); cerr != nil {
			return cerr
		}
		if n < chunk {
			break
		}
	}
	return nil
}

// putOneDriveChunk PUTs one byte range of a resumable upload session, retrying
// transient failures (network errors, 5xx, 429). A final-range response (200/201)
// or a 409 conflict both indicate the file has been committed.
func putOneDriveChunk(ctx context.Context, uploadURL string, b []byte, off, end, total int64) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(b))
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(b)))
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, end, total))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			// 200/201 = last chunk committed the file; 409 rename conflict on
			// completion is benign; 2xx/3xx mid-stream = accepted, keep going.
			if r.StatusCode < 300 || r.StatusCode == http.StatusConflict {
				r.Body.Close()
				return nil
			}
			if r.StatusCode == http.StatusTooManyRequests || r.StatusCode >= 500 {
				body, _ := io.ReadAll(r.Body)
				r.Body.Close()
				lastErr = fmt.Errorf("onedrive chunk %d: %s", r.StatusCode, string(body))
			} else {
				body, _ := io.ReadAll(r.Body)
				r.Body.Close()
				return fmt.Errorf("onedrive chunk %d: %s", r.StatusCode, string(body))
			}
		}
		// brief backoff before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * time.Second):
		}
	}
	return lastErr
}

func (o *Onedrive) Pull(ctx context.Context, remoteName, localPath string) error {
	resp, err := o.do(ctx, http.MethodGet, o.itemURL(remoteName), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("onedrive pull %d", resp.StatusCode)
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func (o *Onedrive) List(ctx context.Context, prefix string) ([]string, error) {
	u := "https://graph.microsoft.com/v1.0/me/drive/root:/" + url.PathEscape(o.folder()) + ":/children?$select=name"
	resp, err := o.do(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var page struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&page)
	out := make([]string, 0, len(page.Value))
	for _, v := range page.Value {
		out = append(out, v.Name)
	}
	return out, nil
}

func (o *Onedrive) Delete(ctx context.Context, remoteName string) error {
	u := "https://graph.microsoft.com/v1.0/me/drive/root:/" + url.PathEscape(o.folder()) + "/" + url.PathEscape(remoteName)
	resp, err := o.do(ctx, http.MethodDelete, u, nil, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (o *Onedrive) Test(ctx context.Context) error {
	resp, err := o.do(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me/drive", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("onedrive test %d", resp.StatusCode)
	}
	return nil
}
