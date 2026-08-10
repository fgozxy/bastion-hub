package targets

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// VPSConfig stores SSH/SFTP connection details.
type VPSConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	KeyPEM   string `json:"key_pem"` // optional private key
	BaseDir  string `json:"base_dir"`
}

type VPS struct{ cfg VPSConfig }

func (v *VPS) port() int {
	if v.cfg.Port == 0 {
		return 22
	}
	return v.cfg.Port
}

func (v *VPS) connect() (*sftp.Client, func(), error) {
	auths := []ssh.AuthMethod{}
	if v.cfg.KeyPEM != "" {
		signer, err := ssh.ParsePrivateKey([]byte(v.cfg.KeyPEM))
		if err == nil {
			auths = append(auths, ssh.PublicKeys(signer))
		}
	}
	if v.cfg.Password != "" {
		auths = append(auths, ssh.Password(v.cfg.Password))
	}
	cfg := &ssh.ClientConfig{
		User:            v.cfg.User,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", v.cfg.Host, v.port()), 15*time.Second)
	if err != nil {
		return nil, nil, err
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, fmt.Sprintf("%s:%d", v.cfg.Host, v.cfg.Port), cfg)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	cli := ssh.NewClient(c, ch, reqs)
	sc, err := sftp.NewClient(cli)
	if err != nil {
		_ = cli.Close()
		return nil, nil, err
	}
	return sc, func() { _ = sc.Close(); _ = cli.Close() }, nil
}

func (v *VPS) remote(remoteName string) string {
	base := strings.TrimRight(v.cfg.BaseDir, "/")
	if base == "" {
		base = "/srv/nodepanel-backups"
	}
	return base + "/" + remoteName
}

// withConnRetry runs fn on a fresh SFTP connection, retrying transient SSH/SFTP
// failures with exponential backoff. The VPS target is hit concurrently by many
// backups at once — a schedule fans out across nodes/containers, each unit opens
// its own SSH connection to the same host, and sshd's MaxStartups (default
// 10:30:100) then drops some in-flight handshakes with a TCP RST. That surfaces
// as "ssh: handshake failed: ... connection reset by peer" and, without retry,
// fails the whole push for that container. By the next attempt (2s later) the
// colliding handshakes have authenticated and left the startup window, so the
// retry connects cleanly. A fresh connection is opened each attempt so Push stays
// idempotent (the remote file is truncated by Create).
func (v *VPS) withConnRetry(ctx context.Context, fn func(*sftp.Client) error) error {
	const maxRetries = 4
	backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	var last error
	for attempt := 0; ; attempt++ {
		sc, closeConn, err := v.connect()
		if err == nil {
			err = fn(sc)
			closeConn()
			if err == nil {
				return nil
			}
		}
		last = err
		if !isTransientSSH(err) || attempt >= maxRetries {
			return last
		}
		select {
		case <-time.After(backoff[attempt]):
		case <-ctx.Done():
			return last
		}
	}
}

// isTransientSSH reports whether an SSH/SFTP error is the kind a quick retry
// fixes — momentary network blips and sshd dropping concurrent handshakes.
// Auth/permission failures are permanent and excluded so we don't burn retries
// on a misconfigured key.
func isTransientSSH(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	if strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "no supported methods remain") ||
		strings.Contains(s, "permission denied") {
		return false
	}
	return strings.Contains(s, "reset by peer") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "timed out") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection closed") ||
		strings.Contains(s, "no route to host") ||
		strings.Contains(s, "network is unreachable") ||
		strings.Contains(s, "handshake failed") ||
		strings.Contains(s, "dial tcp") ||
		strings.Contains(s, "i/o timeout")
}

func (v *VPS) Push(ctx context.Context, localPath, remoteName string) error {
	return v.withConnRetry(ctx, func(sc *sftp.Client) error {
		dst := v.remote(remoteName)
		if err := sc.MkdirAll(path.Dir(dst)); err != nil {
			return err
		}
		in, err := os.Open(localPath)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := sc.Create(dst)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func (v *VPS) Pull(ctx context.Context, remoteName, localPath string) error {
	return v.withConnRetry(ctx, func(sc *sftp.Client) error {
		in, err := sc.Open(v.remote(remoteName))
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(localPath)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func (v *VPS) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	err := v.withConnRetry(ctx, func(sc *sftp.Client) error {
		dir := path.Dir(v.remote(prefix))
		walker := sc.Walk(dir)
		list := []string{}
		for walker.Step() {
			if err := walker.Err(); err != nil {
				return err
			}
			if !walker.Stat().IsDir() {
				list = append(list, path.Base(walker.Path()))
			}
		}
		out = list
		return nil
	})
	return out, err
}

func (v *VPS) Delete(ctx context.Context, remoteName string) error {
	return v.withConnRetry(ctx, func(sc *sftp.Client) error {
		return sc.Remove(v.remote(remoteName))
	})
}

func (v *VPS) Test(ctx context.Context) error {
	return v.withConnRetry(ctx, func(sc *sftp.Client) error {
		_, err := sc.Getwd()
		return err
	})
}

// DirEntry is one entry in a remote directory listing (for the path browser).
type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// FSDisk describes a mounted filesystem (so the user can tell the 0.5TB data
// disk apart from the 20GB system disk).
type FSDisk struct {
	Path   string `json:"path"`    // mount point
	Device string `json:"device"`  // /dev/vdb1 etc.
	FSType string `json:"fs_type"` // ext4/xfs/...
	Total  uint64 `json:"total"`   // bytes
	Used   uint64 `json:"used"`    // bytes
	Free   uint64 `json:"free"`    // bytes
}

// VPSBrowseResult is the payload returned to the directory browser.
type VPSBrowseResult struct {
	Path    string     `json:"path"`
	Entries []DirEntry `json:"entries"`
	Current *FSDisk    `json:"current,omitempty"` // filesystem of the listed dir
	Mounts  []FSDisk   `json:"mounts"`            // real mounted disks (big first)
}

// BrowseVPS lists entries at dir plus the host's mounted disks. For the initial
// browse ("/", "~", or empty) it tries the likely homes (Getwd, /root,
// /home/<user>, /) and returns the first non-empty one. Mounts are read from
// /proc/mounts and sized via StatVFS so the user can pick the big data disk.
func BrowseVPS(cfg VPSConfig, dir string) (*VPSBrowseResult, error) {
	v := &VPS{cfg: cfg}
	sc, closeConn, err := v.connect()
	if err != nil {
		return nil, err
	}
	defer closeConn()

	list := func(d string) ([]DirEntry, error) {
		infos, err := sc.ReadDir(d)
		if err != nil {
			return nil, err
		}
		var out []DirEntry
		for _, info := range infos {
			name := info.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			out = append(out, DirEntry{Name: name, Path: path.Join(d, name), IsDir: info.IsDir()})
		}
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				a, b := out[i], out[j]
				if (!a.IsDir && b.IsDir) || (a.IsDir == b.IsDir && a.Name > b.Name) {
					out[i], out[j] = out[j], out[i]
				}
			}
		}
		return out, nil
	}

	resolved := dir
	var entries []DirEntry
	if dir == "" || dir == "~" || dir == "/" {
		wd, _ := sc.Getwd()
		order := uniqueStrs([]string{wd, "/root", "/home/" + cfg.User, "/"})
		for _, c := range order {
			out, e := list(c)
			resolved, entries = c, out
			if e == nil && len(out) > 0 {
				break
			}
		}
	} else {
		entries, err = list(dir)
		if err != nil {
			return nil, err
		}
		resolved = dir
	}

	res := &VPSBrowseResult{Path: resolved, Entries: entries, Mounts: readMounts(sc)}
	if t, fr, ok := statVFS(sc, resolved); ok {
		res.Current = &FSDisk{Path: resolved, Total: t, Free: fr, Used: t - fr}
	}
	return res, nil
}

func statVFS(sc *sftp.Client, p string) (total, free uint64, ok bool) {
	defer func() { _ = recover() }()
	st, err := sc.StatVFS(p)
	if err != nil || st == nil {
		return 0, 0, false
	}
	return st.TotalSpace(), st.FreeSpace(), true
}

// readMounts parses /proc/mounts over SFTP and sizes each real block-device
// mount via StatVFS. Big disks first so the 0.5TB data disk shows on top.
func readMounts(sc *sftp.Client) []FSDisk {
	f, err := sc.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	b, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []FSDisk
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		dev, mp, fst := fields[0], fields[1], fields[2]
		if !strings.HasPrefix(dev, "/dev/") {
			continue // skip pseudo filesystems
		}
		mp = strings.ReplaceAll(mp, "\\040", " ")
		if seen[mp] {
			continue
		}
		seen[mp] = true
		d := FSDisk{Path: mp, Device: dev, FSType: fst}
		if t, fr, ok := statVFS(sc, mp); ok {
			d.Total, d.Free, d.Used = t, fr, t-fr
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

func uniqueStrs(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
