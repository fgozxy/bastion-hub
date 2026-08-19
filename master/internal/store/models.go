package store

// Node is a managed host running an agent.
type Node struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	EnrollmentToken string `json:"enrollment_token,omitempty"`
	AgentToken      string `json:"-"`
	Status          string `json:"status"`
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	Kernel          string `json:"kernel"`
	IPv4            string `json:"ipv4"`
	IPv6            string `json:"ipv6"`
	CountryCode     string `json:"country_code"`
	Country         string `json:"country"`
	AgentVersion    string `json:"agent_version"`
	LastSeen        int64  `json:"last_seen"`
	CreatedAt       int64  `json:"created_at"`
	SshPort         string `json:"ssh_port"`
}

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
}

type Credential struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PubKey      string `json:"pub_key"`
	PrivKey     string `json:"priv_key,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	NodeID      string `json:"node_id"`
	CreatedAt   int64  `json:"created_at"`
}

type Backup struct {
	ID            string `json:"id"`
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`
	Paths         string `json:"paths"`
	Container     string `json:"container"`
	ContainerName string `json:"container_name"`
	Size          int64  `json:"size"`
	Target        string `json:"target"`
	StagePath     string `json:"-"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	Manifest      string `json:"-"`
	CreatedAt     int64  `json:"created_at"`
}

type BackupTarget struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // github | onedrive | vps
	Name      string `json:"name"`
	Config    string `json:"config"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

type Schedule struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // backup | container_update
	NodeID    string `json:"node_id"`
	Config    string `json:"config"`
	Cron      string `json:"cron"`
	Enabled   bool   `json:"enabled"`
	LastRun   int64  `json:"last_run"`
	NextRun   int64  `json:"next_run"`
	CreatedAt int64  `json:"created_at"`
}

type Command struct {
	ID         string `json:"id"`
	NodeIDs    string `json:"node_ids"`
	Cmd        string `json:"cmd"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	Author     string `json:"author"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at"`
}

type Metric struct {
	NodeID    string  `json:"node_id"`
	Ts        int64   `json:"ts"`
	CPU       float64 `json:"cpu"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
	Load1     float64 `json:"load1"`
}

type Audit struct {
	ID     int64  `json:"id"`
	Ts     int64  `json:"ts"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

type Session struct {
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// CommandLine is one chunk of output for a (command, node) pair.
type CommandLine struct {
	NodeID string `json:"node_id"`
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

// SavedCommand is a reusable script shown in the "常用命令" list.
type SavedCommand struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Script    string `json:"script"`
	Builtin   bool   `json:"builtin"`
	CreatedAt int64  `json:"created_at"`
}

// Container is a docker container discovered on a node (latest inventory).
type Container struct {
	NodeID      string `json:"node_id"`
	ContainerID string `json:"container_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Image       string `json:"image"`
	ImageID     string `json:"image_id"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Created     int64  `json:"created"`
	Updated     int64  `json:"updated"`
	UpdateType  string `json:"update_type"`
	HasUpdate   int    `json:"has_update"` // scan cache: -1 unknown, 0 digest matches, 1 remote tag content differs
	Note        string `json:"note"`       // scan cache note (e.g. reason)
	ScannedAt   int64  `json:"scanned_at"` // unix seconds; 0 means never scanned
}

// HealthNode marks a node as Netdata-enabled in the health panel.
type HealthNode struct {
	NodeID      string `json:"node_id"`
	Enabled     bool   `json:"enabled"`
	InstalledAt int64  `json:"installed_at"`
	Cores       int    `json:"cores"`
}

// HealthAlert is a per-node metric threshold the master evaluates against polled
// Netdata samples. metric ∈ load1|load5|iowait|swap|cpu|mem|disk.
type HealthAlert struct {
	ID           string  `json:"id"`
	NodeID       string  `json:"node_id"`
	Metric       string  `json:"metric"`
	Threshold    float64 `json:"threshold"`
	WindowSec    int     `json:"window_sec"`
	Enabled      bool    `json:"enabled"`
	LastNotified int64   `json:"last_notified"`
	BreachSince  int64   `json:"breach_since"`
}
