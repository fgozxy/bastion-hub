import hmac
import hashlib
import json
from typing import Optional, List
from fastapi import APIRouter, HTTPException, Depends, Request, Body
from pydantic import BaseModel, Field
import httpx

from app.api.auth import get_current_admin
from app.models.repositories import (
    GitHubSettingsRepository, GitHubRepoRepository,
    NodeGitHubBindingRepository, NodeRepository,
    AuditRepository, JobRepository,
)

router = APIRouter()
GITHUB_API_BASE = "https://api.github.com"


def _get_headers(token: str) -> dict:
    return {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }


async def github_api_get(path: str, token: str):
    async with httpx.AsyncClient() as client:
        resp = await client.get(f"{GITHUB_API_BASE}{path}", headers=_get_headers(token), timeout=30)
        resp.raise_for_status()
        return resp.json()


async def github_api_post(path: str, token: str, payload: dict):
    async with httpx.AsyncClient() as client:
        resp = await client.post(f"{GITHUB_API_BASE}{path}", headers=_get_headers(token), json=payload, timeout=30)
        resp.raise_for_status()
        return resp.json()


async def github_api_delete(path: str, token: str):
    async with httpx.AsyncClient() as client:
        resp = await client.delete(f"{GITHUB_API_BASE}{path}", headers=_get_headers(token), timeout=30)
        if resp.status_code not in (204, 404):
            resp.raise_for_status()
        return resp.status_code == 204


# ------------------------------------------------------------------
# Settings
# ------------------------------------------------------------------

class GitHubSettingsUpdate(BaseModel):
    token: Optional[str] = None
    webhook_secret: Optional[str] = None
    enabled: Optional[bool] = None
    backup_repo: Optional[str] = None


@router.get("/api/github/settings")
async def get_github_settings(admin=Depends(get_current_admin)):
    settings = GitHubSettingsRepository.get()
    if not settings:
        return {"configured": False}
    # Mask token
    token = settings.get("token") or ""
    masked = ""
    if len(token) > 12:
        masked = token[:4] + "****" + token[-4:]
    return {
        "configured": bool(settings.get("enabled")),
        "token_masked": masked,
        "webhook_secret_configured": bool(settings.get("webhook_secret")),
        "backup_repo": settings.get("backup_repo"),
        "updated_at": settings.get("updated_at"),
    }


@router.post("/api/github/settings")
async def update_github_settings(data: GitHubSettingsUpdate, admin=Depends(get_current_admin)):
    updates = {}
    if data.token is not None:
        updates["token"] = data.token
    if data.webhook_secret is not None:
        updates["webhook_secret"] = data.webhook_secret
    if data.enabled is not None:
        updates["enabled"] = int(data.enabled)
    if data.backup_repo is not None:
        updates["backup_repo"] = data.backup_repo
    GitHubSettingsRepository.update(**updates)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="github.settings.update",
        target_type="github",
        target_id="1",
        summary="Updated GitHub settings",
    )
    return {"ok": True}


@router.post("/api/github/test")
async def test_github_connection(admin=Depends(get_current_admin)):
    settings = GitHubSettingsRepository.get()
    if not settings or not settings.get("token"):
        raise HTTPException(status_code=400, detail="GitHub Token 未配置")
    try:
        user = await github_api_get("/user", settings["token"])
        return {"ok": True, "login": user.get("login"), "type": user.get("type")}
    except Exception as e:
        raise HTTPException(status_code=400, detail=f"连接失败: {e}")


# ------------------------------------------------------------------
# Repos
# ------------------------------------------------------------------

@router.get("/api/github/repos")
async def list_github_repos(admin=Depends(get_current_admin)):
    settings = GitHubSettingsRepository.get()
    if not settings or not settings.get("token"):
        raise HTTPException(status_code=400, detail="GitHub Token 未配置")
    # Return cached repos first
    cached = GitHubRepoRepository.list_all()
    if cached:
        return {"repos": cached, "source": "cache"}
    # Fallback: fetch from API
    try:
        repos = await github_api_get("/user/repos?per_page=100&sort=updated", settings["token"])
        for r in repos:
            GitHubRepoRepository.upsert(
                github_id=r.get("id"),
                full_name=r.get("full_name"),
                clone_url=r.get("clone_url"),
                private=r.get("private", True),
                default_branch=r.get("default_branch"),
            )
        return {"repos": GitHubRepoRepository.list_all(), "source": "api"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=f"获取仓库列表失败: {e}")


@router.post("/api/github/sync-repos")
async def sync_github_repos(admin=Depends(get_current_admin)):
    settings = GitHubSettingsRepository.get()
    if not settings or not settings.get("token"):
        raise HTTPException(status_code=400, detail="GitHub Token 未配置")
    try:
        repos = await github_api_get("/user/repos?per_page=100&sort=updated", settings["token"])
        GitHubRepoRepository.clear_all()
        for r in repos:
            GitHubRepoRepository.upsert(
                github_id=r.get("id"),
                full_name=r.get("full_name"),
                clone_url=r.get("clone_url"),
                private=r.get("private", True),
                default_branch=r.get("default_branch"),
            )
        return {"ok": True, "count": len(repos)}
    except Exception as e:
        raise HTTPException(status_code=400, detail=f"同步失败: {e}")


# ------------------------------------------------------------------
# Node bindings
# ------------------------------------------------------------------

class BindGitHubRepo(BaseModel):
    repo_full_name: str
    branch: str = "main"
    compose_file: str = "docker-compose.yml"
    project_path: Optional[str] = None
    auto_deploy: bool = True
    compose_project_name: Optional[str] = None


@router.post("/api/nodes/{node_id}/bind-github-repo")
async def bind_github_repo(node_id: int, data: BindGitHubRepo, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    settings = GitHubSettingsRepository.get()
    if not settings or not settings.get("token"):
        raise HTTPException(status_code=400, detail="GitHub Token 未配置")

    # Auto-fill project_path from compose project if provided
    project_path = data.project_path
    compose_file = data.compose_file
    if data.compose_project_name and not project_path:
        from app.models.repositories import ComposeProjectRepository
        compose_projects = ComposeProjectRepository.get_by_node(node_id)
        for proj in compose_projects:
            if proj.get("name") == data.compose_project_name:
                raw_path = proj.get("project_path", "")
                # project_path from agent may be a file path like /path/to/docker-compose.yml
                # We need the directory for cd and the filename for -f
                if raw_path.endswith((".yml", ".yaml")):
                    import os
                    compose_file = os.path.basename(raw_path)
                    project_path = os.path.dirname(raw_path)
                else:
                    project_path = raw_path
                break
        if not project_path:
            raise HTTPException(status_code=400, detail=f"节点上没有名为 '{data.compose_project_name}' 的 Compose 项目")

    # Try to add deploy key using node's SSH public key if available
    deploy_key_id = None
    try:
        import os
        ssh_pub_path = os.environ.get("SSH_KEY_PATH", "/data/ssh/bastion-hub") + ".pub"
        if os.path.exists(ssh_pub_path):
            with open(ssh_pub_path, "r") as f:
                pub_key = f.read().strip()
            if pub_key:
                owner, repo = data.repo_full_name.split("/", 1)
                dk_resp = await github_api_post(
                    f"/repos/{owner}/{repo}/keys",
                    settings["token"],
                    {"title": f"bastion-hub-{node.get('hostname') or node_id}", "key": pub_key, "read_only": False},
                )
                deploy_key_id = dk_resp.get("id")
    except Exception:
        pass  # Non-fatal: user can manually add deploy key

    binding_id = NodeGitHubBindingRepository.create(
        node_id=node_id,
        repo_full_name=data.repo_full_name,
        deploy_key_id=deploy_key_id,
        branch=data.branch,
        compose_file=compose_file,
        project_path=project_path,
        auto_deploy=data.auto_deploy,
        compose_project_name=data.compose_project_name,
    )
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="github.bind",
        target_type="node",
        target_id=str(node_id),
        summary=f"Bound {data.repo_full_name} to node {node.get('hostname')} project {data.compose_project_name}",
    )
    return {"ok": True, "binding_id": binding_id, "deploy_key_added": deploy_key_id is not None, "project_path": project_path}


@router.post("/api/nodes/{node_id}/unbind-github-repo")
async def unbind_github_repo(node_id: int, data: dict, admin=Depends(get_current_admin)):
    binding_id = data.get("binding_id")
    binding = NodeGitHubBindingRepository.get_by_id(binding_id)
    if not binding or binding["node_id"] != node_id:
        raise HTTPException(status_code=404, detail="绑定关系不存在")

    # Try to delete deploy key from GitHub
    settings = GitHubSettingsRepository.get()
    if settings and settings.get("token") and binding.get("deploy_key_id"):
        try:
            owner, repo = binding["repo_full_name"].split("/", 1)
            await github_api_delete(f"/repos/{owner}/{repo}/keys/{binding['deploy_key_id']}", settings["token"])
        except Exception:
            pass

    NodeGitHubBindingRepository.delete(binding_id)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="github.unbind",
        target_type="node",
        target_id=str(node_id),
        summary=f"Unbound {binding['repo_full_name']} from node",
    )
    return {"ok": True}


@router.get("/api/nodes/{node_id}/github-bindings")
async def list_node_github_bindings(node_id: int, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    bindings = NodeGitHubBindingRepository.get_by_node(node_id)
    return {"bindings": bindings}


@router.post("/api/nodes/{node_id}/deploy")
async def deploy_node(node_id: int, data: Optional[dict] = None, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")

    binding_id = data.get("binding_id") if data else None
    compose_project_name = data.get("compose_project_name") if data else None

    if binding_id:
        binding = NodeGitHubBindingRepository.get_by_id(binding_id)
        if not binding or binding["node_id"] != node_id:
            raise HTTPException(status_code=404, detail="绑定关系不存在")
        payload = {"node_id": node_id, "binding_id": binding_id}
    elif compose_project_name:
        # Deploy without GitHub binding
        from app.models.repositories import ComposeProjectRepository
        projects = ComposeProjectRepository.get_by_node(node_id)
        project = next((p for p in projects if p.get("name") == compose_project_name), None)
        if not project:
            raise HTTPException(status_code=404, detail="Compose 项目不存在")
        payload = {"node_id": node_id, "compose_project_name": compose_project_name}
    else:
        # Deploy all bindings
        bindings = NodeGitHubBindingRepository.get_by_node(node_id)
        if not bindings:
            raise HTTPException(status_code=400, detail="该节点没有绑定任何仓库")
        payload = {"node_id": node_id, "binding_id": bindings[0]["id"]}

    job_id = JobRepository.create(kind="github.deploy", payload=payload, priority=30)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="github.deploy.trigger",
        target_type="node",
        target_id=str(node_id),
        summary=f"Triggered deploy for node {node.get('hostname')}",
    )
    return {"ok": True, "job_id": job_id}


# ------------------------------------------------------------------
# Webhook
# ------------------------------------------------------------------

@router.post("/api/github/webhook")
async def github_webhook(request: Request):
    body = await request.body()
    settings = GitHubSettingsRepository.get()
    if settings and settings.get("webhook_secret"):
        signature = request.headers.get("x-hub-signature-256", "")
        expected = "sha256=" + hmac.new(
            settings["webhook_secret"].encode(),
            body,
            hashlib.sha256,
        ).hexdigest()
        if not hmac.compare_digest(signature, expected):
            raise HTTPException(status_code=403, detail="Invalid signature")

    event_type = request.headers.get("x-github-event", "")
    if event_type != "push":
        return {"ok": True, "message": f"Ignored event: {event_type}"}

    payload = json.loads(body)
    repo_full_name = payload.get("repository", {}).get("full_name")
    ref = payload.get("ref", "")
    after_sha = payload.get("after", "")

    if not repo_full_name:
        raise HTTPException(status_code=400, detail="Missing repository info")

    branch = ref.replace("refs/heads/", "")
    bindings = NodeGitHubBindingRepository.get_by_repo(repo_full_name)
    triggered = 0
    for binding in bindings:
        if not binding.get("auto_deploy"):
            continue
        if binding.get("branch") and binding["branch"] != branch:
            continue
        JobRepository.create(
            kind="github.deploy",
            payload={
                "node_id": binding["node_id"],
                "binding_id": binding["id"],
                "repo_full_name": repo_full_name,
                "branch": branch,
                "commit": after_sha,
            },
            priority=20,
        )
        triggered += 1

    return {"ok": True, "triggered": triggered, "repo": repo_full_name, "branch": branch}


# ------------------------------------------------------------------
# Backup
# ------------------------------------------------------------------

class BackupRequest(BaseModel):
    backup_repo: Optional[str] = None


class BatchNodeBackup(BaseModel):
    node_ids: List[int]
    backup_repo: Optional[str] = None


async def _do_backup(backup_repo: Optional[str], node_ids: Optional[List[int]] = None) -> dict:
    settings = GitHubSettingsRepository.get()
    if not settings or not settings.get("token"):
        raise HTTPException(status_code=400, detail="GitHub Token 未配置")
    target_repo = backup_repo or settings.get("backup_repo")
    if not target_repo:
        raise HTTPException(status_code=400, detail="未配置备份目标仓库")

    from app.services.backup import run_backup
    return await run_backup(token=settings["token"], backup_repo=target_repo, node_ids=node_ids)


@router.post("/api/github/backup")
async def trigger_backup(data: BackupRequest = Body(default=BackupRequest()), admin=Depends(get_current_admin)):
    try:
        result = await _do_backup(backup_repo=data.backup_repo, node_ids=None)
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="github.backup",
            target_type="github",
            target_id="1",
            summary=f"Backup to {result['repo']}: {result.get('backed_up')}/{result.get('total')} nodes",
        )
        return result
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"备份失败: {e}")


@router.post("/api/github/backup-node/{node_id}")
async def trigger_node_backup(node_id: int, data: BackupRequest = Body(default=BackupRequest()), admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")

    try:
        result = await _do_backup(backup_repo=data.backup_repo, node_ids=[node_id])
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="github.backup.node",
            target_type="node",
            target_id=str(node_id),
            summary=f"Backup node {node.get('hostname')} to {result['repo']}",
        )
        return result
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"备份失败: {e}")


@router.post("/api/github/batch-backup")
async def trigger_batch_backup(data: BatchNodeBackup, admin=Depends(get_current_admin)):
    try:
        result = await _do_backup(backup_repo=data.backup_repo, node_ids=data.node_ids)
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="github.backup.batch",
            target_type="github",
            target_id="1",
            summary=f"Batch backup {result.get('backed_up')}/{result.get('total')} nodes to {result['repo']}",
        )
        return result
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"备份失败: {e}")


# ------------------------------------------------------------------
# Binding update & batch ops
# ------------------------------------------------------------------

class BindingUpdate(BaseModel):
    repo_full_name: Optional[str] = None
    branch: Optional[str] = None
    compose_file: Optional[str] = None
    project_path: Optional[str] = None
    auto_deploy: Optional[bool] = None


class BatchBindingIds(BaseModel):
    binding_ids: List[int]
    auto_deploy: bool


@router.patch("/api/nodes/{node_id}/github-bindings/{binding_id}")
async def update_binding(node_id: int, binding_id: int, data: BindingUpdate, admin=Depends(get_current_admin)):
    binding = NodeGitHubBindingRepository.get_by_id(binding_id)
    if not binding or binding["node_id"] != node_id:
        raise HTTPException(status_code=404, detail="绑定关系不存在")
    updates = data.model_dump(exclude_unset=True)
    if not updates:
        raise HTTPException(status_code=400, detail="没有要更新的字段")
    NodeGitHubBindingRepository.update(binding_id, **updates)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="github.binding.update",
        target_type="node",
        target_id=str(node_id),
        summary=f"Updated binding {binding_id} for project {binding.get('compose_project_name')}",
    )
    return {"ok": True}


@router.post("/api/nodes/{node_id}/github-bindings/batch-auto-deploy")
async def batch_auto_deploy(node_id: int, data: BatchBindingIds, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    updated = 0
    for bid in data.binding_ids:
        binding = NodeGitHubBindingRepository.get_by_id(bid)
        if binding and binding["node_id"] == node_id:
            NodeGitHubBindingRepository.update(bid, auto_deploy=data.auto_deploy)
            updated += 1
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="github.binding.batch_auto_deploy",
        target_type="node",
        target_id=str(node_id),
        summary=f"Batch set auto_deploy={data.auto_deploy} for {updated} bindings",
    )
    return {"ok": True, "updated": updated}
