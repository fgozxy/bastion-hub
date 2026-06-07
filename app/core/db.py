import sqlite3
import os
from typing import Optional
from contextlib import contextmanager

from app.core.config import get_settings

INIT_SQL_PATH = os.path.join(os.path.dirname(__file__), "..", "models", "schema.sql")


def get_db_path() -> str:
    return get_settings().database_url


@contextmanager
def get_connection():
    db_path = get_db_path()
    os.makedirs(os.path.dirname(db_path), exist_ok=True)
    conn = sqlite3.connect(db_path, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    try:
        yield conn
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


@contextmanager
def get_cursor():
    with get_connection() as conn:
        cursor = conn.cursor()
        yield cursor
        conn.commit()


def init_db():
    db_path = get_db_path()
    os.makedirs(os.path.dirname(db_path), exist_ok=True)
    with get_connection() as conn:
        if os.path.exists(INIT_SQL_PATH):
            with open(INIT_SQL_PATH, "r") as f:
                conn.executescript(f.read())
        # migrations
        try:
            conn.execute("ALTER TABLE nodes ADD COLUMN policy_id INTEGER")
        except sqlite3.OperationalError:
            pass
        try:
            conn.execute("ALTER TABLE ssh_policies ADD COLUMN docker_config_json TEXT")
        except sqlite3.OperationalError:
            pass
        try:
            conn.execute("""
                CREATE TABLE credential_bindings (
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
                )
            """)
            conn.execute("CREATE INDEX idx_credential_bindings_node_id ON credential_bindings(node_id)")
            conn.execute("CREATE INDEX idx_credential_bindings_credential_id ON credential_bindings(credential_id)")
        except sqlite3.OperationalError:
            pass
        try:
            conn.execute("""
                CREATE TABLE github_settings (
                    id INTEGER PRIMARY KEY CHECK (id = 1),
                    token TEXT,
                    webhook_secret TEXT,
                    enabled INTEGER NOT NULL DEFAULT 0,
                    backup_repo TEXT,
                    updated_at TEXT NOT NULL
                )
            """)
        except sqlite3.OperationalError:
            pass
        try:
            conn.execute("ALTER TABLE github_settings ADD COLUMN backup_repo TEXT")
        except sqlite3.OperationalError:
            pass
        try:
            conn.execute("ALTER TABLE compose_projects ADD COLUMN auto_update INTEGER NOT NULL DEFAULT 1")
        except sqlite3.OperationalError:
            pass
        try:
            conn.execute("""
                CREATE TABLE github_repos (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    github_id INTEGER UNIQUE,
                    full_name TEXT NOT NULL UNIQUE,
                    clone_url TEXT,
                    private INTEGER NOT NULL DEFAULT 1,
                    default_branch TEXT,
                    updated_at TEXT NOT NULL
                )
            """)
            conn.execute("CREATE INDEX idx_github_repos_full_name ON github_repos(full_name)")
        except sqlite3.OperationalError:
            pass
        try:
            conn.execute("""
                CREATE TABLE node_github_bindings (
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
                )
            """)
            conn.execute("CREATE INDEX idx_node_github_bindings_node_id ON node_github_bindings(node_id)")
        except sqlite3.OperationalError:
            pass
        # Migrate old table: add compose_project_name and change unique constraint
        try:
            conn.execute("ALTER TABLE node_github_bindings ADD COLUMN compose_project_name TEXT")
        except sqlite3.OperationalError:
            pass
        try:
            cur = conn.cursor()
            cur.execute("SELECT sql FROM sqlite_master WHERE type='table' AND name='node_github_bindings'")
            row = cur.fetchone()
            if row and 'UNIQUE(node_id, repo_full_name)' in row[0]:
                conn.executescript("""
                    CREATE TABLE node_github_bindings_new (
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
                    INSERT INTO node_github_bindings_new (id, node_id, repo_full_name, deploy_key_id, branch, compose_file, project_path, auto_deploy, created_at, updated_at)
                        SELECT id, node_id, repo_full_name, deploy_key_id, branch, compose_file, project_path, auto_deploy, created_at, updated_at FROM node_github_bindings;
                    DROP TABLE node_github_bindings;
                    ALTER TABLE node_github_bindings_new RENAME TO node_github_bindings;
                    CREATE INDEX idx_node_github_bindings_node_id ON node_github_bindings(node_id);
                """)
        except Exception:
            pass
        try:
            conn.execute("""
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
                )
            """)
        except Exception:
            pass
        try:
            conn.execute("""
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
                )
            """)
        except Exception:
            pass
        try:
            conn.execute("""
                CREATE TABLE IF NOT EXISTS maintenance_logs (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    node_id INTEGER NOT NULL,
                    report_json TEXT,
                    warnings_json TEXT,
                    checked_at TEXT NOT NULL,
                    FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
                )
            """)
        except Exception:
            pass
        try:
            conn.execute("ALTER TABLE ssh_policies ADD COLUMN agent_config_json TEXT")
        except sqlite3.OperationalError:
            pass
