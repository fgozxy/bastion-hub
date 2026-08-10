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
	"time"
)

// This file implements GitHub's Git Data API (blobs → tree → commit → ref) for
// pushing large backups as a SINGLE commit. The Contents API creates one commit
// per file, so a sharded 11 GB backup would produce ~340 commits/night and bloat
// the repo history. With the Git Data API all parts land in one tree under one
// commit. Parts are still stored as ordinary files (path-addressable), so Pull
// is unchanged (Contents GET by path).

// treeEntry is one entry in a git/trees create call. SHA == nil means "delete
// this path" (when used against a base_tree).
type treeEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"`
}

// ghReq is a small helper for the GitHub REST API: auth + accept headers and a
// retry on transient transport/5xx/429 errors (a multi-hundred-blob push will
// brush against these).
func (g *Github) ghReq(ctx context.Context, method, url string, body []byte) (int, []byte, error) {
	const tries = 5
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
		req.Header.Set("Accept", "application/vnd.github+json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue // transport error — retry
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("github %d: %s", resp.StatusCode, truncate(string(rb), 120))
			continue // transient — retry
		}
		return resp.StatusCode, rb, nil
	}
	return 0, nil, lastErr
}

// uploadBlob POSTs a base64 blob and returns its git sha.
func (g *Github) uploadBlob(ctx context.Context, data []byte) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"content":  base64.StdEncoding.EncodeToString(data),
		"encoding": "base64",
	})
	url := "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo + "/git/blobs"
	code, rb, err := g.ghReq(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", err
	}
	if code >= 300 {
		return "", fmt.Errorf("git blob %d: %s", code, truncate(string(rb), 200))
	}
	var out struct {
		SHA string `json:"sha"`
	}
	_ = json.Unmarshal(rb, &out)
	if out.SHA == "" {
		return "", fmt.Errorf("git blob: no sha")
	}
	return out.SHA, nil
}

// headTree returns the branch's current commit sha and its tree sha.
func (g *Github) headTree(ctx context.Context) (commitSha, treeSha string, err error) {
	refURL := "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo + "/git/ref/heads/" + g.branch()
	code, rb, err := g.ghReq(ctx, http.MethodGet, refURL, nil)
	if err != nil {
		return "", "", err
	}
	if code >= 300 {
		return "", "", fmt.Errorf("git ref %d: %s", code, truncate(string(rb), 200))
	}
	var ref struct {
		Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
	}
	_ = json.Unmarshal(rb, &ref)
	commitSha = ref.Object.SHA
	if commitSha == "" {
		return "", "", fmt.Errorf("git ref: no sha")
	}
	commitURL := "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo + "/git/commits/" + commitSha
	code, rb, err = g.ghReq(ctx, http.MethodGet, commitURL, nil)
	if err != nil {
		return "", "", err
	}
	if code >= 300 {
		return "", "", fmt.Errorf("git commit %d: %s", code, truncate(string(rb), 200))
	}
	var c struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	_ = json.Unmarshal(rb, &c)
	treeSha = c.Tree.SHA
	if treeSha == "" {
		return "", "", fmt.Errorf("git commit: no tree sha")
	}
	return commitSha, treeSha, nil
}

// createTree builds a tree on top of baseTreeSha with the given entries.
func (g *Github) createTree(ctx context.Context, baseTreeSha string, entries []treeEntry) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"base_tree": baseTreeSha,
		"tree":      entries,
	})
	url := "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo + "/git/trees"
	code, rb, err := g.ghReq(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", err
	}
	if code >= 300 {
		return "", fmt.Errorf("git tree %d: %s", code, truncate(string(rb), 200))
	}
	var out struct {
		SHA string `json:"sha"`
	}
	_ = json.Unmarshal(rb, &out)
	if out.SHA == "" {
		return "", fmt.Errorf("git tree: no sha")
	}
	return out.SHA, nil
}

// createCommit creates a commit with the given tree and parents.
func (g *Github) createCommit(ctx context.Context, treeSha string, parents []string, msg string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"message": msg,
		"tree":    treeSha,
		"parents": parents,
	})
	url := "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo + "/git/commits"
	code, rb, err := g.ghReq(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", err
	}
	if code >= 300 {
		return "", fmt.Errorf("git commit %d: %s", code, truncate(string(rb), 200))
	}
	var out struct {
		SHA string `json:"sha"`
	}
	_ = json.Unmarshal(rb, &out)
	if out.SHA == "" {
		return "", fmt.Errorf("git commit: no sha")
	}
	return out.SHA, nil
}

// updateRef fast-forwards the branch to sha (force=false). Returns a non-nil
// error on non-fast-forward (ref moved), which the caller treats as "retry".
func (g *Github) updateRef(ctx context.Context, sha string) error {
	body, _ := json.Marshal(map[string]any{"sha": sha, "force": false})
	url := "https://api.github.com/repos/" + g.cfg.Owner + "/" + g.cfg.Repo + "/git/refs/heads/" + g.branch()
	code, rb, err := g.ghReq(ctx, http.MethodPatch, url, body)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("git ref update %d: %s", code, truncate(string(rb), 200))
	}
	return nil
}

// gitDataPush uploads the file as N blobs + a manifest blob and commits them all
// at once (one commit). Retries the tree/commit/ref sequence if the branch moved
// between reading HEAD and updating the ref.
func (g *Github) gitDataPush(ctx context.Context, localPath, remoteName string) error {
	fi, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	total := fi.Size()
	n := int(total / githubShardSize)
	if total%githubShardSize != 0 || n == 0 {
		n++
	}

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Upload each part as a blob; keep only its sha (peak memory ≈ one part).
	buf := make([]byte, githubShardSize)
	entries := make([]treeEntry, 0, n+1)
	for i := 0; i < n; i++ {
		rd, rerr := io.ReadFull(f, buf)
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return rerr
		}
		if rd == 0 {
			break
		}
		sha, err := g.uploadBlob(ctx, buf[:rd])
		if err != nil {
			return fmt.Errorf("git blob part %d: %w", i, err)
		}
		s := sha
		entries = append(entries, treeEntry{Path: partName(remoteName, i), Mode: "100644", Type: "blob", SHA: &s})
	}

	// Manifest blob (same format the Contents-shard Pull already understands).
	mb, _ := json.Marshal(shardManifest{Shards: n, Size: total})
	msha, err := g.uploadBlob(ctx, mb)
	if err != nil {
		return fmt.Errorf("git blob manifest: %w", err)
	}
	entries = append(entries, treeEntry{Path: manifestName(remoteName), Mode: "100644", Type: "blob", SHA: &msha})

	// Commit the tree; retry the read-HEAD→commit→ref sequence on a ref race.
	const tries = 5
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		headCommit, baseTree, err := g.headTree(ctx)
		if err != nil {
			return err
		}
		treeSha, err := g.createTree(ctx, baseTree, entries)
		if err != nil {
			return err
		}
		commitSha, err := g.createCommit(ctx, treeSha, []string{headCommit}, "nodepanel backup: "+remoteName)
		if err != nil {
			return err
		}
		if err := g.updateRef(ctx, commitSha); err != nil {
			lastErr = err // ref moved or transient — rebuild against new HEAD
			continue
		}
		return nil
	}
	return lastErr
}

// gitDataDelete removes all parts + manifest for remoteName in one commit (tree
// entries with sha=null delete paths). Falls back to delRaw if there's no
// manifest (legacy single-file object).
func (g *Github) gitDataDelete(ctx context.Context, remoteName string) error {
	mb, ok := readManifest(ctx, g, remoteName)
	if !ok {
		return g.delRaw(ctx, remoteName) // no manifest → single file
	}
	var m shardManifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return g.delRaw(ctx, remoteName)
	}
	entries := make([]treeEntry, 0, m.Shards+1)
	for i := 0; i < m.Shards; i++ {
		entries = append(entries, treeEntry{Path: partName(remoteName, i), Mode: "100644", Type: "blob", SHA: nil})
	}
	entries = append(entries, treeEntry{Path: manifestName(remoteName), Mode: "100644", Type: "blob", SHA: nil})

	const tries = 5
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		headCommit, baseTree, err := g.headTree(ctx)
		if err != nil {
			return err
		}
		treeSha, err := g.createTree(ctx, baseTree, entries)
		if err != nil {
			return err
		}
		commitSha, err := g.createCommit(ctx, treeSha, []string{headCommit}, "nodepanel delete: "+remoteName)
		if err != nil {
			return err
		}
		if err := g.updateRef(ctx, commitSha); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}
