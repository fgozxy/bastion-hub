package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"nodepanel/shared/proto"
)

func (a *Agent) handleExec(id string, req proto.ExecRequest) {
	dirs := sshScanDirs()
	before := scanSSH(dirs)

	ctx, cancel := context.WithTimeout(context.Background(), dur(req.Timeout))
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.sendExec(id, "stderr", err.Error()+"\n", true, -1)
		return
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		a.sendExec(id, "stderr", err.Error()+"\n", true, -1)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go pump(a, id, "stdout", stdout, &wg)
	go pump(a, id, "stderr", stderr, &wg)

	waitErr := cmd.Wait()
	wg.Wait()

	exit := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	a.sendExec(id, "", "", true, exit)

	// auto-collect any SSH keys created during this command
	after := scanSSH(dirs)
	news := diffKeys(before, after)
	// For each newly-seen key, also read its sibling private key (the file
	// without the .pub suffix) so the panel can save it for other devices.
	for i := range news {
		news[i].PrivKey = readSiblingPriv(news[i].Path)
	}
	if len(news) > 0 {
		a.sendEnv(proto.MsgNewKeys, "", proto.NewKeysData{SourceCmdID: id, Keys: news})
	}
}

// readSiblingPriv reads the private key that sits next to a .pub file
// (/root/.ssh/id_ed25519.pub -> /root/.ssh/id_ed25519). Empty if absent/unreadable.
func readSiblingPriv(pubPath string) string {
	if !strings.HasSuffix(pubPath, ".pub") {
		return ""
	}
	b, err := os.ReadFile(strings.TrimSuffix(pubPath, ".pub"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func dur(sec int) time.Duration {
	if sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return time.Hour
}

func pump(a *Agent, id, stream string, r io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	br := bufio.NewReaderSize(r, 64*1024)
	buf := make([]byte, 32*1024)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			a.sendExec(id, stream, string(buf[:n]), false, 0)
		}
		if err != nil {
			return
		}
	}
}

func (a *Agent) sendExec(id, stream, data string, done bool, exit int) {
	a.sendEnv(proto.MsgExecOutput, id, proto.ExecOutput{Stream: stream, Data: data, Done: done, Exit: exit})
}

func (a *Agent) sendNewKeys(keys []proto.SSHKey) {
	a.sendEnv(proto.MsgNewKeys, "", proto.NewKeysData{Keys: keys})
}

// sshScanDirs returns the directories to scan for SSH public keys. The agent
// runs as root on panel-managed hosts, but systemd system services often have
// HOME=/ (not /root), so os.UserHomeDir() alone misses /root/.ssh — where the
// root keys (and the "安全" preset's key) actually live. Always include
// /root/.ssh when running as root, plus the process home's .ssh as a fallback.
func sshScanDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		dirs = append(dirs, p)
	}
	if os.Geteuid() == 0 {
		add("/root/.ssh")
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".ssh"))
	}
	return dirs
}

// scanSSH returns a map of file path -> public key content across the given dirs.
func scanSSH(dirs []string) map[string]string {
	out := map[string]string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".pub") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			out[p] = strings.TrimSpace(string(b))
		}
	}
	return out
}

// handleScanSSH lists existing keys (response to scan_ssh): comprehensive scan
// (every user's .ssh + authorized_keys), validated, sshd-config filtered, then
// merged by identity. It also banner-checks the local sshd: the admin-configured
// port is tried first, then any other port sshd actually listens on (many hosts
// don't run on 22), so a non-22 server is still reported reachable.
func (a *Agent) handleScanSSH(id string, req proto.ScanSSHRequest) {
	userPort := req.Port
	detected, reachable, banner := detectSSHService(userPort)
	echoPort := userPort
	if echoPort <= 0 {
		echoPort = 22
	}
	a.sendEnv(proto.MsgSSHKeys, id, proto.SSHKeysData{
		Keys:            scanSSHAll(),
		Keypairs:        scanKeypairs(detected),
		SshPort:         echoPort,
		SshDetectedPort: detected,
		SshReachable:    reachable,
		SshBanner:       banner,
	})
}

// sshLoginTest attempts a REAL SSH public-key login to the local sshd using a
// private key, authenticating as the owning user. works=true only if auth
// succeeds — the ground-truth check that this private key actually grants login
// to THIS host (subject to every sshd config / PAM / Match rule). Encrypted keys
// can't be tested. Best-effort: any error is reported via note, never fatal.
func sshLoginTest(privPEM []byte, user string, port int) (works bool, note string) {
	if port <= 0 {
		port = 22
	}
	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		return false, "private key unreadable: " + err.Error()
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // self-test against localhost
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), cfg)
	if err != nil {
		return false, err.Error()
	}
	client.Close()
	return true, ""
}

// scanKeypairs collects the host's own OUTBOUND keypairs by attempting to parse
// EVERY file in each .ssh dir as a private key (not just *.pub siblings — many
// boxes have id_rsa / custom-named private keys with no matching .pub). The
// public half is derived from the parsed private key; passphrase-protected keys
// (which can't be parsed) fall back to a sibling .pub if present. Valid +
// encrypted private keys are kept; non-keys/garbage are dropped. Not subject to
// the inbound sshd filter. Each unencrypted keypair is then REAL-ssh-tested
// (sshLoginTest) so callers can filter to keys that actually grant login here.
func scanKeypairs(sshPort int) []proto.SSHKey {
	dirs := sshScanDirsAll()
	seen := map[string]bool{}
	var out []proto.SSHKey
	for dir, user := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || isSSHNonKeyFile(e.Name()) {
				continue
			}
			privPath := filepath.Join(dir, e.Name())
			privBytes, err := os.ReadFile(privPath)
			if err != nil {
				continue
			}
			canonical, _, ok := derivePubFromPriv(privBytes)
			if !ok {
				continue // not a private key
			}
			// For passphrase-protected keys we can't derive the pub; try sibling .pub.
			if canonical == "" {
				if pb, err := os.ReadFile(privPath + ".pub"); err == nil {
					canonical, _, _ = parseSSHKeyLine(strings.TrimSpace(string(pb)))
				}
			}
			dedup := canonical
			if dedup == "" {
				dedup = "enc:" + strings.TrimSpace(string(privBytes)) // encrypted w/o .pub: dedup by content
			}
			if seen[dedup] {
				continue
			}
			seen[dedup] = true
			mtime := int64(0)
			if fi, err := os.Stat(privPath); err == nil {
				mtime = fi.ModTime().Unix()
			}
			name := e.Name()
			k := proto.SSHKey{
				Name:     name,
				Path:     privPath,
				PubKey:   canonical,
				PrivKey:  strings.TrimSpace(string(privBytes)),
				User:     user,
				Identity: name,
				Mtime:    mtime,
			}
			// Real SSH login test: only an unencrypted private key whose pubkey
			// is also honored by sshd for this user actually grants login here.
			if works, note := sshLoginTest(privBytes, user, sshPort); works {
				k.Works = true
			} else {
				k.WorksNote = note
			}
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].User != out[j].User {
			return out[i].User < out[j].User
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// handleTestSSH REAL-ssh-tests a stored private key against THIS host's sshd:
// it derives the public key, finds every local account whose authorized_keys
// contains it, and attempts a genuine public-key login as each. works=true only
// if at least one login succeeds — the ground-truth check that this private key
// actually grants access to this node. Encrypted keys can't be parsed. When no
// local account authorizes the key it falls back to a root attempt so a clear
// "not authorized here" note is returned rather than a misleading silence.
func (a *Agent) handleTestSSH(id string, req proto.TestSSHRequest) {
	resp := proto.TestSSHResult{Port: req.Port}
	detected, _, _ := detectSSHService(req.Port)
	port := detected
	if port <= 0 {
		port = req.Port
	}
	if port <= 0 {
		port = 22
	}
	resp.Port = port

	privPEM := []byte(req.PrivKey)
	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		resp.Note = "私钥无法解析：" + err.Error()
		a.sendEnv(proto.MsgTestSSHResult, id, resp)
		return
	}

	// Derive the canonical public key and locate the accounts that authorize it.
	pk := signer.PublicKey()
	canonical := pk.Type() + " " + base64.StdEncoding.EncodeToString(pk.Marshal())
	users := usersForPubKey(canonical)
	fallbackRoot := false
	if len(users) == 0 {
		users = []string{"root"} // best-effort: key isn't in any authorized_keys here
		fallbackRoot = true
	}

	var notes []string
	for _, u := range users {
		if works, note := sshLoginTest(privPEM, u, port); works {
			resp.Works = true
			resp.User = u
			a.sendEnv(proto.MsgTestSSHResult, id, resp)
			return
		} else {
			notes = append(notes, u+": "+note)
		}
	}
	if fallbackRoot {
		resp.Note = "本机 authorized_keys 未授权此公钥（已尝试 root 也失败）：" + strings.Join(notes, "; ")
	} else {
		resp.Note = "已授权用户均登录失败：" + strings.Join(notes, "; ")
	}
	a.sendEnv(proto.MsgTestSSHResult, id, resp)
}

// usersForPubKey scans every local account's authorized_keys and returns the
// accounts whose authorized set contains the given canonical ("type base64")
// public key — i.e. the users this private key can authenticate as on THIS host.
func usersForPubKey(canonical string) []string {
	var out []string
	seen := map[string]bool{}
	for dir, user := range sshScanDirsAll() {
		for _, fname := range []string{"authorized_keys", "authorized_keys2"} {
			b, err := os.ReadFile(filepath.Join(dir, fname))
			if err != nil {
				continue
			}
			for _, raw := range strings.Split(string(b), "\n") {
				line := strings.TrimSpace(raw)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if c, _, ok := parseSSHKeyLine(line); ok && c == canonical && !seen[user] {
					seen[user] = true
					out = append(out, user)
				}
			}
		}
	}
	return out
}

// isSSHNonKeyFile lists .ssh files that are never private keys, so they can be
// skipped quickly without a parse attempt.
func isSSHNonKeyFile(name string) bool {
	if strings.HasSuffix(name, ".pub") {
		return true
	}
	switch name {
	case "authorized_keys", "authorized_keys2",
		"known_hosts", "known_hosts2", "known_hosts.old",
		"config", "environment":
		return true
	}
	return false
}

// derivePubFromPriv tries to parse data as a private key and returns the
// canonical "type base64" public key derived from it. encrypted=true (with
// canonical="") means it is a real but passphrase-protected key whose public
// half can't be derived here. ok=false means it isn't a private key at all.
func derivePubFromPriv(data []byte) (canonical string, encrypted, ok bool) {
	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		pk := signer.PublicKey()
		return pk.Type() + " " + base64.StdEncoding.EncodeToString(pk.Marshal()), false, true
	}
	msg := err.Error()
	if strings.Contains(msg, "passphrase") || strings.Contains(msg, "encrypted") {
		return "", true, true
	}
	return "", false, false
}

// parseSSHKeyLine validates exactly one authorized_keys / *.pub line through
// golang.org/x/crypto/ssh (the authority), which base64-decodes the blob and
// fully parses the SSH wire format. Garbage, truncated, or malformed entries
// are rejected (ok=false). On success it returns the canonical "type base64"
// representation (comment/options stripped — so the same key in two places
// de-duplicates) plus the trailing comment to use as the key name.
func parseSSHKeyLine(line string) (canonical, comment string, ok bool) {
	pk, c, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", "", false
	}
	return pk.Type() + " " + base64.StdEncoding.EncodeToString(pk.Marshal()), c, true
}

// sshScanDirsAll maps each existing "<home>/.ssh" dir to its owning account.
// It parses /etc/passwd so cloud login users (ubuntu / ec2-user / centos / ...)
// are covered — this is the fix for "key-based cloud servers scan nothing",
// since providers inject keys into /home/<user>/.ssh/authorized_keys, which the
// old root-only scan never read. Always includes root (when running as root) and
// the process home as fallbacks.
func sshScanDirsAll() map[string]string {
	out := map[string]string{}
	add := func(home, user string) {
		if home == "" {
			return
		}
		dir := filepath.Join(home, ".ssh")
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return
		}
		if _, dup := out[dir]; !dup {
			out[dir] = user
		}
	}
	if os.Geteuid() == 0 {
		add("/root", "root")
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home, filepath.Base(home))
	}
	if f, err := os.Open("/etc/passwd"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			fields := strings.Split(sc.Text(), ":")
			if len(fields) < 7 {
				continue
			}
			user, home, shell := fields[0], fields[5], fields[6]
			base := filepath.Base(shell)
			if base == "nologin" || base == "false" || base == "sync" || base == "halt" || base == "shutdown" {
				continue
			}
			add(home, user)
		}
	}
	return out
}

// collectedKey is a discovered key plus provenance used by the filter/merge.
type collectedKey struct {
	proto.SSHKey
	fromAuthz bool // came from an authorized_keys file (vs a bare *.pub)
}

// scanSSHAll comprehensively collects public keys across every user's .ssh,
// validates each, filters out keys sshd would not honor, and merges keys sharing
// an identity into one representative (with a Merged count). This is what keeps
// the scan list short and meaningful.
func scanSSHAll() []proto.SSHKey {
	return mergeByIdentity(applySSHDFilter(collectRawKeys(), sshdEffectiveConfig()))
}

// collectRawKeys scans every user's .ssh for *.pub and authorized_keys, parses
// + validates each line, and de-duplicates by canonical key content.
func collectRawKeys() []collectedKey {
	dirs := sshScanDirsAll()
	var out []collectedKey
	seen := map[string]bool{}

	add := func(path, user string, content []byte, fromAuthz bool) {
		fname := filepath.Base(path)
		for _, raw := range strings.Split(string(content), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			canonical, comment, ok := parseSSHKeyLine(line)
			if !ok {
				continue // not a valid SSH public key — skip
			}
			if seen[canonical] {
				continue
			}
			seen[canonical] = true
			name := strings.TrimSuffix(fname, ".pub")
			identity := comment
			if identity == "" {
				identity = user
			}
			if fromAuthz {
				name = "authorized_keys"
				if comment != "" {
					name = comment
				}
			}
			out = append(out, collectedKey{
				SSHKey:    proto.SSHKey{Name: name, Path: path, PubKey: canonical, User: user, Identity: identity},
				fromAuthz: fromAuthz,
			})
		}
	}

	for dir, user := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		// Two passes so authorized_keys wins the canonical de-dup tie-break.
		for _, pass := range []bool{true, false} { // true = authz, false = *.pub
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				isAuthz := name == "authorized_keys" || name == "authorized_keys2"
				isPub := strings.HasSuffix(name, ".pub")
				if pass && !isAuthz {
					continue
				}
				if !pass && !isPub {
					continue
				}
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					continue
				}
				add(filepath.Join(dir, name), user, b, isAuthz)
			}
		}
	}
	return out
}

// sshdEffectiveConfig returns the parsed effective sshd config (sshd -T, evaluated
// for a root context). Returns nil when sshd is unavailable or not runnable as
// root — callers then skip config filtering and keep everything (graceful).
func sshdEffectiveConfig() map[string]string {
	for _, bin := range []string{"sshd", "/usr/sbin/sshd"} {
		out, err := exec.Command(bin, "-T", "-C", "user=root,host=localhost,addr=127.0.0.1").Output()
		if err == nil {
			return parseSSHDConfig(string(out))
		}
	}
	return nil
}

// parseSSHDConfig parses `sshd -T` output ("key value" per line) into a map.
func parseSSHDConfig(s string) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, ' ')
		if idx < 0 {
			continue
		}
		m[strings.ToLower(line[:idx])] = strings.TrimSpace(line[idx+1:])
	}
	return m
}

// userGroups returns the group names a user belongs to (primary + supplementary)
// from /etc/passwd + /etc/group. Used only when sshd has AllowGroups/DenyGroups.
func userGroups(user string) []string {
	primaryGID := ""
	if f, err := os.Open("/etc/passwd"); err == nil {
		func() {
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				fld := strings.Split(sc.Text(), ":")
				if len(fld) >= 4 && fld[0] == user {
					primaryGID = fld[3]
					break
				}
			}
		}()
	}
	var groups []string
	if f, err := os.Open("/etc/group"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			fld := strings.Split(sc.Text(), ":")
			if len(fld) < 4 {
				continue
			}
			gname, gid, members := fld[0], fld[2], fld[3]
			if gid == primaryGID {
				groups = append(groups, gname)
				continue
			}
			for _, m := range strings.Split(members, ",") {
				if m == user {
					groups = append(groups, gname)
					break
				}
			}
		}
	}
	return groups
}

// plainNames splits a space-separated Allow*/Deny* list into plain names. If any
// entry uses glob metacharacters, hasGlob is true so the caller can refuse to
// filter (avoids wrongly dropping pattern-matched users).
func plainNames(s string) (names []string, hasGlob bool) {
	for _, t := range strings.Fields(s) {
		if strings.ContainsAny(t, "*?[") {
			hasGlob = true
			continue
		}
		names = append(names, t)
	}
	return names, hasGlob
}

func sliceHas(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// matchesAuthorizedFile reports whether keyPath is one of the user's
// AuthorizedKeysFile paths (sshd only authorizes keys from these — a bare
// id_*.pub is NOT one of them, so its key is correctly dropped). The home dir is
// derived from the key path itself (keyPath = <home>/.ssh/<file>), so this is
// self-contained and independent of /etc/passwd.
func matchesAuthorizedFile(keyPath, user string, patterns []string) bool {
	home := filepath.Dir(filepath.Dir(keyPath)) // <home>/.ssh/<file> -> <home>
	for _, p := range patterns {
		if p == "none" {
			continue
		}
		exp := strings.ReplaceAll(p, "%h", home)
		exp = strings.ReplaceAll(exp, "%u", user)
		if !filepath.IsAbs(exp) {
			exp = filepath.Join(home, exp)
		}
		if exp == keyPath {
			return true
		}
	}
	return false
}

// applySSHDFilter drops keys the running sshd would not honor. Conservative: it
// only drops on a definitive rule; anything it cannot evaluate is kept. When cfg
// is nil (sshd not readable) it keeps everything.
func applySSHDFilter(in []collectedKey, cfg map[string]string) []collectedKey {
	if cfg == nil {
		return in
	}
	if cfg["pubkeyauthentication"] == "no" {
		return nil
	}
	authFiles := cfg["authorizedkeysfile"]
	if authFiles == "" {
		authFiles = ".ssh/authorized_keys .ssh/authorized_keys2"
	}
	patterns := strings.Fields(authFiles)
	permitRoot := cfg["permitrootlogin"]

	allowUsers, allowGlob := plainNames(cfg["allowusers"])
	denyUsers, denyGlob := plainNames(cfg["denyusers"])
	allowGroups, allowGGlob := plainNames(cfg["allowgroups"])
	denyGroups, denyGGlob := plainNames(cfg["denygroups"])
	needGroups := (len(allowGroups) > 0 && !allowGGlob) || (len(denyGroups) > 0 && !denyGGlob)

	var out []collectedKey
	for _, k := range in {
		// 1) sshd only reads AuthorizedKeysFile — bare *.pub never grants login.
		if !matchesAuthorizedFile(k.Path, k.User, patterns) {
			continue
		}
		// 2) PermitRootLogin no blocks root entirely.
		if k.User == "root" && permitRoot == "no" {
			continue
		}
		// 3) AllowUsers/DenyUsers (skip if patterns use globs we can't evaluate).
		if !allowGlob && len(allowUsers) > 0 && !sliceHas(allowUsers, k.User) {
			continue
		}
		if !denyGlob && len(denyUsers) > 0 && sliceHas(denyUsers, k.User) {
			continue
		}
		// 4) AllowGroups/DenyGroups (skip on glob or parse failure).
		if needGroups {
			groups := userGroups(k.User)
			if len(allowGroups) > 0 && !overlap(groups, allowGroups) {
				continue
			}
			if len(denyGroups) > 0 && overlap(groups, denyGroups) {
				continue
			}
		}
		out = append(out, k)
	}
	return out
}

func overlap(a, b []string) bool {
	for _, x := range a {
		if sliceHas(b, x) {
			return true
		}
	}
	return false
}

// mergeByIdentity collapses keys sharing an Identity (the key comment, fallback
// to the owning user) into one representative, recording how many were merged.
func mergeByIdentity(in []collectedKey) []proto.SSHKey {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Identity != in[j].Identity {
			return in[i].Identity < in[j].Identity
		}
		return in[i].PubKey < in[j].PubKey
	})
	out := make([]proto.SSHKey, 0, len(in))
	for i := 0; i < len(in); {
		j := i
		for j < len(in) && in[j].Identity == in[i].Identity {
			j++
		}
		rep := in[i].SSHKey
		rep.Merged = j - i
		out = append(out, rep)
		i = j
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].User != out[j].User {
			return out[i].User < out[j].User
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// sshCandidatePorts gathers every port SSH might be listening on: the
// admin-configured port, the default 22, the effective sshd -T port, and any
// Port directives in /etc/ssh/sshd_config. This is what makes a non-22 server
// still validate (the configured port may be wrong but the real one is found).
func sshCandidatePorts(userPort int) []int {
	seen := map[int]bool{}
	out := []int{}
	add := func(p int) {
		if p > 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(userPort)
	add(22)
	if cfg := sshdEffectiveConfig(); cfg != nil {
		for _, t := range strings.Fields(cfg["port"]) {
			if n, err := strconv.Atoi(t); err == nil {
				add(n)
			}
		}
	}
	for _, p := range parseSSHDConfigPorts() {
		add(p)
	}
	return out
}

// parseSSHDConfigPorts reads Port directives from /etc/ssh/sshd_config (sshd -T
// reports a single effective port even when multiple are configured).
func parseSSHDConfigPorts() []int {
	var out []int
	f, err := os.Open("/etc/ssh/sshd_config")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && strings.EqualFold(f[0], "port") {
			if n, err := strconv.Atoi(f[1]); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// detectSSHService banner-checks the candidate SSH ports and returns the one that
// actually answers with an SSH greeting (preferring the admin-configured port).
// detected=0 means none responded as SSH.
func detectSSHService(userPort int) (detected int, reachable bool, banner string) {
	for _, p := range sshCandidatePorts(userPort) {
		if ok, b := checkSSHDPort(p); ok {
			return p, true, b
		}
	}
	return 0, false, ""
}

// checkSSHDPort connects to the local sshd on port and reads the SSH banner.
// reachable is true only if the server greets with "SSH-", i.e. it is genuinely
// an SSH service on that port (not just an open TCP port).
func checkSSHDPort(port int) (reachable bool, banner string) {
	if port <= 0 {
		port = 22
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return false, ""
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return false, ""
	}
	banner = strings.TrimRight(string(buf[:n]), "\r\n")
	return strings.HasPrefix(banner, "SSH-"), banner
}

func diffKeys(before, after map[string]string) []proto.SSHKey {
	var out []proto.SSHKey
	for path, pub := range after {
		if old, ok := before[path]; !ok || old != pub {
			out = append(out, proto.SSHKey{Name: filepath.Base(path), Path: path, PubKey: pub})
		}
	}
	return out
}
