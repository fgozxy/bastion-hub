import base64
import hashlib
from typing import Optional, List
from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel, Field

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519

from app.api.auth import get_current_admin
from app.api.agent import get_current_node
from app.models.repositories import (
    CredentialRepository, CredentialBindingRepository,
    NodeRepository, AuditRepository,
)

router = APIRouter()


def generate_ssh_keypair() -> tuple[str, str]:
    private_key = ed25519.Ed25519PrivateKey.generate()
    private_pem = private_key.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.OpenSSH,
        encryption_algorithm=serialization.NoEncryption(),
    ).decode()
    public_key = private_key.public_key().public_bytes(
        encoding=serialization.Encoding.OpenSSH,
        format=serialization.PublicFormat.OpenSSH,
    ).decode()
    return private_pem, public_key


def ssh_fingerprint(public_key: str) -> str:
    parts = public_key.strip().split()
    if len(parts) >= 2:
        key_data = base64.b64decode(parts[1])
        digest = hashlib.sha256(key_data).digest()
        return "SHA256:" + base64.b64encode(digest).rstrip(b"=").decode()
    return ""


class CredentialCreate(BaseModel):
    name: str
    type: str = "ssh_public_key"
    source: str = "generate"  # "generate" or "upload"
    public_payload: Optional[str] = None
    encrypted_payload: Optional[str] = None
    scope: Optional[str] = None


class CredentialUpdate(BaseModel):
    name: Optional[str] = None
    public_payload: Optional[str] = None
    encrypted_payload: Optional[str] = None
    fingerprint: Optional[str] = None
    status: Optional[str] = None


class CredentialBind(BaseModel):
    node_ids: List[int]
    target_user: str = "root"


class CredentialUnbind(BaseModel):
    node_ids: List[int]
    target_user: Optional[str] = None


class AgentCredentialResultItem(BaseModel):
    binding_id: int
    status: str  # applied / failed
    error_msg: Optional[str] = None


class AgentCredentialResult(BaseModel):
    results: List[AgentCredentialResultItem]


# ------------------------------------------------------------------
# Panel APIs
# ------------------------------------------------------------------

@router.get("/api/credentials")
async def list_credentials(admin=Depends(get_current_admin)):
    credentials = CredentialRepository.list_all()
    return {"credentials": credentials}


@router.post("/api/credentials")
async def create_credential(data: CredentialCreate, admin=Depends(get_current_admin)):
    public_payload = data.public_payload
    encrypted_payload = data.encrypted_payload
    fingerprint = None

    if data.source == "generate":
        encrypted_payload, public_payload = generate_ssh_keypair()
        fingerprint = ssh_fingerprint(public_payload)
    elif data.source == "upload":
        if not public_payload:
            raise HTTPException(status_code=400, detail="上传模式必须提供公钥内容")
        fingerprint = ssh_fingerprint(public_payload)
    else:
        raise HTTPException(status_code=400, detail="不支持的 source 类型")

    credential_id = CredentialRepository.create(
        name=data.name,
        type=data.type,
        public_payload=public_payload,
        encrypted_payload=encrypted_payload,
        fingerprint=fingerprint,
        scope=data.scope,
        status="active",
    )
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="credential.create",
        target_type="credential",
        target_id=str(credential_id),
        summary=f"Created credential {data.name} ({data.type})",
    )
    cred = CredentialRepository.get_by_id(credential_id)
    return {"credential": cred}


@router.get("/api/credentials/{credential_id}")
async def get_credential(credential_id: int, admin=Depends(get_current_admin)):
    credential = CredentialRepository.get_by_id(credential_id)
    if not credential:
        raise HTTPException(status_code=404, detail="凭证不存在")
    bindings = CredentialBindingRepository.get_by_credential(credential_id)
    return {"credential": credential, "bindings": bindings}


@router.patch("/api/credentials/{credential_id}")
async def update_credential(credential_id: int, data: CredentialUpdate, admin=Depends(get_current_admin)):
    credential = CredentialRepository.get_by_id(credential_id)
    if not credential:
        raise HTTPException(status_code=404, detail="凭证不存在")
    updates = data.model_dump(exclude_unset=True)
    if "public_payload" in updates and updates["public_payload"]:
        updates["fingerprint"] = ssh_fingerprint(updates["public_payload"])
    if updates:
        CredentialRepository.update(credential_id, **updates)
        AuditRepository.log(
            actor_type="admin",
            actor_id=admin.get("sub"),
            action="credential.update",
            target_type="credential",
            target_id=str(credential_id),
            summary=f"Updated credential {credential_id}",
        )
    return {"ok": True}


@router.delete("/api/credentials/{credential_id}")
async def delete_credential(credential_id: int, admin=Depends(get_current_admin)):
    credential = CredentialRepository.get_by_id(credential_id)
    if not credential:
        raise HTTPException(status_code=404, detail="凭证不存在")
    CredentialRepository.delete(credential_id)
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="credential.delete",
        target_type="credential",
        target_id=str(credential_id),
        summary=f"Deleted credential {credential.get('name')}",
    )
    return {"ok": True}


@router.post("/api/credentials/{credential_id}/bind")
async def bind_credential(credential_id: int, data: CredentialBind, admin=Depends(get_current_admin)):
    credential = CredentialRepository.get_by_id(credential_id)
    if not credential:
        raise HTTPException(status_code=404, detail="凭证不存在")
    bound = 0
    for node_id in data.node_ids:
        node = NodeRepository.get_by_id(node_id)
        if not node:
            continue
        CredentialBindingRepository.create(credential_id, node_id, data.target_user)
        bound += 1
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="credential.bind",
        target_type="credential",
        target_id=str(credential_id),
        summary=f"Bound credential {credential.get('name')} to {bound} nodes (user={data.target_user})",
    )
    return {"ok": True, "bound": bound}


@router.post("/api/credentials/{credential_id}/unbind")
async def unbind_credential(credential_id: int, data: CredentialUnbind, admin=Depends(get_current_admin)):
    credential = CredentialRepository.get_by_id(credential_id)
    if not credential:
        raise HTTPException(status_code=404, detail="凭证不存在")
    deleted = CredentialBindingRepository.delete_by_credential_and_nodes(
        credential_id, data.node_ids, data.target_user
    )
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="credential.unbind",
        target_type="credential",
        target_id=str(credential_id),
        summary=f"Unbound credential {credential.get('name')} from {deleted} nodes",
    )
    return {"ok": True, "deleted": deleted}


# ------------------------------------------------------------------
# Agent APIs
# ------------------------------------------------------------------

@router.get("/api/agent/credentials")
async def agent_credentials(node_ctx: dict = Depends(get_current_node)):
    node = node_ctx["node"]
    pending = CredentialBindingRepository.list_pending_for_node(node["id"])
    # Group by target_user
    grouped: dict[str, list[dict]] = {}
    for row in pending:
        user = row["target_user"]
        grouped.setdefault(user, []).append({
            "binding_id": row["id"],
            "credential_id": row["credential_id"],
            "name": row["credential_name"],
            "public_payload": row["public_payload"],
            "fingerprint": row["fingerprint"],
        })
    return {"credentials_by_user": grouped}


@router.post("/api/agent/credentials-result")
async def agent_credentials_result(data: AgentCredentialResult, node_ctx: dict = Depends(get_current_node)):
    for item in data.results:
        binding = CredentialBindingRepository.get_by_id(item.binding_id)
        if binding and binding["node_id"] == node_ctx["node"]["id"]:
            CredentialBindingRepository.update_status(
                item.binding_id,
                item.status,
                item.error_msg,
            )
    return {"ok": True}
