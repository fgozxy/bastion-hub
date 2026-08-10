package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"nodepanel/shared/proto"
)

func (a *Agent) handleBackup(id string, req proto.BackupRequest) {
	// Stream the archive straight to the upload URL through an io.Pipe instead
	// of staging the whole tar.gz in /tmp first. A node with one large volume
	// used to fill its own system disk (ENOSPC) before the upload even began;
	// with a pipe the only on-disk footprint is whatever the OS pipe buffers.
	pr, pw := io.Pipe()
	cw := &pipeCountWriter{w: pw} // bytes pushed into the pipe = archive size

	type archiveResult struct {
		meta []byte
		size int64
		err  error
	}
	archCh := make(chan archiveResult, 1)

	// Producer: tar+gzip the container volumes (or raw paths) into the pipe.
	go func() {
		gw := gzip.NewWriter(cw)
		tw := tar.NewWriter(gw)
		var meta []byte
		var archErr error

		if req.Container != "" {
			paths, m, err := containerBackupMeta(req.Container)
			meta = m
			switch {
			case err != nil:
				archErr = fmt.Errorf("container: %w", err)
			default:
				// container.json first so restore can map volumes/<i> → source in one pass
				if werr := writeBytes(tw, "container.json", m); werr != nil {
					archErr = werr
					break
				}
				for i, src := range paths {
					if err := addPathPrefix(tw, src, "volumes/"+strconv.Itoa(i), req.Exclude); err != nil {
						archErr = err
						break
					}
				}
			}
		} else {
			for _, p := range req.Paths {
				root := strings.TrimSuffix(p, "/")
				if err := addPathPrefix(tw, root, filepath.Base(root), req.Exclude); err != nil {
					archErr = err
					break
				}
			}
		}

		// Close writers in order; the first real failure is what we report.
		if cerr := tw.Close(); archErr == nil {
			archErr = cerr
		}
		if gerr := gw.Close(); archErr == nil {
			archErr = gerr
		}
		// Closing the pipe signals EOF (success) or the error to the uploader.
		if archErr != nil {
			pw.CloseWithError(archErr)
		} else {
			pw.Close()
		}
		archCh <- archiveResult{meta: meta, size: cw.n, err: archErr}
	}()

	// Consumer: stream the pipe either directly to S3/MinIO, or to the panel's
	// chunk upload endpoint. Direct S3 avoids staging the full archive on master.
	upErr := error(nil)
	if req.S3Upload != nil {
		upErr = uploadS3Stream(*req.S3Upload, pr)
	} else if req.Upload != "" {
		upErr = uploadStream(req.Upload, pr)
	} else {
		upErr = fmt.Errorf("backup upload destination missing")
	}
	if upErr != nil {
		// Upload gave up before the producer finished — close the read end with the
		// error so a still-writing producer unblocks instead of deadlocking.
		pr.CloseWithError(upErr)
	}
	ar := <-archCh

	res := proto.BackupResult{}
	switch {
	case ar.err != nil:
		res.OK, res.Err = false, ar.err.Error()
	case upErr != nil:
		res.OK, res.Err = false, upErr.Error()
	default:
		res.OK, res.Size = true, ar.size
		if len(ar.meta) > 0 {
			res.Manifest = buildManifest(ar.meta, ar.size)
		}
	}
	a.sendEnv(proto.MsgBackupResult, id, res)
}

// buildManifest derives the preflight footprint (image / bound host ports /
// bind-mount source paths) from a container.json snapshot, so preflight can run
// against a target node without reading the archive.
func buildManifest(meta []byte, size int64) json.RawMessage {
	if len(meta) == 0 {
		return nil
	}
	var m struct {
		Image      string          `json:"image"`
		HostConfig json.RawMessage `json:"host_config"`
		Mounts     []struct {
			Source string `json:"source"`
			Type   string `json:"type"`
		} `json:"mounts"`
	}
	if json.Unmarshal(meta, &m) != nil {
		return nil
	}
	mf := proto.BackupManifest{Image: m.Image, Size: size}
	for _, mt := range m.Mounts {
		if strings.TrimSpace(mt.Source) == "" {
			continue
		}
		mf.Binds = append(mf.Binds, proto.PreflightItem{BindPath: mt.Source})
	}
	var hc struct {
		PortBindings map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	}
	if len(m.HostConfig) > 0 {
		_ = json.Unmarshal(m.HostConfig, &hc)
	}
	for key, bindings := range hc.PortBindings {
		p, _ := protoPort(key)
		for _, b := range bindings {
			if b.HostPort == "" || b.HostPort == "0" {
				continue
			}
			mf.Ports = append(mf.Ports, proto.PreflightItem{HostPort: b.HostPort, Proto: p})
		}
	}
	out, err := json.Marshal(mf)
	if err != nil {
		return nil
	}
	return out
}

// containerBackupMeta inspects a container and returns the host paths of its
// persistent mounts (bind sources + named-volume _data dirs) plus a JSON
// descriptor (container.json) carrying the config needed to restore/recreate.
func containerBackupMeta(containerID string) (paths []string, meta []byte, err error) {
	dc := newDocker()
	resp, err := dc.req(http.MethodGet, "/containers/"+containerID+"/json", nil)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, nil, errHTTP(resp.StatusCode, string(b))
	}
	var ins struct {
		Name       string          `json:"Name"`
		Config     json.RawMessage `json:"Config"`
		HostConfig json.RawMessage `json:"HostConfig"`
		Mounts     []struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
		// NetworkSettings.Networks carries each network's Aliases — for a
		// compose app these hold the service names (e.g. "postgres", "redis")
		// that the app uses as hostnames. Captured here so a cross-node restore
		// can recreate the network and re-attach the aliases instead of dumping
		// the container on the default bridge where the names won't resolve.
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases []string `json:"Aliases"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ins); err != nil {
		return nil, nil, err
	}

	type mentry struct {
		Index       int    `json:"index"`
		Source      string `json:"source"`
		Destination string `json:"dest"`
		Type        string `json:"type"`
		Name        string `json:"name,omitempty"`
	}
	var mounts []mentry
	for _, m := range ins.Mounts {
		if m.Type == "tmpfs" || strings.TrimSpace(m.Source) == "" {
			continue // not persistent / no host path
		}
		paths = append(paths, m.Source)
		mounts = append(mounts, mentry{Index: len(paths) - 1, Source: m.Source, Destination: m.Destination, Type: m.Type, Name: m.Name})
	}
	image := ""
	if len(ins.Config) > 0 {
		var c struct {
			Image string `json:"Image"`
		}
		_ = json.Unmarshal(ins.Config, &c)
		image = c.Image
	}
	// Snapshot the user-defined networks + their aliases (compose service names
	// live in Aliases). Built-in networks (bridge/host/none/container:*) are
	// skipped — they're either present on every host or not real networks, and
	// aliases on the default bridge don't participate in DNS service discovery.
	// Only Aliases is kept: the IPAM/endpoint runtime fields (IPAddress, Gateway,
	// MacAddress, NetworkID, EndpointID) are host-specific and meaningless (or
	// harmful) when replayed on another node. Stored as EndpointConfig-shaped JSON
	// so restore can hand it straight to buildCreateBody's NetworkingConfig.
	networks := map[string]json.RawMessage{}
	for name, n := range ins.NetworkSettings.Networks {
		if isBuiltinNetwork(name) {
			continue
		}
		ep, _ := json.Marshal(map[string]any{"Aliases": n.Aliases})
		networks[name] = ep
	}
	descriptor := map[string]any{
		"container":   containerID,
		"name":        strings.TrimPrefix(ins.Name, "/"),
		"image":       image,
		"config":      json.RawMessage(orEmpty(ins.Config)),
		"host_config": json.RawMessage(orEmpty(ins.HostConfig)),
		"mounts":      mounts,
		"created_at":  time.Now().Unix(),
	}
	if len(networks) > 0 {
		descriptor["networks"] = networks
	}
	b, _ := json.Marshal(descriptor)
	return paths, b, nil
}

// isBuiltinNetwork reports whether name is a docker built-in network or a
// "container:<id>" network mode rather than a user-defined network we should
// recreate on the target host.
func isBuiltinNetwork(name string) bool {
	switch name {
	case "bridge", "host", "none", "default":
		return true
	}
	return strings.HasPrefix(name, "container:")
}

// ensureNetworks creates each user-defined network on this host if it doesn't
// already exist. Compose-created networks (e.g. "sub2api_default") don't exist
// on a fresh target node, so without this the container's NetworkMode points at
// a missing network and create/start fails. Idempotent and concurrency-safe: a
// network a parallel sibling restore just created returns 409 "already exists",
// which we treat as success. Returns a note describing any failures so the
// caller can fall back to the default bridge.
func ensureNetworks(dc *dockerClient, networks map[string]json.RawMessage) string {
	if len(networks) == 0 {
		return ""
	}
	var note string
	for name := range networks {
		if isBuiltinNetwork(name) {
			continue
		}
		if resp, err := dc.req(http.MethodGet, "/networks/"+url.PathEscape(name), nil); err == nil {
			code := resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if code == 200 {
				continue // already exists — maybe just created by a sibling restore
			}
		}
		body, _ := json.Marshal(map[string]any{"Name": name, "Driver": "bridge"})
		resp, err := dc.req(http.MethodPost, "/networks/create", strings.NewReader(string(body)))
		if err != nil {
			note += "网络 " + name + " 创建失败（" + err.Error() + "），将回退 bridge；"
			continue
		}
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		// 409 "already exists" is a benign race with a concurrent restore.
		if resp.StatusCode >= 400 && !strings.Contains(strings.ToLower(string(rb)), "already exists") {
			note += "网络 " + name + " 创建失败（HTTP " + strconv.Itoa(resp.StatusCode) + "），将回退 bridge；"
		}
	}
	return note
}

func orEmpty(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}

// excluded reports whether path is, or lies beneath, any of the excludes
// prefixes. Paths are cleaned so trailing slashes / "." don't defeat the match.
func excluded(path string, excludes []string) bool {
	if len(excludes) == 0 {
		return false
	}
	cp := filepath.Clean(path)
	sep := string(os.PathSeparator)
	for _, e := range excludes {
		ce := filepath.Clean(e)
		if ce == "" || ce == "." {
			continue
		}
		if cp == ce || strings.HasPrefix(cp, ce+sep) {
			return true
		}
	}
	return false
}

// addPathPrefix walks root and writes entries under the given archive prefix.
// Entries whose path is excluded (== or beneath an excludes prefix) are skipped,
// so a container can shed circular/bloated dirs — e.g. nodepanel backing up
// /var/lib/nodepanel excludes .../backups (all other archives) and .../agents.
func addPathPrefix(tw *tar.Writer, root, prefix string, excludes []string) error {
	base := filepath.Base(root)
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		// A mount source or any entry beneath it may be missing or become
		// unreadable: bind-mount sources that point at a single file the app
		// creates lazily (common — e.g. plugin config files), live sqlite -wal
		// files, log rotations, plugin temp files, dangling symlinks. Skipping
		// one entry is far better than aborting the whole backup over it, so a
		// walk/lstat error is never propagated — including for the root itself.
		if err != nil {
			return nil
		}
		if excluded(path, excludes) {
			if info.IsDir() {
				return filepath.SkipDir // don't recurse into an excluded subtree
			}
			return nil // skip an excluded file
		}
		// Resolve symlink targets so restore can recreate them faithfully.
		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(path)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil
		}
		rel := strings.TrimPrefix(path, root)
		rel = strings.TrimPrefix(rel, string(os.PathSeparator))
		name := prefix
		if rel == "" {
			name = prefix + "/" + base
		} else {
			name = prefix + "/" + base + "/" + rel
		}
		hdr.Name = strings.TrimSuffix(name, "/")
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			// Became unreadable between walk and open — skip, don't abort.
			return nil
		}
		// A file may shrink mid-copy (the app rewriting it). The header already
		// declared info.Size(), so a short copy would misalign the tar stream
		// ("archive/tar: missed writing N bytes" on the next header). Pad the
		// shortfall with zeros to keep the stream consistent; the next backup
		// captures the stable version.
		n, _ := io.CopyN(tw, f, info.Size())
		f.Close()
		if n < info.Size() {
			io.CopyN(tw, zeroReader{}, info.Size()-n)
		}
		return nil
	})
}

// zeroReader is an infinite source of zero bytes, used to pad a truncated tar
// entry back to its declared size.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func writeBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: time.Now(), Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// chunkSize is safely under Cloudflare's 100 MB request-body cap. The agent
// uploads the archive through the panel's public URL (Cloudflare → reverse
// proxy → master), so a single >100 MB POST is rejected with HTTP 413 before
// it ever reaches the master. Streaming the file in chunks under this size
// sidesteps the limit entirely.
const chunkSize int64 = 90 * 1024 * 1024

// uploadStream posts r to url in chunkSize pieces, streaming each piece straight
// into the request body with no full-chunk buffering. It returns nil once the
// reader hits EOF. The per-chunk "last" flag is advisory: the panel finalizes on
// the BackupResult message, not on last=1.
func uploadStream(url string, r io.Reader) error {
	return uploadStreamChunks(url, r, chunkSize)
}

func uploadS3Stream(cfg proto.S3UploadConfig, r io.Reader) error {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.Object) == "" {
		return fmt.Errorf("s3 upload destination incomplete")
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	secure := cfg.Secure
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "https://"), true
	case strings.HasPrefix(endpoint, "http://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "http://"), false
	}
	opts := &minio.Options{
		Creds:        credentials.NewStatic(cfg.AccessKey, cfg.SecretKey, "", credentials.SignatureDefault),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: minio.BucketLookupAuto,
	}
	if cfg.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	if cfg.InsecureSkipVerify {
		opts.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	cli, err := minio.New(endpoint, opts)
	if err != nil {
		return err
	}
	_, err = cli.PutObject(context.Background(), cfg.Bucket, cfg.Object, r, -1, minio.PutObjectOptions{
		PartSize:    16 << 20,
		ContentType: "application/gzip",
	})
	return err
}

// uploadStreamChunks is uploadStream with an injectable chunk size so the
// boundary logic can be tested with small buffers instead of 90 MB each.
func uploadStreamChunks(url string, r io.Reader, chunk int64) error {
	client := &http.Client{Timeout: 2 * time.Hour}
	for idx := 0; ; idx++ {
		er := &eofReader{r: r}
		if err := postChunk(client, url, idx, false, io.LimitReader(er, chunk)); err != nil {
			return err
		}
		if er.eof {
			return nil
		}
	}
}

// eofReader records whether the underlying reader returned EOF, so uploadStream
// can stop after the chunk in which the stream ends — without knowing the total
// size up front. A producer error (io.Pipe CloseWithError with a non-EOF error)
// is passed through untouched, surfacing as a failed POST.
type eofReader struct {
	r   io.Reader
	eof bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		e.eof = true
	}
	return n, err
}

// pipeCountWriter counts bytes written so handleBackup can report the archive
// size without stat-ing a temp file (there is no temp file anymore).
type pipeCountWriter struct {
	w io.Writer
	n int64
}

func (c *pipeCountWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func postChunk(client *http.Client, baseURL string, idx int, last bool, body io.Reader) error {
	full := baseURL + "&chunk=" + strconv.Itoa(idx) + "&last="
	if last {
		full += "1"
	} else {
		full += "0"
	}
	resp, err := client.Post(full, "application/octet-stream", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return errHTTP(resp.StatusCode, string(b))
	}
	return nil
}

func (a *Agent) handleRestore(id string, req proto.RestoreRequest) {
	resp, err := http.Get(req.Download)
	if err != nil {
		a.sendEnv(proto.MsgRestoreResult, id, proto.RestoreResult{Err: err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		a.sendEnv(proto.MsgRestoreResult, id, proto.RestoreResult{Err: "download failed"})
		return
	}
	// Wrap the response body in a counting reader that streams download/extract
	// progress. The gzip+tar decode runs as bytes arrive, so download and
	// extract are a single streaming pass — reported as the "download" stage,
	// bucketed by bytes against ContentLength.
	body := &countReader{
		r:      resp.Body,
		total:  resp.ContentLength,
		report: func(done, total int64) { a.restoreProgress(id, "download", "下载并解压归档", done, total) },
	}
	gz, err := gzip.NewReader(body)
	if err != nil {
		a.sendEnv(proto.MsgRestoreResult, id, proto.RestoreResult{Err: err.Error()})
		return
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	// container.json (if present) maps volumes/<i> → host source path, and
	// carries the snapshot (name/image/Config/HostConfig) used to recreate the
	// container when req.Recreate is set.
	mountSrc := map[int]string{}
	var snap struct {
		Name       string                     `json:"name"`
		Image      string                     `json:"image"`
		Config     json.RawMessage            `json:"config"`
		HostConfig json.RawMessage            `json:"host_config"`
		Networks   map[string]json.RawMessage `json:"networks"`
	}
	hasSnap := false
	dest := filepath.Clean(req.Dest)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			a.sendEnv(proto.MsgRestoreResult, id, proto.RestoreResult{Err: err.Error()})
			return
		}
		// container descriptor: parse the mount map and keep a copy at the destination.
		if hdr.Name == "container.json" {
			buf, _ := io.ReadAll(tr)
			var meta struct {
				Name       string                     `json:"name"`
				Image      string                     `json:"image"`
				Config     json.RawMessage            `json:"config"`
				HostConfig json.RawMessage            `json:"host_config"`
				Networks   map[string]json.RawMessage `json:"networks"`
				Mounts     []struct {
					Index  int    `json:"index"`
					Source string `json:"source"`
				} `json:"mounts"`
			}
			if json.Unmarshal(buf, &meta) == nil {
				snap.Name, snap.Image, snap.Config, snap.HostConfig, snap.Networks = meta.Name, meta.Image, meta.Config, meta.HostConfig, meta.Networks
				hasSnap = len(buf) > 0
				for _, m := range meta.Mounts {
					if m.Source != "" {
						mountSrc[m.Index] = m.Source
					}
				}
			}
			_ = os.MkdirAll(dest, 0o755)
			if of, err := os.Create(filepath.Join(dest, "container.json")); err == nil {
				_, _ = of.Write(buf)
				of.Close()
			}
			continue
		}
		// container volume data → restore under the original mount source path.
		// archive entry shape: volumes/<i>/<base>/<rel>
		if strings.HasPrefix(hdr.Name, "volumes/") {
			rest := strings.TrimPrefix(hdr.Name, "volumes/")
			slash := strings.IndexByte(rest, '/')
			if slash < 0 {
				continue
			}
			idx, perr := strconv.Atoi(rest[:slash])
			tail := rest[slash+1:] // "<base>/<rel>"
			src := ""
			if perr == nil {
				src = mountSrc[idx]
			}
			if src == "" {
				continue
			}
			// drop the leading "<base>/" so files land directly under src
			if i := strings.IndexByte(tail, '/'); i >= 0 {
				tail = tail[i+1:]
			} else {
				tail = ""
			}
			target := src
			if tail != "" {
				target = filepath.Join(src, tail)
			}
			if err := extractEntry(tr, hdr, target, src); err != nil {
				continue
			}
			continue
		}
		// legacy directory backup → extract under req.Dest
		target := filepath.Join(dest, hdr.Name)
		if err := extractEntry(tr, hdr, target, dest); err != nil {
			continue
		}
	}

	res := proto.RestoreResult{OK: true}
	// Container restore: optionally recreate the container on this host from the
	// archived snapshot, so the restored data is actually consumed. Data-only
	// restores (Recreate=false, or old agents) skip this.
	if req.Recreate && hasSnap && snap.Name != "" {
		a.restoreProgress(id, "recreate", "重建容器 "+snap.Name, 0, 0)
		detail, recreated, ports, rerr := recreateContainer(snap.Name, snap.Image, snap.Config, snap.HostConfig, snap.Networks, req.AutoPull)
		res.Detail = detail
		res.Recreated = recreated
		res.Ports = ports
		if rerr != nil {
			// Recreate was requested and failed (image missing, port taken, …).
			// The volume data was extracted, but the rebuild the user asked for
			// did not happen — report failure so the master maps this to "failed"
			// instead of silently green-checking a container that never came up.
			// (Intentional data-only outcomes — same-name container already
			// present, or no container.json — return err==nil and stay OK.)
			res.Err = rerr.Error()
			res.OK = false
		}
	}
	a.sendEnv(proto.MsgRestoreResult, id, res)
}

// restoreProgress sends one throttled progress event during a restore.
func (a *Agent) restoreProgress(id, stage, label string, done, total int64) {
	p := proto.RestoreProgress{Stage: stage, Label: label, BytesDone: done, BytesTotal: total}
	if total > 0 {
		p.Percent = int(done * 100 / total)
		if p.Percent > 100 {
			p.Percent = 100
		}
	}
	a.sendEnv(proto.MsgRestoreProgress, id, p)
}

// countReader wraps a reader, accumulating bytes read and reporting progress on
// a throttle (and once on EOF). Used to stream download/extract progress.
type countReader struct {
	r      io.Reader
	n      int64
	total  int64
	report func(done, total int64)
	last   time.Time
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.report != nil && (err != nil || time.Since(c.last) > 500*time.Millisecond) {
		c.report(c.n, c.total)
		c.last = time.Now()
	}
	return n, err
}

// handlePreflight answers a MsgRestorePreflight: checks, without touching data,
// whether a set of host ports are free, bind paths are unoccupied, the image is
// present, and there is enough disk. Port conflicts are hard blockers; path
// conflicts are warnings (restore would overwrite). Reused by the master's
// preflight endpoint so users see predicted collisions before committing.
func (a *Agent) handlePreflight(id string, req proto.RestorePreflightRequest) {
	res := proto.PreflightResult{OK: true, AgentSupportsPreflight: true}
	seenPort := map[string]bool{}
	var image string
	var required int64
	for _, it := range req.Items {
		if it.HostPort != "" {
			key := it.Proto + "/" + it.HostPort
			if !seenPort[key] {
				seenPort[key] = true
				if p, perr := strconv.Atoi(it.HostPort); perr == nil && p > 0 && !portBindable(it.Proto, p) {
					res.PortConflicts = append(res.PortConflicts, proto.PortConflict{HostPort: it.HostPort, Proto: it.Proto, Note: "端口已被占用"})
					res.OK = false
				}
			}
		}
		if it.BindPath != "" {
			if occupied, nonEmpty := pathOccupied(it.BindPath); occupied {
				note := "路径已存在"
				if nonEmpty {
					note = "路径非空（恢复将覆盖）"
				}
				res.PathConflicts = append(res.PathConflicts, proto.PathConflict{Path: it.BindPath, Note: note})
			}
		}
		if it.Image != "" {
			image = it.Image
		}
		required += it.Size
	}
	if image != "" {
		if present := imagePresent(image); present {
			res.ImagePresent = true
		} else {
			res.ImageMissing = image
		}
	}
	res.DiskRequired = required
	res.DiskAvailable = diskFree("/")
	a.sendEnv(proto.MsgPreflightResult, id, res)
}

// pathOccupied reports whether path exists, and whether it is a non-empty dir /
// an existing file (either means restore would overwrite existing data).
func pathOccupied(path string) (exists bool, nonEmpty bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, false
	}
	if !fi.IsDir() {
		return true, true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true, false
	}
	return true, len(entries) > 0
}

// imagePresent checks whether an image ref (repo:tag) is available locally.
func imagePresent(ref string) bool {
	dc := newDocker()
	resp, err := dc.req(http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == 200
}

// diskFree returns free bytes on the filesystem holding path (best-effort).
func diskFree(path string) int64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0
	}
	return int64(s.Bavail) * int64(s.Bsize)
}

// extractEntry writes one tar entry to target, guarding traversal beyond allowBase.
func extractEntry(tr *tar.Reader, hdr *tar.Header, target, allowBase string) error {
	cleanTarget := filepath.Clean(target)
	cleanBase := filepath.Clean(allowBase)
	if cleanTarget != cleanBase && !strings.HasPrefix(cleanTarget, cleanBase+string(os.PathSeparator)) {
		return nil // path traversal outside allowBase
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(cleanTarget, os.FileMode(hdr.Mode))
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, tr)
		return err
	case tar.TypeSymlink:
		_ = os.Symlink(hdr.Linkname, cleanTarget)
		return nil
	}
	return nil
}

// recreateContainer recreates a container on this host from a snapshot's Config
// + HostConfig (docker create + start), used by container restore when the
// requested target node has no same-named container. The volume data was already
// extracted to the original mount source paths, so a freshly created container
// with the same bind mounts picks it up directly.
//
// Failure modes handled gracefully:
//   - image absent on this host: create returns 404 / "No such image". With
//     autoPull we pull repo:tag and retry; otherwise we surface a clear error.
//   - the snapshot's NetworkMode points at a compose-created network (e.g.
//     "npm_default") that doesn't exist on a different host: create fails, so we
//     retry once with NetworkMode forced to "bridge".
//   - a host port in PortBindings is already taken on the target node (common in
//     cross-node DR — the original's ports may be held by another service): we
//     proactively remap occupied ports to free ephemeral ones before create, and
//     if docker still rejects a port at start, force a full remap and retry once.
func recreateContainer(name, image string, cfg, host json.RawMessage, networks map[string]json.RawMessage, autoPull bool) (detail string, recreated bool, ports map[string][]proto.PortBinding, err error) {
	dc := newDocker()
	// Don't clobber a same-named container that already runs here.
	if resp, e := dc.req(http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil); e == nil {
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			// Already present — still report its bound ports so the caller (e.g.
			// migration's domain re-point) can wire the ingress to the live port.
			return "节点已有同名容器，仅回填数据（未重建）", false, containerPorts(dc, name), nil
		}
	}
	if len(cfg) == 0 {
		return "归档缺少 container.json，仅回填数据（未重建）", false, nil, nil
	}
	var cfgMap map[string]json.RawMessage
	if e := json.Unmarshal(cfg, &cfgMap); e != nil {
		return "解析容器配置失败: " + e.Error(), false, nil, e
	}

	create := func(hostCfg json.RawMessage, nets map[string]json.RawMessage) (string, error) {
		body := buildCreateBody(cfgMap, hostCfg, nets)
		resp, e := dc.req(http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body)
		if e != nil {
			return "", e
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return "", errHTTP(resp.StatusCode, string(rb))
		}
		var cr struct {
			ID string `json:"Id"`
		}
		_ = json.Unmarshal(rb, &cr)
		if cr.ID == "" {
			return "", errHTTP(resp.StatusCode, "create returned no id: "+string(rb))
		}
		return cr.ID, nil
	}
	start := func(cid string) error {
		r, e := dc.req(http.MethodPost, "/containers/"+cid+"/start", nil)
		if e != nil {
			return e
		}
		rb, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode >= 400 {
			return errHTTP(r.StatusCode, string(rb))
		}
		return nil
	}
	remove := func(cid string) {
		if r, e := dc.req(http.MethodDelete, "/containers/"+cid+"?force=true&v=true", nil); e == nil {
			_, _ = io.ReadAll(r.Body)
			r.Body.Close()
		}
	}

	// Proactively remap any host port already in use on this node so the
	// snapshot's ports don't collide with services already running here.
	useHost := host
	if rh, note, changed := remapPorts(host, false); changed {
		useHost = rh
		detail += note
	}

	// Recreate the snapshot's user-defined networks (e.g. a compose project's
	// "sub2api_default") so the container lands on the same network with its
	// service-name aliases rather than the default bridge. A creation failure is
	// noted here and the networkMissing → bridge fallback below still catches it.
	detail += ensureNetworks(dc, networks)

	cid, cerr := create(useHost, networks)
	if cerr != nil && imageMissing(cerr) {
		if autoPull && image != "" {
			repo, tag := splitImage(image)
			if perr := dc.pullImage(repo, tag); perr == nil {
				detail = "已自动拉取镜像 " + image + "；" + detail
				cid, cerr = create(useHost, networks)
			} else {
				return detail, false, nil, fmt.Errorf("镜像 %s 拉取失败: %s", image, perr)
			}
		} else {
			return detail, false, nil, fmt.Errorf("镜像 %s 在目标节点不存在（未自动拉取；可勾选「镜像缺失时自动拉取」重试）", image)
		}
	}
	if cerr != nil && networkMissing(cerr) {
		// Retry with NetworkMode forced to the default bridge network.
		cid, cerr = create(withBridgeNetwork(useHost), nil)
		if cerr == nil {
			detail += "原网络在目标节点不存在，已回退 bridge 重建；"
		}
	}
	if cerr != nil {
		return detail, false, nil, fmt.Errorf("重建失败: %s", cerr)
	}

	// Ports bind at start. If a requested port got taken between our check and
	// now (race, or docker-internal allocation), docker fails start with "port
	// is already allocated" — force every bound port to a fresh free port and
	// recreate once.
	if serr := start(cid); serr != nil {
		if networkMissing(serr) {
			// docker validates networks at start, not at create, so a snapshot
			// whose NetworkMode points at a compose network absent on this host
			// clears create (201) but 404s start. Recreate on the default bridge
			// network and retry start once.
			remove(cid)
			cid2, cerr2 := create(withBridgeNetwork(useHost), nil)
			if cerr2 != nil {
				return detail, false, nil, fmt.Errorf("重建（bridge 回退）失败: %s", cerr2)
			}
			if serr2 := start(cid2); serr2 != nil {
				remove(cid2)
				return detail, false, nil, fmt.Errorf("启动失败（bridge 回退）: %s", serr2)
			}
			detail += "原网络在目标节点不存在，已回退 bridge 重建；"
			detail += "已重建并启动容器 " + name
			return detail, true, containerPorts(dc, cid2), nil
		}
		if portAllocated(serr) {
			remove(cid)
			if rh, note, changed := remapPorts(useHost, true); changed {
				detail += note
				if cid2, cerr2 := create(rh, networks); cerr2 == nil {
					if serr2 := start(cid2); serr2 == nil {
						detail += "已重建并启动容器 " + name
						return detail, true, containerPorts(dc, cid2), nil
					} else {
						remove(cid2)
						return detail, false, nil, fmt.Errorf("启动失败（端口）: %s", serr2)
					}
				}
			}
			return detail, false, nil, fmt.Errorf("端口被占用且无法分配空闲端口: %s", serr)
		}
		remove(cid)
		return detail, false, nil, fmt.Errorf("启动失败: %s", serr)
	}
	detail += "已重建并启动容器 " + name
	return detail, true, containerPorts(dc, cid), nil
}

// containerPorts reads a (running) container's actual host port bindings from its
// NetworkSettings.Ports, in proto.PortBinding form. Used after recreate so the
// master learns the real port a remapped container ended up on (e.g. to point a
// tunnel ingress at it after a migration). Returns nil if unreadable/none.
func containerPorts(dc *dockerClient, ref string) map[string][]proto.PortBinding {
	resp, err := dc.req(http.MethodGet, "/containers/"+url.PathEscape(ref)+"/json", nil)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var ins struct {
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIp   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if json.NewDecoder(resp.Body).Decode(&ins) != nil {
		return nil
	}
	out := map[string][]proto.PortBinding{}
	for k, bs := range ins.NetworkSettings.Ports {
		for _, b := range bs {
			out[k] = append(out[k], proto.PortBinding{HostIp: b.HostIp, HostPort: b.HostPort})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// withBridgeNetwork returns host with NetworkMode overridden to "bridge" (used
// when the snapshot referenced a network that doesn't exist on this host).
func withBridgeNetwork(host json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(host, &m) != nil {
		return host
	}
	nm, _ := json.Marshal("bridge")
	m["NetworkMode"] = nm
	out, err := json.Marshal(m)
	if err != nil {
		return host
	}
	return out
}

// protoPort splits a docker PortBindings key like "80/tcp" / "53/udp" / "8080"
// into protocol + container port. Defaults to tcp.
func protoPort(key string) (proto string, port int) {
	proto = "tcp"
	p := key
	if i := strings.Index(key, "/"); i >= 0 {
		p, proto = key[:i], key[i+1:]
	}
	port, _ = strconv.Atoi(p)
	return
}

// portBindable reports whether nothing is currently listening on the given host
// port for the protocol (so docker could bind it at start). Best-effort: it
// opens and immediately closes a listener — a tiny TOCTOU window remains, which
// the start-failure fallback in recreateContainer covers.
func portBindable(proto string, port int) bool {
	switch proto {
	case "udp":
		c, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	default:
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return false
		}
		_ = l.Close()
		return true
	}
}

// pickFreePort asks the OS for a currently-free ephemeral port for the protocol.
// Returns 0 if it could not get one.
func pickFreePort(proto string) int {
	switch proto {
	case "udp":
		c, err := net.ListenPacket("udp", ":0")
		if err != nil {
			return 0
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	default:
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
}

// remapPorts rewrites HostConfig.PortBindings in the host config so host ports
// that collide move to free ephemeral ports. With force=false, only ports
// currently not bindable are moved (keeps the snapshot's port when free). With
// force=true, every explicit host port is moved (used after a start failure).
// Returns (newHost, note, changed); note lists "key:oldport→newport".
func remapPorts(host json.RawMessage, force bool) (json.RawMessage, string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(host, &m); err != nil {
		return host, "", false
	}
	pbRaw, ok := m["PortBindings"]
	if !ok || len(pbRaw) == 0 {
		return host, "", false
	}
	type binding struct {
		HostIp   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}
	pb := map[string][]binding{}
	if err := json.Unmarshal(pbRaw, &pb); err != nil {
		return host, "", false
	}
	var notes []string
	for key, bindings := range pb {
		proto, _ := protoPort(key)
		for i := range bindings {
			hp := bindings[i].HostPort
			if hp == "" || hp == "0" {
				continue // docker auto-assigns; can't collide
			}
			port, err := strconv.Atoi(hp)
			if err != nil {
				continue
			}
			if !force && portBindable(proto, port) {
				continue // free → keep the snapshot's port
			}
			np := pickFreePort(proto)
			if np == 0 {
				continue
			}
			bindings[i].HostPort = strconv.Itoa(np)
			notes = append(notes, fmt.Sprintf("%s: %s→%d", key, hp, np))
		}
		pb[key] = bindings
	}
	if len(notes) == 0 {
		return host, "", false
	}
	out, err := json.Marshal(pb)
	if err != nil {
		return host, "", false
	}
	m["PortBindings"] = out
	newHost, err := json.Marshal(m)
	if err != nil {
		return host, "", false
	}
	return newHost, "端口被占用已改用空闲端口[" + strings.Join(notes, ", ") + "]；", true
}

func portAllocated(e error) bool {
	m := errBody(e)
	return strings.Contains(m, "port is already allocated") ||
		strings.Contains(m, "address already in use") ||
		strings.Contains(m, "bind: address already in use")
}

func errBody(e error) string {
	if h, ok := e.(httpErr); ok {
		return strings.ToLower(h.body)
	}
	return strings.ToLower(e.Error())
}
func errCode(e error) int {
	if h, ok := e.(httpErr); ok {
		return h.code
	}
	return 0
}

func imageMissing(e error) bool {
	m := errBody(e)
	return errCode(e) == 404 || strings.Contains(m, "no such image") || strings.Contains(m, "manifest unknown")
}

func networkMissing(e error) bool {
	m := errBody(e)
	return strings.Contains(m, "network") && (strings.Contains(m, "not found") || strings.Contains(m, "not exist"))
}

type httpErr struct {
	code int
	body string
}

func (e httpErr) Error() string           { return "http " + itoa(e.code) + ": " + e.body }
func errHTTP(code int, body string) error { return httpErr{code, body} }
