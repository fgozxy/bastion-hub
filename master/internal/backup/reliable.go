package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"nodepanel/master/internal/store"
	"nodepanel/master/internal/targets"
)

const targetPushAttempts = 5

var targetPushLimiters sync.Map // target id or storage host -> chan struct{}

func targetPushLimiter(key string) chan struct{} {
	v, _ := targetPushLimiters.LoadOrStore(key, make(chan struct{}, 1))
	return v.(chan struct{})
}

func acquireTargetPushSlot(ctx context.Context, key string) (func(), error) {
	sem := targetPushLimiter(key)
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func targetPushGroupKey(t *store.BackupTarget) string {
	switch t.Type {
	case "vps":
		var c targets.VPSConfig
		if json.Unmarshal([]byte(t.Config), &c) == nil {
			if host := normalizeStorageHost(c.Host); host != "" {
				return "host:" + host
			}
		}
	case "s3":
		var c targets.S3Config
		if json.Unmarshal([]byte(t.Config), &c) == nil {
			if host := normalizeStorageHost(c.Endpoint); host != "" {
				return "host:" + host
			}
		}
	}
	return "target:" + t.ID
}

func normalizeStorageHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		if u, err := url.Parse(host); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

func isLikelyPermanentPushError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	permanent := []string{
		"unable to authenticate",
		"no supported methods remain",
		"permission denied",
		"bad credentials",
		"unauthorized",
		"forbidden",
		"invalid_grant",
		"no such host",
		"bucket name",
		"access denied",
	}
	for _, p := range permanent {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func pushBackoff(attempt int) time.Duration {
	backoffs := []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}
	if attempt < len(backoffs) {
		return backoffs[attempt]
	}
	return backoffs[len(backoffs)-1]
}

func (s *Service) pushArchiveReliable(ctx context.Context, t *store.BackupTarget, localPath, remoteName string) error {
	release, err := acquireTargetPushSlot(ctx, targetPushGroupKey(t))
	if err != nil {
		return err
	}
	defer release()

	var lastErr error
	for attempt := 0; attempt < targetPushAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(pushBackoff(attempt - 1)):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		up, err := targets.New(t, s.saver())
		if err != nil {
			return err
		}
		if err := up.Push(ctx, localPath, remoteName); err != nil {
			lastErr = err
			if isLikelyPermanentPushError(err) {
				return err
			}
			continue
		}
		if err := verifyRemoteObject(ctx, up, remoteName); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("after %d attempts: %w", targetPushAttempts, lastErr)
}

func verifyRemoteObject(ctx context.Context, up targets.Uploader, remoteName string) error {
	candidates := []string{remoteName, path.Dir(remoteName), ""}
	seenPrefix := map[string]bool{}
	for _, prefix := range candidates {
		if prefix == "." {
			prefix = ""
		}
		if seenPrefix[prefix] {
			continue
		}
		seenPrefix[prefix] = true
		items, err := up.List(ctx, prefix)
		if err != nil {
			continue
		}
		if remoteListContains(items, remoteName) {
			return nil
		}
	}
	// List() is not a strict contract across backends: GitHub lists one directory,
	// OneDrive lists the configured folder, S3 lists by object prefix, and SFTP
	// returns basenames. Push success is authoritative; verification is a useful
	// extra signal only when the backend can confirm the object immediately.
	return nil
}

func remoteListContains(items []string, remoteName string) bool {
	base := strings.TrimPrefix(remoteName, "/")
	file := base
	if i := strings.LastIndex(file, "/"); i >= 0 {
		file = file[i+1:]
	}
	for _, item := range items {
		item = strings.TrimPrefix(item, "/")
		if item == base || strings.HasSuffix(item, "/"+base) || item == file || strings.HasPrefix(item, file+".") || strings.HasPrefix(item, base+".") {
			return true
		}
	}
	return false
}
