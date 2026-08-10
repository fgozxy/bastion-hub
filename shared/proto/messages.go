// Package proto defines the JSON message protocol shared between the master
// server and the agents. All communication over the agent websocket uses an
// Envelope with a typed Data payload.
package proto

import "encoding/json"

// Envelope is the outer message exchanged over the agent websocket.
type Envelope struct {
	Type string          `json:"type"` // see Msg* constants
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Message types master -> agent.
const (
	MsgExec             = "exec"
	MsgScanSSH          = "scan_ssh"
	MsgBackup           = "backup"
	MsgRestore          = "restore"
	MsgContainerOp      = "container_op"
	MsgContainerScan    = "container_scan"    // master asks agent to scan containers for available updates
	MsgAgentUpdate      = "agent_update"      // in-place self-update to the latest binary
	MsgTestSSH          = "test_ssh"          // master asks agent to real-SSH-test a stored private key against this host
	MsgRestorePreflight = "restore_preflight" // master asks agent to feasibility-check a restore without touching data
	MsgPing             = "ping"
	MsgHTTPFetch        = "http_fetch" // master asks agent to GET a node-local URL (e.g. http://127.0.0.1:19999/...) and return the body
)

// Message types agent -> master.
const (
	MsgHello               = "hello"
	MsgMetrics             = "metrics"
	MsgExecOutput          = "exec_output"
	MsgSSHKeys             = "ssh_keys"
	MsgNewKeys             = "new_keys" // auto-collected keys produced by a command
	MsgBackupResult        = "backup_result"
	MsgRestoreResult       = "restore_result"
	MsgRestoreProgress     = "restore_progress" // streamed by the agent during a restore (0..N before the final result)
	MsgPreflightResult     = "preflight_result" // reply to MsgRestorePreflight (routed by ID)
	MsgContainerResult     = "container_result"
	MsgContainers          = "containers"            // periodic container inventory report
	MsgContainerScanResult = "container_scan_result" // reply to MsgContainerScan (routed by ID)
	MsgAgentUpdateResult   = "agent_update_result"   // reply to MsgAgentUpdate (routed by ID)
	MsgTestSSHResult       = "test_ssh_result"       // reply to MsgTestSSH (routed by ID)
	MsgPong                = "pong"
	MsgHTTPFetchResult     = "http_fetch_result" // reply to MsgHTTPFetch (routed by ID)
)

// HelloData is sent by the agent right after authenticating.
type HelloData struct {
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Kernel       string `json:"kernel"`
	IPv4         string `json:"ipv4"`
	IPv6         string `json:"ipv6"`
	Timezone     string `json:"timezone"`
	Uptime       int64  `json:"uptime"`
}

// MetricsData is reported periodically.
type MetricsData struct {
	CPU       float64 `json:"cpu"`        // 0-100 percent
	MemTotal  uint64  `json:"mem_total"`  // bytes
	MemUsed   uint64  `json:"mem_used"`   // bytes
	DiskTotal uint64  `json:"disk_total"` // bytes (root fs)
	DiskUsed  uint64  `json:"disk_used"`  // bytes
	Load1     float64 `json:"load1"`
	Uptime    int64   `json:"uptime"`
	NetRx     uint64  `json:"net_rx"` // bytes/sec
	NetTx     uint64  `json:"net_tx"` // bytes/sec
}

// ExecRequest asks the agent to run a shell command.
type ExecRequest struct {
	Cmd     string `json:"cmd"`
	Timeout int    `json:"timeout,omitempty"` // seconds, 0 = no timeout
}

// ExecOutput streams command output. Done=true marks completion with Exit.
type ExecOutput struct {
	Stream string `json:"stream"` // "stdout" | "stderr"
	Data   string `json:"data"`
	Done   bool   `json:"done,omitempty"`
	Exit   int    `json:"exit,omitempty"`
}

// SSHKey describes a discovered public key.
type SSHKey struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	PubKey    string `json:"pub_key"`
	PrivKey   string `json:"priv_key,omitempty"`   // sibling private key, when available (auto-collect / keypair scan)
	User      string `json:"user,omitempty"`       // owning account (e.g. ubuntu / root) — comprehensive scan only
	Identity  string `json:"identity,omitempty"`   // merge group (key comment, fallback user) — same-identity dedup
	Merged    int    `json:"merged,omitempty"`     // how many same-identity keys this representative stands in for
	Mtime     int64  `json:"mtime,omitempty"`      // source file mtime (unix s) — used to pick the newest key
	Works     bool   `json:"works,omitempty"`      // real SSH login with this private key succeeded (keypair scan only)
	WorksNote string `json:"works_note,omitempty"` // why the login test failed / was skipped (debug)
}

// ScanSSHRequest asks the agent to scan keys and validate the SSH service. Port
// is the admin-configured SSH port (default 22) used to banner-check the local
// sshd — proving SSH actually answers on that port.
type ScanSSHRequest struct {
	Port int `json:"port,omitempty"`
}

// SSHKeysData is the response to a scan_ssh request.
type SSHKeysData struct {
	Keys            []SSHKey `json:"keys"`                        // inbound: authorized_keys entries (public only)
	Keypairs        []SSHKey `json:"keypairs,omitempty"`          // outbound: this host's own *.pub+private keypairs
	SshPort         int      `json:"ssh_port,omitempty"`          // admin-configured port (echo)
	SshDetectedPort int      `json:"ssh_detected_port,omitempty"` // port that actually answered SSH (0 if none)
	SshReachable    bool     `json:"ssh_reachable"`
	SshBanner       string   `json:"ssh_banner,omitempty"`
}

// TestSSHRequest asks the agent to REAL-ssh-test a stored private key against
// THIS host's sshd — the ground-truth check that the key actually grants login
// here. PrivKey is the PEM; PubKey (optional) is only for the agent's note.
type TestSSHRequest struct {
	PrivKey string `json:"priv_key"`
	PubKey  string `json:"pub_key,omitempty"`
	Port    int    `json:"port,omitempty"` // admin-configured SSH port (default 22)
}

// TestSSHResult is the reply to a test_ssh request.
type TestSSHResult struct {
	Works bool   `json:"works"`          // a real SSH public-key login succeeded
	User  string `json:"user,omitempty"` // the account the login succeeded as
	Port  int    `json:"port,omitempty"` // the port actually tested against
	Note  string `json:"note,omitempty"` // why it failed / was skipped (debug)
}

// NewKeysData carries keys auto-collected after a command run.
type NewKeysData struct {
	SourceCmdID string   `json:"source_cmd_id,omitempty"`
	Keys        []SSHKey `json:"keys"`
}

// BackupRequest asks the agent to tar+gzip paths and upload the result.
type BackupRequest struct {
	Paths     []string        `json:"paths,omitempty"`
	Container string          `json:"container,omitempty"` // container id/name → back up its volumes + config
	Upload    string          `json:"upload"`              // http(s) URL the agent POSTs the archive to
	Token     string          `json:"token"`               // bearer token for the upload
	Exclude   []string        `json:"exclude,omitempty"`   // host-path prefixes to skip (e.g. circular dirs)
	S3Upload  *S3UploadConfig `json:"s3_upload,omitempty"` // direct S3/MinIO upload; bypasses panel staging
}

// S3UploadConfig lets the agent stream a backup archive directly to an
// S3-compatible target. It is intentionally narrow: the master only sends it
// for one MinIO/S3 target so older multi-target flows keep using panel staging.
type S3UploadConfig struct {
	Endpoint           string `json:"endpoint"`
	AccessKey          string `json:"access_key"`
	SecretKey          string `json:"secret_key"`
	Bucket             string `json:"bucket"`
	Object             string `json:"object"`
	Region             string `json:"region,omitempty"`
	PathStyle          bool   `json:"path_style,omitempty"`
	Secure             bool   `json:"secure,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
}

// BackupResult reports the outcome of a backup operation.
type BackupResult struct {
	OK       bool            `json:"ok"`
	Size     int64           `json:"size"`
	Err      string          `json:"err,omitempty"`
	Manifest json.RawMessage `json:"manifest,omitempty"` // container.json-derived footprint (image/ports/binds) for preflight
}

// RestoreRequest asks the agent to download an archive and extract it.
type RestoreRequest struct {
	Download string `json:"download"` // http(s) URL the agent GETs the archive from
	Token    string `json:"token"`
	Dest     string `json:"dest"` // destination directory
	// Recreate (container backups only): after extracting the volumes, if no
	// same-named container exists on this host, recreate it from the archived
	// container.json (Config + HostConfig) via docker create + start. Old agents
	// ignore this field and only restore the data (backward compatible).
	Recreate bool `json:"recreate,omitempty"`
	// AutoPull: when Recreate would fail because the image is absent on this
	// host, pull it first (repo:tag from the snapshot). Default false — report a
	// clear error instead of silently pulling.
	AutoPull bool `json:"auto_pull,omitempty"`
}

// PortBinding mirrors one entry of docker's NetworkSettings.Ports binding list
// (keyed by "containerPort/proto" in the Ports map). Returned by the agent after
// recreate so the master knows the actual host ports the new container bound —
// needed to re-point a tunnel ingress after a port remap on the target node.
type PortBinding struct {
	HostIp   string `json:"HostIp,omitempty"`
	HostPort string `json:"HostPort,omitempty"`
}

// RestoreResult reports the outcome of a restore.
type RestoreResult struct {
	OK        bool   `json:"ok"`
	Err       string `json:"err,omitempty"`
	Detail    string `json:"detail,omitempty"`    // human-readable note (recreated / skipped / fell back to bridge / …)
	Recreated bool   `json:"recreated,omitempty"` // a container was created+started during this restore
	// Ports is the recreated container's actual host port bindings (docker
	// NetworkSettings.Ports shape, key "8089/tcp"). Filled by agents >= 1.9.0 after
	// a successful recreate; absent on older agents. Lets the master learn the
	// real port a remapped container ended up on, to wire the domain ingress.
	Ports map[string][]PortBinding `json:"ports,omitempty"`
}

// RestoreProgress is streamed by the agent during a restore (0..N times before
// the final RestoreResult). stage is one of "download" | "extract" | "recreate".
type RestoreProgress struct {
	Stage      string `json:"stage"`
	Label      string `json:"label,omitempty"` // human note e.g. "拉取镜像 nginx:1.25"
	BytesDone  int64  `json:"bytes_done,omitempty"`
	BytesTotal int64  `json:"bytes_total,omitempty"`
	Percent    int    `json:"percent,omitempty"` // 0..100 (derived; convenience for clients)
}

// PreflightItem describes one aspect of a candidate restore target's footprint
// on a node — a bound host port, a bind-mount host path, the image, the size.
// The agent checks each locally without touching data.
type PreflightItem struct {
	HostPort string `json:"host_port,omitempty"` // e.g. "8080"
	Proto    string `json:"proto,omitempty"`     // tcp | udp
	BindPath string `json:"bind_path,omitempty"` // host source path of a mount
	Image    string `json:"image,omitempty"`
	Size     int64  `json:"size,omitempty"` // archive size (disk estimate)
}

// RestorePreflightRequest asks the agent to check local feasibility of a set of
// items without touching data: are these ports free, are these bind paths
// occupied, is the image present, is there disk space.
type RestorePreflightRequest struct {
	Items []PreflightItem `json:"items"`
}

// PortConflict / PathConflict describe one predicted collision on the target.
type PortConflict struct {
	HostPort string `json:"host_port"`
	Proto    string `json:"proto"`
	Note     string `json:"note,omitempty"`
}

type PathConflict struct {
	Path string `json:"path"`
	Note string `json:"note,omitempty"` // "occupied" | "non-empty-dir"
}

// PreflightResult is the agent's feasibility report. Port conflicts are hard
// blockers (OK=false); path conflicts are warnings (data would be overwritten).
type PreflightResult struct {
	OK                     bool           `json:"ok"`
	PortConflicts          []PortConflict `json:"port_conflicts,omitempty"`
	PathConflicts          []PathConflict `json:"path_conflicts,omitempty"`
	ImagePresent           bool           `json:"image_present"`
	ImageMissing           string         `json:"image_missing,omitempty"`
	DiskAvailable          int64          `json:"disk_available"`
	DiskRequired           int64          `json:"disk_required"`
	AgentSupportsPreflight bool           `json:"agent_supports_preflight"` // always true from 1.8.0
}

// BackupManifest is captured from container.json at backup time and persisted
// on the backup row, so preflight can run against a target node without reading
// the (possibly evicted) archive.
type BackupManifest struct {
	Image string          `json:"image,omitempty"`
	Ports []PreflightItem `json:"ports,omitempty"`
	Binds []PreflightItem `json:"binds,omitempty"`
	Size  int64           `json:"size,omitempty"`
}

// ContainerOpRequest asks the agent to act on containers.
type ContainerOpRequest struct {
	Action   string   `json:"action"`              // update | restart | start | stop | rebuild | upgrade | delete
	IDs      []string `json:"ids,omitempty"`       // specific container IDs; empty = all running (for update)
	Label    string   `json:"label,omitempty"`     // optional docker label filter (update only)
	NewImage string   `json:"new_image,omitempty"` // upgrade: target image ref (repo:tag) to switch to
}

// ContainerResult reports per-container operation results.
type ContainerResult struct {
	OK        bool              `json:"ok"`
	Err       string            `json:"err,omitempty"`
	Updated   []string          `json:"updated,omitempty"`
	Unchanged []string          `json:"unchanged,omitempty"`
	Skipped   []string          `json:"skipped,omitempty"`
	Failed    map[string]string `json:"failed,omitempty"`
	// Details is retained for compatibility with older masters and agents.
	Details map[string]string `json:"details,omitempty"`
}

// ContainerInfo is one entry in the periodic container inventory report.
type ContainerInfo struct {
	ID         string `json:"id"`          // docker container id
	Name       string `json:"name"`        // primary container name (no leading /)
	Image      string `json:"image"`       // repo:tag as configured
	ImageID    string `json:"image_id"`    // image digest/id (sha256:...) — version fingerprint
	State      string `json:"state"`       // running | exited | created | ...
	Status     string `json:"status"`      // human string, e.g. "Up 2 minutes"
	Created    int64  `json:"created"`     // unix seconds
	UpdateType string `json:"update_type"` // latest | tag | unmanaged | pinned | build | local — how this container can be updated
	// HostPorts are the container's published host ports (docker
	// /containers/json Ports[].PublicPort), reported by agents >= 1.9.2. The
	// master caches them so container-migration's domain pre-plan can match a
	// container to its tunnel ingress rule (hostname→service port) before backup.
	HostPorts []int `json:"host_ports,omitempty"`
}

// ContainersData is the periodic inventory report from an agent.
type ContainersData struct {
	Containers []ContainerInfo `json:"containers"`
	// TunnelID is this node's Cloudflare Tunnel id, best-effort discovered by the
	// agent from the local cloudflared container's token (agents >= 1.9.0). Empty
	// when the node has no cloudflared or runs an older agent. The master caches it
	// per node so container migration knows which tunnel owns a domain.
	TunnelID string `json:"tunnel_id,omitempty"`
}

// ContainerScanItem is one container's update-readiness assessment.
type ContainerScanItem struct {
	Name           string `json:"name"`
	NodeID         string `json:"node_id,omitempty"` // filled by master when aggregating across nodes
	Image          string `json:"image"`
	State          string `json:"state,omitempty"` // running | exited | created | ...
	UpdateType     string `json:"update_type"`     // latest | tag | unmanaged | pinned | build | local
	HasUpdate      int    `json:"has_update"`      // -1 unknown, 0 up to date, 1 newer version available
	RegistryDigest string `json:"registry_digest,omitempty"`
	LocalDigest    string `json:"local_digest,omitempty"`
	// SuggestedImage is a newer repo:tag the scheduler should upgrade to
	// (semver tag bump). Empty means same tag / digest-only update path.
	SuggestedImage string `json:"suggested_image,omitempty"`
	Convertible    bool   `json:"convertible"` // build using a public image name (could switch to pull)
	Note           string `json:"note,omitempty"`
}

// ContainerScanResult is the agent's reply to MsgContainerScan.
type ContainerScanResult struct {
	OK    bool                `json:"ok"`
	Err   string              `json:"err,omitempty"`
	Items []ContainerScanItem `json:"items"`
}

// AgentUpdateResult reports the outcome of a self-update.
type AgentUpdateResult struct {
	OK      bool   `json:"ok"`
	Err     string `json:"err,omitempty"`
	Version string `json:"version,omitempty"`
}

// HTTPFetchMaxBody caps the body the agent will return for one MsgHTTPFetch
// call. Prevents a misbehaving local service from saturating the websocket or
// OOMing the agent.
const HTTPFetchMaxBody = 512 * 1024 // 512KB

// HTTPFetchRequest asks the agent to perform an in-process net/http GET to a
// node-local URL (e.g. http://127.0.0.1:19999/api/v1/...) and return the body.
// The agent enforces a loopback-only host check (anti-SSRF) and a body cap.
// Avoids spawning curl per metric poll.
type HTTPFetchRequest struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout,omitempty"` // seconds, 0 = default 10
}

// HTTPFetchResult is the reply to MsgHTTPFetch. Body is capped at
// HTTPFetchMaxBody. Status is 0 on a transport error (see Err).
type HTTPFetchResult struct {
	Status int    `json:"status"`
	Body   string `json:"body,omitempty"`
	Err    string `json:"err,omitempty"`
}

// Encode wraps a payload into an Envelope.
func Encode(typ, id string, v any) (*Envelope, error) {
	var raw json.RawMessage
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return &Envelope{Type: typ, ID: id, Data: raw}, nil
}
