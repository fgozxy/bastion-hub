package targets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// GithubConfig stores a PAT-based connection to a private repo.
type GithubConfig struct {
	Token  string `json:"token"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"` // default "main"
	Prefix string `json:"prefix"` // e.g. "nodepanel-backups"
}

type Github struct{ cfg GithubConfig }

// repoLocks serializes pushes to the same repo. A schedule fans out many backups
// in parallel, each pushing to GitHub via the Contents API; concurrent commits
// to one branch race on the branch HEAD and GitHub rejects the loser with 409
// ("is at X but expected Y"). One mutex per owner/repo eliminates that race
// within a process.
var (
	repoLockMu sync.Mutex
	repoLocks  = map[string]*sync.Mutex{}
)

func repoMutex(key string) *sync.Mutex {
	repoLockMu.Lock()
	defer repoLockMu.Unlock()
	if m, ok := repoLocks[key]; ok {
		return m
	}
	m := &sync.Mutex{}
	repoLocks[key] = m
	return m
}

func (g *Github) branch() string {
	if g.cfg.Branch == "" {
		return "main"
	}
	return g.cfg.Branch
}

func (g *Github) apiBase() string {
	return "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo + "/contents"
}

func (g *Github) remotePath(remoteName string) string {
	p := strings.Trim(g.cfg.Prefix, "/")
	if p == "" {
		return remoteName
	}
	return p + "/" + remoteName
}

func (g *Github) Push(ctx context.Context, localPath, remoteName string) error {
	mu := repoMutex(g.cfg.Owner + "/" + g.cfg.Repo)
	mu.Lock()
	defer mu.Unlock()

	// Big archives go through the Git Data API (blobs → one tree → one commit)
	// so an 11 GB backup isn't ~340 commits. Small files use the Contents API.
	if fi, err := os.Stat(localPath); err == nil && fi.Size() > githubShardSize {
		return g.gitDataPush(ctx, localPath, remoteName)
	}
	return g.putRaw(ctx, localPath, remoteName)
}

func (g *Github) Pull(ctx context.Context, remoteName, localPath string) error {
	// Manifest-aware: reassembles sharded objects, falls back to single file.
	// Works for both Contents- and Git-Data-pushed objects (parts are path-addressable files).
	return shardPull(ctx, g, remoteName, localPath)
}

func (g *Github) Delete(ctx context.Context, remoteName string) error {
	mu := repoMutex(g.cfg.Owner + "/" + g.cfg.Repo)
	mu.Lock()
	defer mu.Unlock()
	return g.gitDataDelete(ctx, remoteName)
}

// putRaw uploads a single file via the Contents API (base64). Caller holds the
// per-repo mutex. Retries on 409/422 (stale sha / branch race).
func (g *Github) putRaw(ctx context.Context, localPath, remoteName string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	content := base64.StdEncoding.EncodeToString(data)
	url := g.apiBase() + "/" + g.remotePath(remoteName)

	const maxTries = 5
	var lastErr error
	for attempt := 0; attempt < maxTries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		body := map[string]any{
			"message": "nodepanel backup: " + remoteName,
			"content": content,
			"branch":  g.branch(),
		}
		// if file exists, include its sha to update rather than 422 "already exists"
		if sha, err := g.sha(ctx, remoteName); err == nil && sha != "" {
			body["sha"] = sha
		}
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err // transport error — retry
			continue
		}
		if resp.StatusCode < 300 {
			resp.Body.Close()
			return nil
		}
		eb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("github push %d: %s", resp.StatusCode, string(eb))
		// Retry sha/branch races (409/422) and transient server/rate-limit
		// errors (5xx/429). Other 4xx are real and shouldn't loop.
		if resp.StatusCode == 409 || resp.StatusCode == 422 ||
			resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			continue
		}
		return lastErr
	}
	return lastErr
}

// getRaw downloads a single file (raw). Used by the shard layer for parts +
// manifest, and as the legacy fallback in shardPull.
func (g *Github) getRaw(ctx context.Context, remoteName, localPath string) error {
	url := g.apiBase() + "/" + g.remotePath(remoteName) + "?ref=" + g.branch()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("github pull %d", resp.StatusCode)
	}
	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// delRaw removes a single file via the Contents API (best-effort).
func (g *Github) delRaw(ctx context.Context, remoteName string) error {
	sha, err := g.sha(ctx, remoteName)
	if err != nil {
		return nil
	}
	url := g.apiBase() + "/" + g.remotePath(remoteName)
	body, _ := json.Marshal(map[string]any{
		"message": "nodepanel delete: " + remoteName,
		"sha":     sha,
		"branch":  g.branch(),
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (g *Github) sha(ctx context.Context, remoteName string) (string, error) {
	url := g.apiBase() + "/" + g.remotePath(remoteName) + "?ref=" + g.branch()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("not found")
	}
	var out struct {
		SHA string `json:"sha"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.SHA, nil
}

func (g *Github) List(ctx context.Context, prefix string) ([]string, error) {
	// list the prefix dir
	dir := g.remotePath(prefix)
	dir = path.Dir(dir + "/x") // normalize to a dir
	url := g.apiBase() + "/" + dir + "?ref=" + g.branch()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil
	}
	var items []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&items)
	var out []string
	for _, it := range items {
		if it.Type == "file" {
			out = append(out, it.Name)
		}
	}
	return out, nil
}

func (g *Github) Test(ctx context.Context) error {
	url := "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github test %d", resp.StatusCode)
	}
	return nil
}

// ResolveGithub derives the owner (login) from the PAT and ensures the repo
// exists, creating a private repo if needed. Returns owner + default branch.
// This lets the user paste just a token (+ optional repo name) instead of
// filling in owner/repo/branch by hand.
func ResolveGithub(ctx context.Context, token, repo string) (owner, branch string, err error) {
	if repo == "" {
		repo = "nodepanel-backups"
	}
	hdr := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
	}

	// 1) validate token + read the authenticated user's login
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	hdr(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return "", "", fmt.Errorf("令牌无效 (%d)：%s", resp.StatusCode, truncate(string(b), 200))
	}
	var u struct {
		Login string `json:"login"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&u)
	resp.Body.Close()
	owner = u.Login
	if owner == "" {
		return "", "", fmt.Errorf("无法从令牌解析用户名")
	}

	// 2) does the repo already exist?
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+owner+"/"+repo, nil)
	hdr(req)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode == 200 {
		var r struct {
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if r.DefaultBranch == "" {
			r.DefaultBranch = "main"
		}
		return owner, r.DefaultBranch, nil
	}
	resp.Body.Close()

	// 3) create a private repo (auto-init so the default branch exists)
	body, _ := json.Marshal(map[string]any{"name": repo, "private": true, "auto_init": true})
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/user/repos", bytes.NewReader(body))
	hdr(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("创建仓库失败 (%d)，请确认令牌含 repo 权限：%s", resp.StatusCode, truncate(string(b), 200))
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	return owner, r.DefaultBranch, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// RepoInfo describes a repo the user can back up to.
type RepoInfo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"` // "owner/repo"
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// ListRepos returns the repos the token can push to (owned by the user), so the
// UI can offer a picker instead of asking the user to type owner/repo.
func ListRepos(ctx context.Context, token string) ([]RepoInfo, error) {
	client := http.DefaultClient
	var out []RepoInfo
	for page := 1; page <= 5; page++ {
		url := fmt.Sprintf("https://api.github.com/user/repos?per_page=100&page=%d&affiliation=owner&sort=updated", page)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("拉取仓库失败 (%d)：%s", resp.StatusCode, truncate(string(b), 200))
		}
		var items []struct {
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			Private       bool   `json:"private"`
			DefaultBranch string `json:"default_branch"`
			Permissions   struct {
				Push bool `json:"push"`
			} `json:"permissions"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&items)
		resp.Body.Close()
		for _, it := range items {
			if it.Permissions.Push {
				out = append(out, RepoInfo{
					Name: it.Name, FullName: it.FullName,
					Private: it.Private, DefaultBranch: it.DefaultBranch,
				})
			}
		}
		if len(items) < 100 {
			break
		}
	}
	// private repos first, then by name
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			a, b := out[i], out[j]
			if (!a.Private && b.Private) || (a.Private == b.Private && a.FullName > b.FullName) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}
