import uuid
import os
import json
import asyncio
from datetime import datetime, timedelta, timezone
from typing import Optional, List
from fastapi import APIRouter, Request, Form, HTTPException, Depends, Query
from pydantic import BaseModel, Field

from app.api.auth import get_current_admin
from app.core.config import get_settings
from app.core.security import generate_enrollment_token, hash_token
from app.models.repositories import (
    NodeRepository, NodeAddressRepository, EnrollmentTokenRepository,
    AuditRepository, JobRepository, ComposeProjectRepository,
)


SSH_KEY_PATH = os.environ.get("SSH_KEY_PATH", "/data/ssh/bastion-hub")
SSH_OPTS = [
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "ConnectTimeout=10",
    "-o", "BatchMode=yes",
]


async def ssh_exec(host: str, port: int, user: str, command: str) -> str:
    proc = await asyncio.create_subprocess_exec(
        "ssh", *SSH_OPTS, "-p", str(port), "-i", SSH_KEY_PATH,
        f"{user}@{host}", command,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=30)
    if proc.returncode != 0:
        raise RuntimeError(stderr.decode().strip() or "SSH command failed")
    return stdout.decode()


def is_private_ipv4(addr: str) -> bool:
    return bool(addr.startswith(("10.", "172.16.", "172.17.", "172.18.", "172.19.", "172.20.",
                                 "172.21.", "172.22.", "172.23.", "172.24.", "172.25.",
                                 "172.26.", "172.27.", "172.28.", "172.29.", "172.30.",
                                 "172.31.", "192.168.", "127.", "169.254.")))


def is_private_ipv6(addr: str) -> bool:
    return bool(addr.startswith(("fc", "fd", "fe80:", "::1")))


router = APIRouter()


class NodeCreate(BaseModel):
    hostname: str
    display_name: Optional[str] = None
    role: str = "worker"
    env: str = "prod"
    provider: Optional[str] = None
    region: Optional[str] = None
    ssh_user: str = "root"
    ssh_port: int = 22


class NodeUpdate(BaseModel):
    hostname: Optional[str] = None
    display_name: Optional[str] = None
    role: Optional[str] = None
    env: Optional[str] = None
    provider: Optional[str] = None
    region: Optional[str] = None
    ssh_user: Optional[str] = None
    ssh_port: Optional[int] = None
    status: Optional[str] = None


class BatchNodeIds(BaseModel):
    node_ids: List[int]


class BatchApplyPolicy(BaseModel):
    node_ids: List[int]
    policy_id: int


class BatchUpdate(BaseModel):
    node_ids: List[int]
    role: Optional[str] = None
    env: Optional[str] = None
    ssh_port: Optional[int] = None
    status: Optional[str] = None


@router.get("/api/nodes")
async def list_nodes(admin=Depends(get_current_admin)):
    nodes = NodeRepository.list_all()
    for node in nodes:
        node["addresses"] = NodeAddressRepository.get_by_node(node["id"])
    return {"nodes": nodes}


@router.post("/api/nodes")
async def create_node(data: NodeCreate, admin=Depends(get_current_admin)):
    node_id = NodeRepository.create(
        hostname=data.hostname,
        display_name=data.display_name,
        role=data.role,
        env=data.env,
        provider=data.provider,
        region=data.region,
        ssh_user=data.ssh_user,
        ssh_port=data.ssh_port,
    )
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.create",
        target_type="node",
        target_id=str(node_id),
        summary=f"Created node {data.hostname} ({data.role}/{data.env})",
    )
    JobRepository.create(kind="asset.sync", payload={"node_id": node_id}, priority=50)
    return {"id": node_id}


@router.get("/api/nodes/{node_id}")
async def get_node(node_id: int, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    node["addresses"] = NodeAddressRepository.get_by_node(node_id)
    return node


@router.patch("/api/nodes/{node_id}")
async def update_node(node_id: int, data: NodeUpdate, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    updated = data.model_dump(exclude_unset=True)
    if updated:
        NodeRepository.update(node_id, **updated)
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="node.update",
            target_type="node",
            target_id=str(node_id),
            summary=f"Updated node {node_id}: {updated}",
        )
    return {"ok": True}


@router.delete("/api/nodes/{node_id}")
async def delete_node(node_id: int, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    NodeRepository.delete(node_id)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.delete",
        target_type="node",
        target_id=str(node_id),
        summary=f"Deleted node {node.get('hostname')}",
    )
    JobRepository.create(kind="asset.sync", payload={"node_id": None}, priority=50)
    return {"ok": True}


class BootstrapRequest(BaseModel):
    profile: str = "minimal,docker"
    token_ttl_minutes: int = Field(default=30, ge=5, le=1440)


@router.post("/api/nodes/{node_id}/bootstrap")
async def generate_bootstrap(node_id: int, data: BootstrapRequest, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")

    token = generate_enrollment_token()
    expires = (datetime.now(timezone.utc) + timedelta(minutes=data.token_ttl_minutes)).isoformat()
    EnrollmentTokenRepository.create(
        token=token,
        role=node["role"],
        env=node["env"],
        hostname_pattern=node["hostname"],
        expires_at=expires,
        node_id=node_id,
    )

    settings = get_settings()
    base_url = settings.panel_base_url.rstrip("/")

    cmd = f"""curl -fsSL {base_url}/assets/bootstrap.sh -o bootstrap.sh && chmod +x bootstrap.sh && \\
BOOTSTRAP_PANEL_BASE_URL="{base_url}" \\
BOOTSTRAP_ENROLL_TOKEN="{token}" \\
BOOTSTRAP_HOSTNAME="{node['hostname']}" \\
BOOTSTRAP_ROLE="{node['role']}" \\
BOOTSTRAP_ENV="{node['env']}" \\
BOOTSTRAP_PROFILE="{data.profile}" \\
./bootstrap.sh"""

    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.bootstrap.generate",
        target_type="node",
        target_id=str(node_id),
        summary=f"Generated bootstrap token for node {node['hostname']} profile={data.profile}",
    )
    return {"command": cmd, "token": token, "expires_at": expires}


@router.post("/api/nodes/{node_id}/refresh-addresses")
async def refresh_addresses(node_id: int, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")

    addresses = NodeAddressRepository.get_by_node(node_id)
    pub4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "public"]
    pri4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "private"]
    target_host = pub4[0] if pub4 else (pri4[0] if pri4 else None)

    refreshed = False
    if target_host and os.path.exists(SSH_KEY_PATH):
        try:
            # 1. SSH 获取本地地址
            ip_json = await ssh_exec(target_host, node.get("ssh_port", 22), node.get("ssh_user", "root"), "ip -json addr show")
            local_addrs = json.loads(ip_json)

            for iface in local_addrs:
                ifname = iface.get("ifname", "")
                if ifname.startswith(("lo", "docker0", "br-", "veth")):
                    continue
                for info in iface.get("addr_info", []):
                    if info.get("scope") == "host":
                        continue
                    family = "ipv4" if info.get("family") == "inet" else "ipv6"
                    raw_scope = info.get("scope", "")
                    addr = info.get("local", "")
                    if family == "ipv4":
                        scope = "private" if is_private_ipv4(addr) else "public"
                    else:
                        scope = "private" if is_private_ipv6(addr) else "public"
                    if raw_scope == "link":
                        scope = "link_local"
                    NodeAddressRepository.upsert(
                        node_id=node_id,
                        family=family,
                        scope=scope,
                        address=addr,
                        source="agent",
                        interface=ifname,
                    )

            # 2. SSH 获取公网 IP
            pub_ip = await ssh_exec(target_host, node.get("ssh_port", 22), node.get("ssh_user", "root"), "curl -fsSL -4 --max-time 10 https://icanhazip.com 2>/dev/null || true")
            pub_ip = pub_ip.strip()
            if pub_ip and not is_private_ipv4(pub_ip):
                NodeAddressRepository.upsert(
                    node_id=node_id,
                    family="ipv4",
                    scope="public",
                    address=pub_ip,
                    source="external_check",
                )

            refreshed = True
        except Exception as e:
            NodeAddressRepository.delete_by_sources(node_id, ["agent", "external_check"])
            AuditRepository.log(
                actor_type="admin",
                actor_id=admin.get("sub"),
                action="node.addresses.refresh",
                target_type="node",
                target_id=str(node_id),
                summary=f"SSH refresh failed for {node['hostname']}: {e}",
            )
            return {"ok": False, "message": f"SSH 连接失败: {e}，已清空旧地址等待心跳"}

    if not refreshed:
        NodeAddressRepository.delete_by_sources(node_id, ["agent", "external_check"])

    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.addresses.refresh",
        target_type="node",
        target_id=str(node_id),
        summary=f"Refreshed addresses for node {node['hostname']}",
    )
    return {"ok": True, "message": "地址已刷新"}


@router.post("/api/nodes/{node_id}/refresh-compose")
async def refresh_compose(node_id: int, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")

    addresses = NodeAddressRepository.get_by_node(node_id)
    pub4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "public"]
    pri4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "private"]
    target_host = pub4[0] if pub4 else (pri4[0] if pri4 else None)

    if not target_host:
        return {"ok": False, "message": "没有可用地址进行 SSH 连接"}
    if not os.path.exists(SSH_KEY_PATH):
        return {"ok": False, "message": "SSH 密钥不存在"}

    try:
        cmd = "export PANEL_BASE_URL=https://bastion.zlibza.com; export TOKEN_FILE=/var/lib/bastion-hub/node.token; /opt/bastion-agent/agent.sh"
        output = await ssh_exec(target_host, node.get("ssh_port", 22), node.get("ssh_user", "root"), cmd)
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="node.compose.refresh",
            target_type="node",
            target_id=str(node_id),
            summary=f"Refreshed compose for node {node['hostname']}",
        )
        return {"ok": True, "message": "Compose 信息已刷新"}
    except Exception as e:
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="node.compose.refresh",
            target_type="node",
            target_id=str(node_id),
            summary=f"SSH refresh compose failed for {node['hostname']}: {e}",
        )
        return {"ok": False, "message": f"SSH 执行失败: {e}"}


# ------------------------------------------------------------------
# Batch operations
# ------------------------------------------------------------------

@router.post("/api/nodes/batch-delete")
async def batch_delete(data: BatchNodeIds, admin=Depends(get_current_admin)):
    deleted = 0
    for node_id in data.node_ids:
        node = NodeRepository.get_by_id(node_id)
        if node:
            NodeRepository.delete(node_id)
            deleted += 1
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.batch_delete",
        target_type="node",
        target_id=",".join(str(x) for x in data.node_ids),
        summary=f"Batch deleted {deleted} nodes",
    )
    JobRepository.create(kind="asset.sync", payload={"node_id": None}, priority=50)
    return {"ok": True, "deleted": deleted}


@router.post("/api/nodes/batch-apply-policy")
async def batch_apply_policy(data: BatchApplyPolicy, admin=Depends(get_current_admin)):
    from app.models.repositories import SSHPolicyRepository
    policy = SSHPolicyRepository.get_by_id(data.policy_id)
    if not policy:
        raise HTTPException(status_code=404, detail="策略不存在")
    applied = 0
    for node_id in data.node_ids:
        node = NodeRepository.get_by_id(node_id)
        if not node:
            continue
        NodeRepository.update(node_id, policy_id=data.policy_id, desired_policy_revision=policy["revision"])
        applied += 1
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.batch_apply_policy",
        target_type="policy",
        target_id=str(data.policy_id),
        summary=f"Batch applied policy {policy.get('name')} to {applied} nodes",
    )
    return {"ok": True, "applied": applied}


@router.post("/api/nodes/batch-update")
async def batch_update(data: BatchUpdate, admin=Depends(get_current_admin)):
    updates = {k: v for k, v in data.model_dump(exclude_unset=True).items() if k != "node_ids" and v is not None}
    if not updates:
        raise HTTPException(status_code=400, detail="没有提供要更新的字段")
    updated = 0
    for node_id in data.node_ids:
        node = NodeRepository.get_by_id(node_id)
        if node:
            NodeRepository.update(node_id, **updates)
            updated += 1
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.batch_update",
        target_type="node",
        target_id=",".join(str(x) for x in data.node_ids),
        summary=f"Batch updated {updated} nodes: {updates}",
    )
    return {"ok": True, "updated": updated}


async def _refresh_one_address(node_id: int, admin: dict) -> dict:
    node = NodeRepository.get_by_id(node_id)
    if not node:
        return {"node_id": node_id, "ok": False, "message": "节点不存在"}

    addresses = NodeAddressRepository.get_by_node(node_id)
    pub4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "public"]
    pri4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "private"]
    target_host = pub4[0] if pub4 else (pri4[0] if pri4 else None)

    if not target_host or not os.path.exists(SSH_KEY_PATH):
        return {"node_id": node_id, "ok": False, "message": "没有可用地址或SSH密钥"}

    try:
        ip_json = await ssh_exec(target_host, node.get("ssh_port", 22), node.get("ssh_user", "root"), "ip -json addr show")
        local_addrs = json.loads(ip_json)
        for iface in local_addrs:
            ifname = iface.get("ifname", "")
            if ifname.startswith(("lo", "docker0", "br-", "veth")):
                continue
            for info in iface.get("addr_info", []):
                if info.get("scope") == "host":
                    continue
                family = "ipv4" if info.get("family") == "inet" else "ipv6"
                raw_scope = info.get("scope", "")
                addr = info.get("local", "")
                if family == "ipv4":
                    scope = "private" if is_private_ipv4(addr) else "public"
                else:
                    scope = "private" if is_private_ipv6(addr) else "public"
                if raw_scope == "link":
                    scope = "link_local"
                NodeAddressRepository.upsert(
                    node_id=node_id, family=family, scope=scope, address=addr,
                    source="agent", interface=ifname,
                )
        pub_ip = await ssh_exec(target_host, node.get("ssh_port", 22), node.get("ssh_user", "root"), "curl -fsSL -4 --max-time 10 https://icanhazip.com 2>/dev/null || true")
        pub_ip = pub_ip.strip()
        if pub_ip and not is_private_ipv4(pub_ip):
            NodeAddressRepository.upsert(
                node_id=node_id, family="ipv4", scope="public", address=pub_ip, source="external_check",
            )
        return {"node_id": node_id, "ok": True, "message": "地址已刷新"}
    except Exception as e:
        return {"node_id": node_id, "ok": False, "message": f"SSH 失败: {e}"}


@router.post("/api/nodes/batch-refresh-addresses")
async def batch_refresh_addresses(data: BatchNodeIds, admin=Depends(get_current_admin)):
    tasks = [_refresh_one_address(nid, admin) for nid in data.node_ids]
    results = await asyncio.gather(*tasks, return_exceptions=True)
    cleaned = []
    for r in results:
        if isinstance(r, Exception):
            cleaned.append({"ok": False, "message": str(r)})
        else:
            cleaned.append(r)
    ok_count = sum(1 for r in cleaned if r.get("ok"))
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.batch_refresh_addresses",
        target_type="node",
        target_id=",".join(str(x) for x in data.node_ids),
        summary=f"Batch refreshed addresses for {ok_count}/{len(data.node_ids)} nodes",
    )
    return {"ok": True, "results": cleaned}


async def _refresh_one_compose(node_id: int, admin: dict) -> dict:
    node = NodeRepository.get_by_id(node_id)
    if not node:
        return {"node_id": node_id, "ok": False, "message": "节点不存在"}

    addresses = NodeAddressRepository.get_by_node(node_id)
    pub4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "public"]
    pri4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "private"]
    target_host = pub4[0] if pub4 else (pri4[0] if pri4 else None)

    if not target_host:
        return {"node_id": node_id, "ok": False, "message": "没有可用地址"}
    if not os.path.exists(SSH_KEY_PATH):
        return {"node_id": node_id, "ok": False, "message": "SSH 密钥不存在"}

    try:
        cmd = "export PANEL_BASE_URL=https://bastion.zlibza.com; export TOKEN_FILE=/var/lib/bastion-hub/node.token; /opt/bastion-agent/agent.sh"
        await ssh_exec(target_host, node.get("ssh_port", 22), node.get("ssh_user", "root"), cmd)
        return {"node_id": node_id, "ok": True, "message": "Compose 已刷新"}
    except Exception as e:
        return {"node_id": node_id, "ok": False, "message": f"SSH 失败: {e}"}


@router.post("/api/nodes/batch-refresh-compose")
async def batch_refresh_compose(data: BatchNodeIds, admin=Depends(get_current_admin)):
    tasks = [_refresh_one_compose(nid, admin) for nid in data.node_ids]
    results = await asyncio.gather(*tasks, return_exceptions=True)
    cleaned = []
    for r in results:
        if isinstance(r, Exception):
            cleaned.append({"ok": False, "message": str(r)})
        else:
            cleaned.append(r)
    ok_count = sum(1 for r in cleaned if r.get("ok"))
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="node.batch_refresh_compose",
        target_type="node",
        target_id=",".join(str(x) for x in data.node_ids),
        summary=f"Batch refreshed compose for {ok_count}/{len(data.node_ids)} nodes",
    )
    return {"ok": True, "results": cleaned}


class BatchComposeAutoUpdate(BaseModel):
    names: List[str]
    auto_update: bool


@router.post("/api/nodes/{node_id}/compose-projects/batch-auto-update")
async def batch_compose_auto_update(node_id: int, data: BatchComposeAutoUpdate, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="节点不存在")
    updated = ComposeProjectRepository.update_auto_update(node_id, data.names, data.auto_update)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="compose.batch_auto_update",
        target_type="node",
        target_id=str(node_id),
        summary=f"Batch set auto_update={data.auto_update} for {updated} compose projects",
    )
    return {"ok": True, "updated": updated}
