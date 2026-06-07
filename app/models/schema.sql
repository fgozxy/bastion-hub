-- Nodes table
CREATE TABLE IF NOT EXISTS nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid TEXT NOT NULL UNIQUE,
    hostname TEXT NOT NULL,
    display_name TEXT,
    role TEXT NOT NULL DEFAULT 'worker',
    env TEXT NOT NULL DEFAULT 'prod',
    provider TEXT,
    region TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    ssh_user TEXT NOT NULL DEFAULT 'root',
    ssh_port INTEGER NOT NULL DEFAULT 22,
    agent_version TEXT,
    desired_policy_revision INTEGER NOT NULL DEFAULT 0,
    applied_policy_revision INTEGER NOT NULL DEFAULT 0,
    last_seen_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_role ON nodes(role);
CREATE INDEX IF NOT EXISTS idx_nodes_env ON nodes(env);

-- Node addresses
CREATE TABLE IF NOT EXISTS node_addresses (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    family TEXT NOT NULL,
    scope TEXT NOT NULL,
    address TEXT NOT NULL,
    interface TEXT,
    source TEXT NOT NULL,
    is_primary INTEGER NOT NULL DEFAULT 0,
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at TEXT,
    UNIQUE(node_id, address, source),
    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_addresses_node_id ON node_addresses(node_id);
CREATE INDEX IF NOT EXISTS idx_node_addresses_address ON node_addresses(address);

-- Docker snapshots
CREATE TABLE IF NOT EXISTS docker_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    docker_available INTEGER NOT NULL,
    docker_version TEXT,
    compose_version TEXT,
    containers_running INTEGER DEFAULT 0,
    containers_total INTEGER DEFAULT 0,
    images_total INTEGER DEFAULT 0,
    networks_total INTEGER DEFAULT 0,
    volumes_total INTEGER DEFAULT 0,
    raw_json TEXT,
    collected_at TEXT NOT NULL,
    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_docker_snapshots_node_id ON docker_snapshots(node_id);

-- Compose projects
CREATE TABLE IF NOT EXISTS compose_projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    project_path TEXT,
    compose_files_json TEXT,
    services_json TEXT,
    status TEXT,
    git_url TEXT,
    git_branch TEXT,
    current_revision TEXT,
    auto_update INTEGER NOT NULL DEFAULT 1,
    last_seen_at TEXT NOT NULL,
    UNIQUE(node_id, name, project_path),
    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_compose_projects_node_id ON compose_projects(node_id);

-- Credentials
CREATE TABLE IF NOT EXISTS credentials (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    name TEXT NOT NULL,
    scope TEXT,
    encrypted_payload TEXT,
    public_payload TEXT,
    fingerprint TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    rotated_at TEXT,
    expires_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_credentials_type ON credentials(type);
CREATE INDEX IF NOT EXISTS idx_credentials_status ON credentials(status);

-- SSH policies
CREATE TABLE IF NOT EXISTS ssh_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL,
    mode TEXT NOT NULL DEFAULT 'report',
    trusted_user_ca_public_key TEXT,
    allowed_principals_json TEXT,
    allowed_source_node_ids_json TEXT,
    allowed_source_cidrs_json TEXT,
    sshd_config_json TEXT,
    firewall_config_json TEXT,
    docker_config_json TEXT,
    agent_config_json TEXT,
    created_at TEXT NOT NULL
);

-- Jobs
CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    priority INTEGER NOT NULL DEFAULT 100,
    payload_json TEXT,
    locked_by TEXT,
    locked_at TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    not_before TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_kind ON jobs(kind);
CREATE INDEX IF NOT EXISTS idx_jobs_not_before ON jobs(not_before);

-- Audit events
CREATE TABLE IF NOT EXISTS audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    action TEXT NOT NULL,
    target_type TEXT,
    target_id TEXT,
    summary TEXT,
    metadata_json TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_events_created_at ON audit_events(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events(actor_type, actor_id);

-- Enrollment tokens
CREATE TABLE IF NOT EXISTS enrollment_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    role TEXT NOT NULL DEFAULT 'worker',
    env TEXT NOT NULL DEFAULT 'prod',
    hostname_pattern TEXT,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    used_by_node_id INTEGER,
    created_at TEXT NOT NULL,
    FOREIGN KEY(used_by_node_id) REFERENCES nodes(id) ON DELETE SET NULL
);

-- Credential bindings
CREATE TABLE IF NOT EXISTS credential_bindings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id INTEGER NOT NULL,
    node_id INTEGER NOT NULL,
    target_user TEXT NOT NULL DEFAULT 'root',
    status TEXT NOT NULL DEFAULT 'pending',
    applied_at TEXT,
    error_msg TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(credential_id, node_id, target_user),
    FOREIGN KEY(credential_id) REFERENCES credentials(id) ON DELETE CASCADE,
    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_credential_bindings_node_id ON credential_bindings(node_id);
CREATE INDEX IF NOT EXISTS idx_credential_bindings_credential_id ON credential_bindings(credential_id);

-- Node tokens
CREATE TABLE IF NOT EXISTS node_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    revoked_at TEXT,
    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_tokens_node_id ON node_tokens(node_id);

-- GitHub settings
CREATE TABLE IF NOT EXISTS github_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT,
    webhook_secret TEXT,
    enabled INTEGER NOT NULL DEFAULT 0,
    backup_repo TEXT,
    updated_at TEXT NOT NULL
);

-- GitHub repos cache
CREATE TABLE IF NOT EXISTS github_repos (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    github_id INTEGER UNIQUE,
    full_name TEXT NOT NULL UNIQUE,
    clone_url TEXT,
    private INTEGER NOT NULL DEFAULT 1,
    default_branch TEXT,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_github_repos_full_name ON github_repos(full_name);

-- Node GitHub bindings (per compose project)
CREATE TABLE IF NOT EXISTS node_github_bindings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    compose_project_name TEXT,
    repo_full_name TEXT NOT NULL,
    deploy_key_id INTEGER,
    branch TEXT DEFAULT 'main',
    compose_file TEXT DEFAULT 'docker-compose.yml',
    project_path TEXT,
    auto_deploy INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(node_id, compose_project_name),
    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_github_bindings_node_id ON node_github_bindings(node_id);

-- Cloudflare settings
CREATE TABLE IF NOT EXISTS cloudflare_settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT,
    api_token_encrypted TEXT,
    list_name_ipv4 TEXT DEFAULT 'bastion_nodes_ipv4',
    list_name_ipv6 TEXT DEFAULT 'bastion_nodes_ipv6',
    mode TEXT NOT NULL DEFAULT 'add_only',
    delete_grace_days INTEGER DEFAULT 7,
    enabled INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Cloudflare sync logs
CREATE TABLE IF NOT EXISTS cloudflare_sync_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    mode TEXT NOT NULL,
    desired_count INTEGER,
    actual_count INTEGER,
    added_count INTEGER,
    removed_count INTEGER,
    skipped_count INTEGER,
    dry_run INTEGER NOT NULL DEFAULT 0,
    details_json TEXT,
    created_at TEXT NOT NULL
);

-- Maintenance logs
CREATE TABLE IF NOT EXISTS maintenance_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    report_json TEXT,
    warnings_json TEXT,
    checked_at TEXT NOT NULL,
    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
