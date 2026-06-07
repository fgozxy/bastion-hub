import base64
import json
import os
from datetime import datetime, timezone
from typing import Optional, Dict, Any, List

import httpx

from app.models.repositories import (
    NodeRepository,
    NodeAddressRepository,
    ComposeProjectRepository,
    SSHPolicyRepository,
    CredentialBindingRepository,
    NodeGitHubBindingRepository,
)
from app.core.db import get_cursor

GITHUB_API_BASE = "https://api.github.com"


def _get_headers(token: str) -> dict:
    return {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def export_node_config(node_id: int) -> Dict[str, Any]:
    """收集单个节点的完整配置快照"""
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise ValueError(f"节点 {node_id} 不存在")

    # 脱敏：移除内部 token 等敏感字段
    safe_node = {
        k: v for k, v in node.items()
        if k not in ("uuid",)
    }
    safe_node["id"] = node["id"]
    safe_node["uuid"] = node["uuid"]

    addresses = NodeAddressRepository.get_by_node(node_id)
    compose_projects = ComposeProjectRepository.get_by_node(node_id)
    credential_bindings = CredentialBindingRepository.get_by_node(node_id)
    github_bindings = NodeGitHubBindingRepository.get_by_node(node_id)

    # 清理凭证绑定中的敏感信息
    safe_bindings = []
    for cb in credential_bindings:
        safe_bindings.append({
            "credential_id": cb.get("credential_id"),
            "credential_name": cb.get("credential_name"),
            "target_user": cb.get("target_user"),
            "fingerprint": cb.get("fingerprint"),
            "status": cb.get("status"),
            "applied_at": cb.get("applied_at"),
        })

    policy = None
    if node.get("policy_id"):
        p = SSHPolicyRepository.get_by_id(node["policy_id"])
        if p:
            policy = {
                "id": p["id"],
                "name": p["name"],
                "revision": p["revision"],
                "mode": p["mode"],
                "sshd_config": _try_json(p.get("sshd_config_json")),
                "firewall_config": _try_json(p.get("firewall_config_json")),
                "allowed_principals": _try_json(p.get("allowed_principals_json")),
                "allowed_source_cidrs": _try_json(p.get("allowed_source_cidrs_json")),
            }

    docker_snapshot = None
    with get_cursor() as cur:
        cur.execute(
            "SELECT * FROM docker_snapshots WHERE node_id = ? ORDER BY collected_at DESC LIMIT 1",
            (node_id,),
        )
        row = cur.fetchone()
        if row:
            docker_snapshot = dict(row)
            docker_snapshot.pop("raw_json", None)

    return {
        "node": safe_node,
        "addresses": [dict(a) for a in addresses],
        "policy": policy,
        "compose_projects": [dict(p) for p in compose_projects],
        "credential_bindings": safe_bindings,
        "github_bindings": [dict(g) for g in github_bindings],
        "docker_snapshot": docker_snapshot,
        "exported_at": now_iso(),
    }


def export_all_nodes() -> List[Dict[str, Any]]:
    nodes = NodeRepository.list_all()
    results = []
    for node in nodes:
        try:
            results.append(export_node_config(node["id"]))
        except Exception as e:
            results.append({
                "node_id": node["id"],
                "hostname": node.get("hostname"),
                "error": str(e),
                "exported_at": now_iso(),
            })
    return results


def _try_json(val):
    if val is None:
        return None
    if isinstance(val, str):
        try:
            return json.loads(val)
        except Exception:
            return val
    return val


async def _get_file_sha(owner: str, repo: str, path: str, token: str) -> Optional[str]:
    async with httpx.AsyncClient() as client:
        resp = await client.get(
            f"{GITHUB_API_BASE}/repos/{owner}/{repo}/contents/{path}",
            headers=_get_headers(token),
            timeout=30,
        )
        if resp.status_code == 200:
            data = resp.json()
            return data.get("sha")
        return None


async def push_file(
    repo_full_name: str,
    token: str,
    path: str,
    content: str,
    message: str,
) -> Dict[str, Any]:
    """推送单个文件到 GitHub 仓库，支持新建和更新"""
    owner, repo = repo_full_name.split("/", 1)
    sha = await _get_file_sha(owner, repo, path, token)

    payload = {
        "message": message,
        "content": base64.b64encode(content.encode("utf-8")).decode("ascii"),
    }
    if sha:
        payload["sha"] = sha

    async with httpx.AsyncClient() as client:
        resp = await client.put(
            f"{GITHUB_API_BASE}/repos/{owner}/{repo}/contents/{path}",
            headers=_get_headers(token),
            json=payload,
            timeout=30,
        )
        if resp.status_code not in (200, 201):
            detail = resp.text
            try:
                detail = resp.json().get("message", resp.text)
            except Exception:
                pass
            raise RuntimeError(f"GitHub API 错误 ({resp.status_code}): {detail}")
        return resp.json()


async def run_backup(token: str, backup_repo: str, node_ids: Optional[List[int]] = None) -> Dict[str, Any]:
    """执行备份，每个节点一个 JSON 文件。可指定节点列表，默认全部。"""
    if not backup_repo or "/" not in backup_repo:
        raise ValueError("backup_repo 格式错误，应为 owner/repo")

    if node_ids is not None:
        nodes = [NodeRepository.get_by_id(nid) for nid in node_ids]
        nodes = [n for n in nodes if n]
    else:
        nodes = NodeRepository.list_all()

    if not nodes:
        return {"ok": True, "backed_up": 0, "message": "没有节点需要备份"}

    timestamp = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    errors = []
    pushed = 0

    for node in nodes:
        node_id = node["id"]
        hostname = node.get("hostname") or f"node-{node_id}"
        try:
            config = export_node_config(node_id)
            content = json.dumps(config, ensure_ascii=False, indent=2)
            path = f"nodes/{hostname}.json"
            await push_file(
                repo_full_name=backup_repo,
                token=token,
                path=path,
                content=content,
                message=f"backup({hostname}): auto backup from Bastion Hub at {timestamp}",
            )
            pushed += 1
        except Exception as e:
            errors.append({"node_id": node_id, "hostname": hostname, "error": str(e)})

    # 同时推送一个汇总文件
    try:
        summary = {
            "backup_time": now_iso(),
            "total_nodes": len(nodes),
            "backed_up": pushed,
            "errors": errors,
            "nodes": [
                {
                    "id": n["id"],
                    "hostname": n.get("hostname"),
                    "display_name": n.get("display_name"),
                    "status": n.get("status"),
                }
                for n in nodes
            ],
        }
        await push_file(
            repo_full_name=backup_repo,
            token=token,
            path="nodes/README.json",
            content=json.dumps(summary, ensure_ascii=False, indent=2),
            message=f"backup(summary): auto backup summary at {timestamp}",
        )
    except Exception as e:
        errors.append({"node_id": None, "hostname": "summary", "error": str(e)})

    return {
        "ok": len(errors) == 0,
        "backed_up": pushed,
        "total": len(nodes),
        "errors": errors,
        "repo": backup_repo,
    }
