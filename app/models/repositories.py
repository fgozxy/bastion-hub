import uuid
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any

from app.core.db import get_cursor, get_connection
from app.core.security import hash_token


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


class NodeRepository:
    @staticmethod
    def create(
        hostname: str,
        role: str = "worker",
        env: str = "prod",
        display_name: Optional[str] = None,
        provider: Optional[str] = None,
        region: Optional[str] = None,
        ssh_user: str = "root",
        ssh_port: int = 22,
    ) -> int:
        with get_cursor() as cur:
            node_uuid = str(uuid.uuid4())
            t = now_iso()
            cur.execute(
                """
                INSERT INTO nodes (uuid, hostname, display_name, role, env, provider, region,
                                   status, ssh_user, ssh_port, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)
                """,
                (node_uuid, hostname, display_name, role, env, provider, region, ssh_user, ssh_port, t, t),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_id(node_id: int) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM nodes WHERE id = ?", (node_id,))
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def get_by_uuid(node_uuid: str) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM nodes WHERE uuid = ?", (node_uuid,))
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def list_all() -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM nodes ORDER BY created_at DESC")
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def update(node_id: int, **fields) -> bool:
        allowed = {"hostname", "display_name", "role", "env", "provider", "region",
                   "status", "ssh_user", "ssh_port", "agent_version",
                   "desired_policy_revision", "applied_policy_revision", "last_seen_at",
                   "policy_id"}
        updates = {k: v for k, v in fields.items() if k in allowed}
        if not updates:
            return False
        updates["updated_at"] = now_iso()
        set_clause = ", ".join(f"{k} = ?" for k in updates)
        with get_cursor() as cur:
            cur.execute(f"UPDATE nodes SET {set_clause} WHERE id = ?", (*updates.values(), node_id))
            return cur.rowcount > 0

    @staticmethod
    def delete(node_id: int) -> bool:
        with get_cursor() as cur:
            cur.execute("DELETE FROM nodes WHERE id = ?", (node_id,))
            return cur.rowcount > 0


class NodeAddressRepository:
    @staticmethod
    def upsert(
        node_id: int,
        family: str,
        scope: str,
        address: str,
        source: str,
        interface: Optional[str] = None,
        is_primary: bool = False,
        expires_at: Optional[str] = None,
    ) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO node_addresses (node_id, family, scope, address, source, interface,
                                            is_primary, first_seen_at, last_seen_at, expires_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(node_id, address, source) DO UPDATE SET
                    scope = excluded.scope,
                    interface = excluded.interface,
                    is_primary = excluded.is_primary,
                    last_seen_at = excluded.last_seen_at,
                    expires_at = excluded.expires_at
                """,
                (node_id, family, scope, address, source, interface, int(is_primary), t, t, expires_at),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_node(node_id: int) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM node_addresses WHERE node_id = ? ORDER BY last_seen_at DESC", (node_id,))
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def delete_old(node_id: int, source: str, keep_addresses: List[str]) -> int:
        if not keep_addresses:
            with get_cursor() as cur:
                cur.execute("DELETE FROM node_addresses WHERE node_id = ? AND source = ?", (node_id, source))
                return cur.rowcount
        placeholders = ",".join("?" for _ in keep_addresses)
        with get_cursor() as cur:
            cur.execute(
                f"DELETE FROM node_addresses WHERE node_id = ? AND source = ? AND address NOT IN ({placeholders})",
                (node_id, source, *keep_addresses),
            )
            return cur.rowcount

    @staticmethod
    def delete_by_sources(node_id: int, sources: List[str]) -> int:
        if not sources:
            return 0
        placeholders = ",".join("?" for _ in sources)
        with get_cursor() as cur:
            cur.execute(
                f"DELETE FROM node_addresses WHERE node_id = ? AND source IN ({placeholders})",
                (node_id, *sources),
            )
            return cur.rowcount


class EnrollmentTokenRepository:
    @staticmethod
    def create(token: str, role: str = "worker", env: str = "prod",
               hostname_pattern: Optional[str] = None,
               expires_at: Optional[str] = None,
               node_id: Optional[int] = None) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "INSERT INTO enrollment_tokens (token_hash, role, env, hostname_pattern, expires_at, used_by_node_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
                (hash_token(token), role, env, hostname_pattern, expires_at, node_id, t),
            )
            return cur.lastrowid

    @staticmethod
    def consume(token: str, node_id: int) -> bool:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "UPDATE enrollment_tokens SET used_at = ?, used_by_node_id = ? WHERE token_hash = ? AND used_at IS NULL",
                (t, node_id, hash_token(token)),
            )
            return cur.rowcount > 0

    @staticmethod
    def get_valid(token: str) -> Optional[Dict[str, Any]]:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "SELECT * FROM enrollment_tokens WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?",
                (hash_token(token), t),
            )
            row = cur.fetchone()
            return dict(row) if row else None


class NodeTokenRepository:
    @staticmethod
    def create(node_id: int, token: str) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "INSERT INTO node_tokens (node_id, token_hash, created_at) VALUES (?, ?, ?)",
                (node_id, hash_token(token), t),
            )
            return cur.lastrowid

    @staticmethod
    def validate(token: str) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute(
                "SELECT nt.*, n.uuid as node_uuid, n.status as node_status FROM node_tokens nt JOIN nodes n ON nt.node_id = n.id WHERE nt.token_hash = ? AND nt.status = 'active' AND n.status != 'disabled'",
                (hash_token(token),),
            )
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def revoke(token: str) -> bool:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "UPDATE node_tokens SET status = 'revoked', revoked_at = ? WHERE token_hash = ?",
                (t, hash_token(token)),
            )
            return cur.rowcount > 0


class AuditRepository:
    @staticmethod
    def log(actor_type: str, action: str, actor_id: Optional[str] = None,
            target_type: Optional[str] = None, target_id: Optional[str] = None,
            summary: Optional[str] = None, metadata: Optional[Dict] = None) -> int:
        import json
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "INSERT INTO audit_events (actor_type, actor_id, action, target_type, target_id, summary, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (actor_type, actor_id, action, target_type, target_id, summary, json.dumps(metadata) if metadata else None, t),
            )
            return cur.lastrowid


class ComposeProjectRepository:
    @staticmethod
    def upsert(
        node_id: int,
        name: str,
        project_path: Optional[str] = None,
        compose_files_json: Optional[str] = None,
        services_json: Optional[str] = None,
        status: Optional[str] = None,
        git_url: Optional[str] = None,
        git_branch: Optional[str] = None,
        current_revision: Optional[str] = None,
    ) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO compose_projects (node_id, name, project_path, compose_files_json, services_json, status, git_url, git_branch, current_revision, auto_update, last_seen_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
                ON CONFLICT(node_id, name, project_path) DO UPDATE SET
                    compose_files_json = excluded.compose_files_json,
                    services_json = excluded.services_json,
                    status = excluded.status,
                    git_url = excluded.git_url,
                    git_branch = excluded.git_branch,
                    current_revision = excluded.current_revision,
                    last_seen_at = excluded.last_seen_at
                """,
                (node_id, name, project_path, compose_files_json, services_json, status, git_url, git_branch, current_revision, t),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_node(node_id: int) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM compose_projects WHERE node_id = ? ORDER BY last_seen_at DESC", (node_id,))
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def update_auto_update(node_id: int, names: List[str], auto_update: bool) -> int:
        if not names:
            return 0
        placeholders = ",".join("?" for _ in names)
        with get_cursor() as cur:
            cur.execute(
                f"UPDATE compose_projects SET auto_update = ? WHERE node_id = ? AND name IN ({placeholders})",
                (int(auto_update), node_id, *names),
            )
            return cur.rowcount

    @staticmethod
    def delete_old(node_id: int, keep_names: List[str]) -> int:
        if not keep_names:
            with get_cursor() as cur:
                cur.execute("DELETE FROM compose_projects WHERE node_id = ?", (node_id,))
                return cur.rowcount
        placeholders = ",".join("?" for _ in keep_names)
        with get_cursor() as cur:
            cur.execute(
                f"DELETE FROM compose_projects WHERE node_id = ? AND name NOT IN ({placeholders})",
                (node_id, *keep_names),
            )
            return cur.rowcount


class JobRepository:
    @staticmethod
    def create(kind: str, payload: Optional[Dict] = None, priority: int = 100,
               created_by: Optional[str] = None, not_before: Optional[str] = None) -> int:
        import json
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "INSERT INTO jobs (kind, status, priority, payload_json, created_by, not_before, created_at, updated_at) VALUES (?, 'pending', ?, ?, ?, ?, ?, ?)",
                (kind, priority, json.dumps(payload) if payload else None, created_by, not_before, t, t),
            )
            return cur.lastrowid

    @staticmethod
    def list_pending(limit: int = 10) -> List[Dict[str, Any]]:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                "SELECT * FROM jobs WHERE status = 'pending' AND (not_before IS NULL OR not_before <= ?) ORDER BY priority, created_at LIMIT ?",
                (t, limit),
            )
            return [dict(row) for row in cur.fetchall()]


class SSHPolicyRepository:
    @staticmethod
    def create(
        name: str,
        mode: str = "report",
        trusted_user_ca_public_key: Optional[str] = None,
        allowed_principals_json: Optional[str] = None,
        allowed_source_node_ids_json: Optional[str] = None,
        allowed_source_cidrs_json: Optional[str] = None,
        sshd_config_json: Optional[str] = None,
        firewall_config_json: Optional[str] = None,
        docker_config_json: Optional[str] = None,
        agent_config_json: Optional[str] = None,
    ) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO ssh_policies (name, revision, mode, trusted_user_ca_public_key,
                    allowed_principals_json, allowed_source_node_ids_json, allowed_source_cidrs_json,
                    sshd_config_json, firewall_config_json, docker_config_json, agent_config_json, created_at)
                VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (name, mode, trusted_user_ca_public_key, allowed_principals_json,
                 allowed_source_node_ids_json, allowed_source_cidrs_json,
                 sshd_config_json, firewall_config_json, docker_config_json, agent_config_json, t),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_id(policy_id: int) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM ssh_policies WHERE id = ?", (policy_id,))
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def list_all() -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM ssh_policies ORDER BY created_at DESC")
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def update(policy_id: int, **fields) -> bool:
        allowed = {"name", "mode", "trusted_user_ca_public_key",
                   "allowed_principals_json", "allowed_source_node_ids_json",
                   "allowed_source_cidrs_json", "sshd_config_json", "firewall_config_json",
                   "docker_config_json", "agent_config_json"}
        updates = {k: v for k, v in fields.items() if k in allowed}
        if not updates:
            return False
        with get_cursor() as cur:
            cur.execute("SELECT revision FROM ssh_policies WHERE id = ?", (policy_id,))
            row = cur.fetchone()
            if not row:
                return False
            new_revision = row["revision"] + 1
            set_clause = ", ".join(f"{k} = ?" for k in updates)
            cur.execute(
                f"UPDATE ssh_policies SET {set_clause}, revision = ? WHERE id = ?",
                (*updates.values(), new_revision, policy_id),
            )
            return cur.rowcount > 0

    @staticmethod
    def delete(policy_id: int) -> bool:
        with get_cursor() as cur:
            cur.execute("DELETE FROM ssh_policies WHERE id = ?", (policy_id,))
            return cur.rowcount > 0


class CredentialRepository:
    @staticmethod
    def create(
        name: str,
        type: str = "ssh_public_key",
        public_payload: Optional[str] = None,
        encrypted_payload: Optional[str] = None,
        fingerprint: Optional[str] = None,
        scope: Optional[str] = None,
        status: str = "active",
    ) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO credentials (name, type, public_payload, encrypted_payload,
                                         fingerprint, scope, version, status, created_at)
                VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
                """,
                (name, type, public_payload, encrypted_payload, fingerprint, scope, status, t),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_id(credential_id: int) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM credentials WHERE id = ?", (credential_id,))
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def list_all() -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM credentials ORDER BY created_at DESC")
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def update(credential_id: int, **fields) -> bool:
        allowed = {"name", "public_payload", "encrypted_payload", "fingerprint",
                   "scope", "status", "version", "rotated_at", "expires_at"}
        updates = {k: v for k, v in fields.items() if k in allowed}
        if not updates:
            return False
        updates["updated_at"] = now_iso()
        set_clause = ", ".join(f"{k} = ?" for k in updates)
        with get_cursor() as cur:
            cur.execute(f"UPDATE credentials SET {set_clause} WHERE id = ?", (*updates.values(), credential_id))
            return cur.rowcount > 0

    @staticmethod
    def delete(credential_id: int) -> bool:
        with get_cursor() as cur:
            cur.execute("DELETE FROM credentials WHERE id = ?", (credential_id,))
            return cur.rowcount > 0


class CredentialBindingRepository:
    @staticmethod
    def create(credential_id: int, node_id: int, target_user: str = "root") -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO credential_bindings (credential_id, node_id, target_user, status, created_at, updated_at)
                VALUES (?, ?, ?, 'pending', ?, ?)
                ON CONFLICT(credential_id, node_id, target_user) DO UPDATE SET
                    status = 'pending',
                    error_msg = NULL,
                    updated_at = excluded.updated_at
                """,
                (credential_id, node_id, target_user, t, t),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_id(binding_id: int) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM credential_bindings WHERE id = ?", (binding_id,))
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def get_by_credential(credential_id: int) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute(
                """
                SELECT cb.*, n.display_name as node_display_name, n.hostname as node_hostname
                FROM credential_bindings cb
                JOIN nodes n ON cb.node_id = n.id
                WHERE cb.credential_id = ?
                ORDER BY cb.created_at DESC
                """,
                (credential_id,),
            )
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def get_by_node(node_id: int) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute(
                """
                SELECT cb.*, c.name as credential_name, c.public_payload, c.fingerprint
                FROM credential_bindings cb
                JOIN credentials c ON cb.credential_id = c.id
                WHERE cb.node_id = ?
                ORDER BY cb.created_at DESC
                """,
                (node_id,),
            )
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def list_pending_for_node(node_id: int) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute(
                """
                SELECT cb.*, c.name as credential_name, c.public_payload, c.fingerprint
                FROM credential_bindings cb
                JOIN credentials c ON cb.credential_id = c.id
                WHERE cb.node_id = ? AND cb.status IN ('pending', 'failed')
                ORDER BY cb.created_at DESC
                """,
                (node_id,),
            )
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def update_status(binding_id: int, status: str, error_msg: Optional[str] = None) -> bool:
        t = now_iso()
        with get_cursor() as cur:
            if error_msg is not None:
                cur.execute(
                    "UPDATE credential_bindings SET status = ?, error_msg = ?, applied_at = ?, updated_at = ? WHERE id = ?",
                    (status, error_msg, t if status == 'applied' else None, t, binding_id),
                )
            else:
                cur.execute(
                    "UPDATE credential_bindings SET status = ?, applied_at = ?, updated_at = ? WHERE id = ?",
                    (status, t if status == 'applied' else None, t, binding_id),
                )
            return cur.rowcount > 0

    @staticmethod
    def delete(binding_id: int) -> bool:
        with get_cursor() as cur:
            cur.execute("DELETE FROM credential_bindings WHERE id = ?", (binding_id,))
            return cur.rowcount > 0

    @staticmethod
    def delete_by_credential_and_nodes(credential_id: int, node_ids: List[int], target_user: Optional[str] = None) -> int:
        if not node_ids:
            return 0
        placeholders = ",".join("?" for _ in node_ids)
        with get_cursor() as cur:
            if target_user:
                cur.execute(
                    f"DELETE FROM credential_bindings WHERE credential_id = ? AND node_id IN ({placeholders}) AND target_user = ?",
                    (credential_id, *node_ids, target_user),
                )
            else:
                cur.execute(
                    f"DELETE FROM credential_bindings WHERE credential_id = ? AND node_id IN ({placeholders})",
                    (credential_id, *node_ids),
                )
            return cur.rowcount


class GitHubSettingsRepository:
    @staticmethod
    def get() -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM github_settings WHERE id = 1")
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def update(token: Optional[str] = None, webhook_secret: Optional[str] = None, enabled: Optional[int] = None, backup_repo: Optional[str] = None) -> bool:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute("SELECT id FROM github_settings WHERE id = 1")
            exists = cur.fetchone()
            if not exists:
                cur.execute(
                    "INSERT INTO github_settings (id, token, webhook_secret, enabled, backup_repo, updated_at) VALUES (1, ?, ?, ?, ?, ?)",
                    (token, webhook_secret, enabled if enabled is not None else 0, backup_repo, t),
                )
                return True
            fields = []
            values = []
            if token is not None:
                fields.append("token = ?")
                values.append(token)
            if webhook_secret is not None:
                fields.append("webhook_secret = ?")
                values.append(webhook_secret)
            if enabled is not None:
                fields.append("enabled = ?")
                values.append(enabled)
            if backup_repo is not None:
                fields.append("backup_repo = ?")
                values.append(backup_repo)
            if not fields:
                return False
            values.extend([t, 1])
            cur.execute(f"UPDATE github_settings SET {', '.join(fields)}, updated_at = ? WHERE id = ?", values)
            return cur.rowcount > 0


class GitHubRepoRepository:
    @staticmethod
    def upsert(github_id: int, full_name: str, clone_url: Optional[str] = None,
               private: bool = True, default_branch: Optional[str] = None) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO github_repos (github_id, full_name, clone_url, private, default_branch, updated_at)
                VALUES (?, ?, ?, ?, ?, ?)
                ON CONFLICT(github_id) DO UPDATE SET
                    full_name = excluded.full_name,
                    clone_url = excluded.clone_url,
                    private = excluded.private,
                    default_branch = excluded.default_branch,
                    updated_at = excluded.updated_at
                """,
                (github_id, full_name, clone_url, int(private), default_branch, t),
            )
            return cur.lastrowid

    @staticmethod
    def list_all() -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM github_repos ORDER BY updated_at DESC")
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def get_by_full_name(full_name: str) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM github_repos WHERE full_name = ?", (full_name,))
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def clear_all() -> int:
        with get_cursor() as cur:
            cur.execute("DELETE FROM github_repos")
            return cur.rowcount


class NodeGitHubBindingRepository:
    @staticmethod
    def create(node_id: int, repo_full_name: str, deploy_key_id: Optional[int] = None,
               branch: str = "main", compose_file: str = "docker-compose.yml",
               project_path: Optional[str] = None, auto_deploy: bool = True,
               compose_project_name: Optional[str] = None) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO node_github_bindings (node_id, compose_project_name, repo_full_name, deploy_key_id, branch, compose_file, project_path, auto_deploy, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(node_id, compose_project_name) DO UPDATE SET
                    repo_full_name = excluded.repo_full_name,
                    deploy_key_id = excluded.deploy_key_id,
                    branch = excluded.branch,
                    compose_file = excluded.compose_file,
                    project_path = excluded.project_path,
                    auto_deploy = excluded.auto_deploy,
                    updated_at = excluded.updated_at
                """,
                (node_id, compose_project_name, repo_full_name, deploy_key_id, branch, compose_file, project_path, int(auto_deploy), t, t),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_node(node_id: int) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM node_github_bindings WHERE node_id = ? ORDER BY created_at DESC", (node_id,))
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def get_by_repo(repo_full_name: str) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM node_github_bindings WHERE repo_full_name = ? ORDER BY created_at DESC", (repo_full_name,))
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def get_by_id(binding_id: int) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM node_github_bindings WHERE id = ?", (binding_id,))
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def update(binding_id: int, **fields) -> bool:
        allowed = {"repo_full_name", "branch", "compose_file", "project_path", "auto_deploy"}
        updates = {k: v for k, v in fields.items() if k in allowed}
        if not updates:
            return False
        t = now_iso()
        set_clause = ", ".join(f"{k} = ?" for k in updates)
        with get_cursor() as cur:
            cur.execute(f"UPDATE node_github_bindings SET {set_clause}, updated_at = ? WHERE id = ?", (*updates.values(), t, binding_id))
            return cur.rowcount > 0

    @staticmethod
    def delete(binding_id: int) -> bool:
        with get_cursor() as cur:
            cur.execute("DELETE FROM node_github_bindings WHERE id = ?", (binding_id,))
            return cur.rowcount > 0


class CloudflareSettingsRepository:
    @staticmethod
    def get() -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute("SELECT * FROM cloudflare_settings ORDER BY id DESC LIMIT 1")
            row = cur.fetchone()
            return dict(row) if row else None

    @staticmethod
    def upsert(
        account_id: str,
        api_token_encrypted: Optional[str] = None,
        list_name_ipv4: str = "bastion_nodes_ipv4",
        list_name_ipv6: str = "bastion_nodes_ipv6",
        mode: str = "add_only",
        delete_grace_days: int = 7,
        enabled: bool = False,
    ) -> int:
        t = now_iso()
        existing = CloudflareSettingsRepository.get()
        with get_cursor() as cur:
            if existing:
                cur.execute(
                    """
                    UPDATE cloudflare_settings SET
                        account_id = ?,
                        api_token_encrypted = COALESCE(?, api_token_encrypted),
                        list_name_ipv4 = ?,
                        list_name_ipv6 = ?,
                        mode = ?,
                        delete_grace_days = ?,
                        enabled = ?,
                        updated_at = ?
                    WHERE id = ?
                    """,
                    (account_id, api_token_encrypted, list_name_ipv4, list_name_ipv6,
                     mode, delete_grace_days, int(enabled), t, existing["id"]),
                )
                return existing["id"]
            else:
                cur.execute(
                    """
                    INSERT INTO cloudflare_settings
                        (account_id, api_token_encrypted, list_name_ipv4, list_name_ipv6,
                         mode, delete_grace_days, enabled, created_at, updated_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (account_id, api_token_encrypted, list_name_ipv4, list_name_ipv6,
                     mode, delete_grace_days, int(enabled), t, t),
                )
                return cur.lastrowid


class CloudflareSyncLogRepository:
    @staticmethod
    def create(
        kind: str,
        mode: str,
        desired_count: int = 0,
        actual_count: int = 0,
        added_count: int = 0,
        removed_count: int = 0,
        skipped_count: int = 0,
        dry_run: bool = False,
        details: Optional[dict] = None,
    ) -> int:
        t = now_iso()
        with get_cursor() as cur:
            cur.execute(
                """
                INSERT INTO cloudflare_sync_logs
                    (kind, mode, desired_count, actual_count, added_count, removed_count,
                     skipped_count, dry_run, details_json, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (kind, mode, desired_count, actual_count, added_count, removed_count,
                 skipped_count, int(dry_run), json.dumps(details) if details else None, t),
            )
            return cur.lastrowid

    @staticmethod
    def list_recent(limit: int = 50) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute(
                "SELECT * FROM cloudflare_sync_logs ORDER BY created_at DESC LIMIT ?",
                (limit,),
            )
            return [dict(row) for row in cur.fetchall()]


class MaintenanceLogRepository:
    @staticmethod
    def create(node_id: int, report: Optional[dict] = None, warnings: Optional[List[str]] = None, checked_at: Optional[str] = None) -> int:
        t = checked_at or now_iso()
        with get_cursor() as cur:
            cur.execute(
                "INSERT INTO maintenance_logs (node_id, report_json, warnings_json, checked_at) VALUES (?, ?, ?, ?)",
                (node_id, json.dumps(report) if report else None, json.dumps(warnings) if warnings else None, t),
            )
            return cur.lastrowid

    @staticmethod
    def get_by_node(node_id: int, limit: int = 10) -> List[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute(
                "SELECT * FROM maintenance_logs WHERE node_id = ? ORDER BY checked_at DESC LIMIT ?",
                (node_id, limit),
            )
            return [dict(row) for row in cur.fetchall()]

    @staticmethod
    def get_latest_by_node(node_id: int) -> Optional[Dict[str, Any]]:
        with get_cursor() as cur:
            cur.execute(
                "SELECT * FROM maintenance_logs WHERE node_id = ? ORDER BY checked_at DESC LIMIT 1",
                (node_id,),
            )
            row = cur.fetchone()
            return dict(row) if row else None
