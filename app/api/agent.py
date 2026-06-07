import json
import hashlib
from pathlib import Path
from datetime import datetime, timezone
from typing import List, Optional
from fastapi import APIRouter, Request, Header, HTTPException, Depends
from fastapi.responses import FileResponse
from pydantic import BaseModel, Field, AliasChoices

from app.core.config import get_settings
from app.core.security import generate_node_token, hash_token
from app.models.repositories import (
    NodeRepository, NodeAddressRepository, NodeTokenRepository,
    EnrollmentTokenRepository, AuditRepository, JobRepository,
    SSHPolicyRepository,
)

router = APIRouter()


class AgentRegisterRequest(BaseModel):
    enrollment_token: str
    hostname: str
    role: str = "worker"
    env: str = "prod"
    addresses: List[dict] = Field(default_factory=list)
    ssh_port: int = 22
    agent_version: Optional[str] = None


class AgentHeartbeatRequest(BaseModel):
    hostname: str
    addresses: List[dict] = Field(default_factory=list)
    agent_version: Optional[str] = None
    applied_policy_revision: int = 0


class AgentComposeProject(BaseModel):
    name: str = Field(validation_alias=AliasChoices("Name", "name"))
    status: Optional[str] = Field(default=None, validation_alias=AliasChoices("Status", "status"))
    config_files: Optional[str] = Field(default=None, validation_alias=AliasChoices("ConfigFiles", "config_files"))
    git_url: Optional[str] = Field(default=None, validation_alias=AliasChoices("GitUrl", "git_url"))
    git_branch: Optional[str] = Field(default=None, validation_alias=AliasChoices("GitBranch", "git_branch"))
    current_revision: Optional[str] = Field(default=None, validation_alias=AliasChoices("CurrentRevision", "current_revision"))


class AgentDockerSnapshotRequest(BaseModel):
    docker_available: bool
    docker_version: Optional[str] = None
    compose_version: Optional[str] = None
    containers_running: int = 0
    containers_total: int = 0
    images_total: int = 0
    networks_total: int = 0
    volumes_total: int = 0
    compose_projects: List[AgentComposeProject] = Field(default_factory=list)
    raw_json: Optional[str] = None


def get_current_node(request: Request):
    auth = request.headers.get("authorization", "")
    if not auth.startswith("Bearer "):
        raise HTTPException(status_code=401, detail="缺少节点令牌")
    token = auth[7:]
    rec = NodeTokenRepository.validate(token)
    if not rec:
        raise HTTPException(status_code=401, detail="节点令牌无效或已吊销")
    node = NodeRepository.get_by_id(rec["node_id"])
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    return {"node": node, "token": token}


@router.post("/api/agent/register")
async def agent_register(data: AgentRegisterRequest):
    token_rec = EnrollmentTokenRepository.get_valid(data.enrollment_token)
    if not token_rec:
        raise HTTPException(status_code=400, detail="注册令牌无效或已过期")

    # Find existing node by token association or hostname
    node_id = token_rec.get("used_by_node_id")
    node = None
    if node_id:
        node = NodeRepository.get_by_id(node_id)
    if not node:
        # Fallback: find pending node with matching hostname
        from app.core.db import get_cursor
        with get_cursor() as cur:
            cur.execute("SELECT * FROM nodes WHERE hostname = ? AND status = 'pending' ORDER BY id DESC LIMIT 1", (data.hostname,))
            row = cur.fetchone()
            if row:
                node = dict(row)
                node_id = node["id"]
    if not node:
        node_id = NodeRepository.create(
            hostname=data.hostname,
            role=token_rec["role"],
            env=token_rec["env"],
            ssh_port=data.ssh_port,
        )
        import uuid as uuid_mod
        NodeRepository.update(node_id, uuid=str(uuid_mod.uuid4()))
        node = NodeRepository.get_by_id(node_id)

    NodeRepository.update(node_id, status="online")
    node = NodeRepository.get_by_id(node_id)

    EnrollmentTokenRepository.consume(data.enrollment_token, node_id)

    # Create node token
    node_token = generate_node_token()
    NodeTokenRepository.create(node_id, node_token)

    # Save addresses
    for addr in data.addresses:
        NodeAddressRepository.upsert(
            node_id=node_id,
            family=addr.get("family", "ipv4"),
            scope=addr.get("scope", "public"),
            address=addr.get("address"),
            source=addr.get("source", "agent"),
            interface=addr.get("interface"),
            is_primary=addr.get("is_primary", False),
        )

    AuditRepository.log(
        actor_type="node",
        actor_id=node["uuid"],
        action="agent.register",
        target_type="node",
        target_id=str(node_id),
        summary=f"Node {data.hostname} registered",
    )
    JobRepository.create(kind="asset.sync", payload={"node_id": node_id}, priority=50)

    return {"node_token": node_token, "node_uuid": node["uuid"]}


@router.post("/api/agent/heartbeat")
async def agent_heartbeat(
    data: AgentHeartbeatRequest,
    request: Request,
    node_ctx: dict = Depends(get_current_node),
):
    node = node_ctx["node"]
    node_id = node["id"]
    t = datetime.now(timezone.utc).isoformat()

    NodeRepository.update(
        node_id,
        hostname=data.hostname,
        agent_version=data.agent_version,
        applied_policy_revision=data.applied_policy_revision,
        last_seen_at=t,
        status="online",
    )

    # Reconcile addresses from this source
    keep = []
    for addr in data.addresses:
        NodeAddressRepository.upsert(
            node_id=node_id,
            family=addr.get("family", "ipv4"),
            scope=addr.get("scope", "public"),
            address=addr.get("address"),
            source=addr.get("source", "agent"),
            interface=addr.get("interface"),
            is_primary=addr.get("is_primary", False),
        )
        keep.append(addr.get("address"))

    # Cleanup old agent addresses
    NodeAddressRepository.delete_old(node_id, "agent", keep)

    # Include agent_config in heartbeat so the agent can self-update without extra policy call
    agent_config = {}
    if node.get("policy_id"):
        policy = SSHPolicyRepository.get_by_id(node["policy_id"])
        if policy and policy.get("agent_config_json"):
            try:
                agent_config = json.loads(policy["agent_config_json"])
            except Exception:
                pass

    return {"ok": True, "desired_policy_revision": node["desired_policy_revision"], "agent_config": agent_config}


@router.post("/api/agent/docker-snapshot")
async def agent_docker_snapshot(
    data: AgentDockerSnapshotRequest,
    node_ctx: dict = Depends(get_current_node),
):
    node = node_ctx["node"]
    node_id = node["id"]
    t = datetime.now(timezone.utc).isoformat()

    # Store snapshot (simple insert for history)
    from app.core.db import get_cursor
    with get_cursor() as cur:
        cur.execute(
            """
            INSERT INTO docker_snapshots (node_id, docker_available, docker_version, compose_version,
                containers_running, containers_total, images_total, networks_total, volumes_total,
                raw_json, collected_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                node_id, int(data.docker_available), data.docker_version, data.compose_version,
                data.containers_running, data.containers_total, data.images_total,
                data.networks_total, data.volumes_total, data.raw_json, t,
            ),
        )

    # Store compose projects (only if non-empty to avoid wiping data from partial agents)
    if data.compose_projects:
        from app.models.repositories import ComposeProjectRepository
        import json as _json
        keep_names = []
        for proj in data.compose_projects:
            ComposeProjectRepository.upsert(
                node_id=node_id,
                name=proj.name,
                project_path=proj.config_files,
                status=proj.status,
            )
            keep_names.append(proj.name)
        ComposeProjectRepository.delete_old(node_id, keep_names)

    return {"ok": True}


@router.get("/api/agent/policy")
async def agent_policy(node_ctx: dict = Depends(get_current_node)):
    node = node_ctx["node"]
    policy_id = node.get("policy_id")
    if policy_id:
        policy = SSHPolicyRepository.get_by_id(policy_id)
        if policy:
            import json as _json
            return {
                "revision": policy["revision"],
                "mode": policy["mode"],
                "trusted_user_ca_public_key": policy.get("trusted_user_ca_public_key"),
                "allowed_principals": _json.loads(policy.get("allowed_principals_json") or "[]"),
                "allowed_source_cidrs": _json.loads(policy.get("allowed_source_cidrs_json") or "[]"),
                "sshd_config": _json.loads(policy.get("sshd_config_json") or "{}"),
                "firewall_config": _json.loads(policy.get("firewall_config_json") or "{}"),
                "docker_config": _json.loads(policy.get("docker_config_json") or "{}"),
                "agent_config": _json.loads(policy.get("agent_config_json") or "{}"),
            }
    return {
        "revision": node["desired_policy_revision"],
        "mode": "report",
        "sshd_config": {},
        "firewall_config": {},
        "docker_config": {},
    }


@router.post("/api/agent/policy-result")
async def agent_policy_result(result: dict, node_ctx: dict = Depends(get_current_node)):
    node = node_ctx["node"]
    applied_rev = result.get("applied_revision")
    if applied_rev is not None:
        NodeRepository.update(node["id"], applied_policy_revision=applied_rev)
    return {"ok": True}


@router.get("/api/agent/assets")
async def agent_assets(node_ctx: dict = Depends(get_current_node)):
    # Return any assets the node might need (e.g., CA public keys)
    return {"assets": []}


@router.get("/api/agent/assets/{filename}")
async def agent_asset(filename: str, node_ctx: dict = Depends(get_current_node)):
    allowed = {"agent.sh", "policy-apply.sh", "maintenance.sh"}
    if filename not in allowed:
        raise HTTPException(status_code=404, detail="Asset not found")
    asset_path = Path(__file__).resolve().parent.parent.parent / "agent" / filename
    if not asset_path.exists():
        raise HTTPException(status_code=404, detail="Asset not found")
    return FileResponse(asset_path, media_type="text/plain", filename=filename)


@router.get("/api/agent/assets-checksums")
async def agent_assets_checksums(node_ctx: dict = Depends(get_current_node)):
    """Return sha256 checksums of current agent scripts for policy authoring."""
    agent_dir = Path(__file__).resolve().parent.parent.parent / "agent"
    checksums = {}
    for filename in ("agent.sh", "policy-apply.sh", "maintenance.sh"):
        fpath = agent_dir / filename
        if fpath.exists():
            h = hashlib.sha256()
            h.update(fpath.read_bytes())
            checksums[filename] = f"sha256:{h.hexdigest()}"
    return {"checksums": checksums}


@router.post("/api/agent/maintenance")
async def agent_maintenance(data: dict, node_ctx: dict = Depends(get_current_node)):
    node = node_ctx["node"]
    from app.models.repositories import MaintenanceLogRepository
    MaintenanceLogRepository.create(
        node_id=node["id"],
        report=data.get("report"),
        warnings=data.get("warnings"),
        checked_at=data.get("checked_at"),
    )
    return {"ok": True}
