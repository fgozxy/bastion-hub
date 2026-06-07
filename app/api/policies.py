import json
from typing import Optional, List
from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel, Field

from app.api.auth import get_current_admin
from app.api.credentials import generate_ssh_keypair, ssh_fingerprint
from app.models.repositories import (
    SSHPolicyRepository, NodeRepository, AuditRepository, JobRepository,
    CredentialRepository, CredentialBindingRepository,
)

router = APIRouter()


class PolicyCreate(BaseModel):
    name: str
    mode: str = "report"
    trusted_user_ca_public_key: Optional[str] = None
    allowed_principals: List[str] = Field(default_factory=list)
    allowed_source_node_ids: List[int] = Field(default_factory=list)
    allowed_source_cidrs: List[str] = Field(default_factory=list)
    sshd_config: dict = Field(default_factory=dict)
    firewall_config: dict = Field(default_factory=dict)
    docker_config: dict = Field(default_factory=dict)
    agent_config: dict = Field(default_factory=dict)


class PolicyUpdate(BaseModel):
    name: Optional[str] = None
    mode: Optional[str] = None
    trusted_user_ca_public_key: Optional[str] = None
    allowed_principals: Optional[List[str]] = None
    allowed_source_node_ids: Optional[List[int]] = None
    allowed_source_cidrs: Optional[List[str]] = None
    sshd_config: Optional[dict] = None
    firewall_config: Optional[dict] = None
    docker_config: Optional[dict] = None
    agent_config: Optional[dict] = None


class PolicyApply(BaseModel):
    node_ids: List[int]
    auto_generate_root_key: bool = False


@router.get("/api/policies")
async def list_policies(admin=Depends(get_current_admin)):
    policies = SSHPolicyRepository.list_all()
    return {"policies": policies}


@router.post("/api/policies")
async def create_policy(data: PolicyCreate, admin=Depends(get_current_admin)):
    policy_id = SSHPolicyRepository.create(
        name=data.name,
        mode=data.mode,
        trusted_user_ca_public_key=data.trusted_user_ca_public_key,
        allowed_principals_json=json.dumps(data.allowed_principals),
        allowed_source_node_ids_json=json.dumps(data.allowed_source_node_ids),
        allowed_source_cidrs_json=json.dumps(data.allowed_source_cidrs),
        sshd_config_json=json.dumps(data.sshd_config),
        firewall_config_json=json.dumps(data.firewall_config),
        docker_config_json=json.dumps(data.docker_config),
        agent_config_json=json.dumps(data.agent_config),
    )
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="policy.create",
        target_type="policy",
        target_id=str(policy_id),
        summary=f"Created policy {data.name} ({data.mode})",
    )
    return {"id": policy_id}


@router.get("/api/policies/{policy_id}")
async def get_policy(policy_id: int, admin=Depends(get_current_admin)):
    policy = SSHPolicyRepository.get_by_id(policy_id)
    if not policy:
        raise HTTPException(status_code=404, detail="策略不存在")
    for key in ["allowed_principals_json", "allowed_source_node_ids_json", "allowed_source_cidrs_json", "sshd_config_json", "firewall_config_json", "docker_config_json", "agent_config_json"]:
        try:
            default = "[]" if any(x in key for x in ["principals", "node_ids", "cidrs"]) else "{}"
            policy[key.replace("_json", "")] = json.loads(policy.get(key) or default)
        except Exception:
            policy[key.replace("_json", "")] = [] if any(x in key for x in ["principals", "node_ids", "cidrs"]) else {}
    return policy


@router.patch("/api/policies/{policy_id}")
async def update_policy(policy_id: int, data: PolicyUpdate, admin=Depends(get_current_admin)):
    policy = SSHPolicyRepository.get_by_id(policy_id)
    if not policy:
        raise HTTPException(status_code=404, detail="策略不存在")
    updates = {}
    if data.name is not None:
        updates["name"] = data.name
    if data.mode is not None:
        updates["mode"] = data.mode
    if data.trusted_user_ca_public_key is not None:
        updates["trusted_user_ca_public_key"] = data.trusted_user_ca_public_key
    if data.allowed_principals is not None:
        updates["allowed_principals_json"] = json.dumps(data.allowed_principals)
    if data.allowed_source_node_ids is not None:
        updates["allowed_source_node_ids_json"] = json.dumps(data.allowed_source_node_ids)
    if data.allowed_source_cidrs is not None:
        updates["allowed_source_cidrs_json"] = json.dumps(data.allowed_source_cidrs)
    if data.sshd_config is not None:
        updates["sshd_config_json"] = json.dumps(data.sshd_config)
    if data.firewall_config is not None:
        updates["firewall_config_json"] = json.dumps(data.firewall_config)
    if data.docker_config is not None:
        updates["docker_config_json"] = json.dumps(data.docker_config)
    if data.agent_config is not None:
        updates["agent_config_json"] = json.dumps(data.agent_config)
    if updates:
        SSHPolicyRepository.update(policy_id, **updates)
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="policy.update",
            target_type="policy",
            target_id=str(policy_id),
            summary=f"Updated policy {policy_id}",
        )
    return {"ok": True}


@router.delete("/api/policies/{policy_id}")
async def delete_policy(policy_id: int, admin=Depends(get_current_admin)):
    policy = SSHPolicyRepository.get_by_id(policy_id)
    if not policy:
        raise HTTPException(status_code=404, detail="策略不存在")
    SSHPolicyRepository.delete(policy_id)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="policy.delete",
        target_type="policy",
        target_id=str(policy_id),
        summary=f"Deleted policy {policy.get('name')}",
    )
    return {"ok": True}


@router.post("/api/policies/{policy_id}/apply")
async def apply_policy(policy_id: int, data: PolicyApply, admin=Depends(get_current_admin)):
    policy = SSHPolicyRepository.get_by_id(policy_id)
    if not policy:
        raise HTTPException(status_code=404, detail="策略不存在")
    applied = 0
    generated_keys = []

    for node_id in data.node_ids:
        node = NodeRepository.get_by_id(node_id)
        if not node:
            continue
        NodeRepository.update(node_id, policy_id=policy_id, desired_policy_revision=policy["revision"])
        applied += 1

        if data.auto_generate_root_key:
            # Check if node already has an active credential binding for root
            existing_bindings = CredentialBindingRepository.get_by_node(node_id)
            has_root_key = any(
                b["target_user"] == "root" and b["status"] in ("pending", "applied")
                for b in existing_bindings
            )
            if not has_root_key:
                priv_key, pub_key = generate_ssh_keypair()
                fingerprint = ssh_fingerprint(pub_key)
                cred_name = f"{policy['name']}-{node['hostname'] or node['display_name'] or 'node'}-root"
                credential_id = CredentialRepository.create(
                    name=cred_name,
                    type="ssh_public_key",
                    public_payload=pub_key,
                    encrypted_payload=priv_key,
                    fingerprint=fingerprint,
                    status="active",
                )
                CredentialBindingRepository.create(credential_id, node_id, "root")
                generated_keys.append({
                    "node_id": node_id,
                    "node_name": node.get("display_name") or node.get("hostname"),
                    "credential_id": credential_id,
                    "credential_name": cred_name,
                    "private_key": priv_key,
                    "public_key": pub_key,
                })

    summary = f"Applied policy {policy.get('name')} rev {policy['revision']} to {applied} nodes"
    if generated_keys:
        summary += f", generated {len(generated_keys)} root keys"

    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="policy.apply",
        target_type="policy",
        target_id=str(policy_id),
        summary=summary,
    )
    JobRepository.create(kind="policy.reconcile", payload={"policy_id": policy_id}, priority=40)
    return {"ok": True, "applied": applied, "generated_keys": generated_keys}
