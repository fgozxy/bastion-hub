import os
import json
from datetime import datetime, timezone
from typing import Optional, List
from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel, Field

from app.api.auth import get_current_admin
from app.core.config import get_settings
from app.core.security import encrypt_payload, decrypt_payload
from app.models.repositories import (
    CloudflareSettingsRepository, CloudflareSyncLogRepository,
    AuditRepository, NodeRepository, NodeAddressRepository, JobRepository,
)
from app.integrations.cloudflare import CloudflareClient, CloudflareAPIError, build_managed_comment, is_managed_item

router = APIRouter()

MASTER_KEY = os.environ.get("PANEL_SECRET_KEY", "")


class CloudflareSettingsUpdate(BaseModel):
    account_id: str
    api_token: Optional[str] = None
    list_name_ipv4: str = Field(default="bastion_nodes_ipv4")
    list_name_ipv6: str = Field(default="bastion_nodes_ipv6")
    mode: str = Field(default="add_only")
    delete_grace_days: int = Field(default=7)
    enabled: bool = Field(default=False)


class CloudflareSyncResult(BaseModel):
    ok: bool
    kind: str
    desired: int
    actual: int
    added: int
    removed: int
    skipped: int
    dry_run: bool
    details: dict = Field(default_factory=dict)


def _encrypt_token(token: Optional[str]) -> Optional[str]:
    if not token or not MASTER_KEY:
        return token
    return encrypt_payload(MASTER_KEY, token)


def _decrypt_token(encrypted: Optional[str]) -> Optional[str]:
    if not encrypted or not MASTER_KEY:
        return encrypted
    try:
        return decrypt_payload(MASTER_KEY, encrypted)
    except Exception:
        return None


@router.get("/api/cloudflare/settings")
async def get_cloudflare_settings(admin=Depends(get_current_admin)):
    settings = CloudflareSettingsRepository.get()
    if not settings:
        return {
            "account_id": "",
            "list_name_ipv4": "bastion_nodes_ipv4",
            "list_name_ipv6": "bastion_nodes_ipv6",
            "mode": "add_only",
            "delete_grace_days": 7,
            "enabled": False,
        }
    return {
        "account_id": settings.get("account_id", ""),
        "list_name_ipv4": settings.get("list_name_ipv4", "bastion_nodes_ipv4"),
        "list_name_ipv6": settings.get("list_name_ipv6", "bastion_nodes_ipv6"),
        "mode": settings.get("mode", "add_only"),
        "delete_grace_days": settings.get("delete_grace_days", 7),
        "enabled": bool(settings.get("enabled", 0)),
    }


@router.post("/api/cloudflare/settings")
async def save_cloudflare_settings(data: CloudflareSettingsUpdate, admin=Depends(get_current_admin)):
    existing = CloudflareSettingsRepository.get()
    token_encrypted = _encrypt_token(data.api_token) if data.api_token else None
    if not token_encrypted and existing:
        token_encrypted = existing.get("api_token_encrypted")

    CloudflareSettingsRepository.upsert(
        account_id=data.account_id,
        api_token_encrypted=token_encrypted,
        list_name_ipv4=data.list_name_ipv4,
        list_name_ipv6=data.list_name_ipv6,
        mode=data.mode,
        delete_grace_days=data.delete_grace_days,
        enabled=data.enabled,
    )
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="cloudflare.settings.update",
        target_type="cloudflare",
        target_id="",
        summary="Updated Cloudflare settings",
    )
    return {"ok": True}


@router.post("/api/cloudflare/test")
async def test_cloudflare_connection(admin=Depends(get_current_admin)):
    settings = CloudflareSettingsRepository.get()
    if not settings:
        raise HTTPException(status_code=400, detail="Cloudflare 未配置")
    token = _decrypt_token(settings.get("api_token_encrypted"))
    if not token:
        raise HTTPException(status_code=400, detail="API Token 无法解密")
    client = CloudflareClient(token, settings["account_id"])
    try:
        ok = await client.verify_token()
        await client.close()
        if not ok:
            raise HTTPException(status_code=400, detail="Token 验证失败")
        return {"ok": True, "message": "连接成功"}
    except CloudflareAPIError as e:
        raise HTTPException(status_code=400, detail=f"Cloudflare API 错误: {e}")
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"测试失败: {e}")


@router.get("/api/cloudflare/sync-logs")
async def list_sync_logs(limit: int = 50, admin=Depends(get_current_admin)):
    logs = CloudflareSyncLogRepository.list_recent(limit)
    return {"logs": logs}


@router.post("/api/cloudflare/sync")
async def manual_sync(dry_run: bool = False, admin=Depends(get_current_admin)):
    settings = CloudflareSettingsRepository.get()
    if not settings or not settings.get("enabled"):
        raise HTTPException(status_code=400, detail="Cloudflare 同步未启用")
    token = _decrypt_token(settings.get("api_token_encrypted"))
    if not token:
        raise HTTPException(status_code=400, detail="API Token 无法解密")

    job_id = JobRepository.create(
        kind="cloudflare.ip_list.reconcile",
        payload={"dry_run": dry_run, "triggered_by": admin.get("sub")},
        priority=20,
    )
    AuditRepository.log(
        actor_type="admin",
        actor_id=admin.get("sub"),
        action="cloudflare.sync.trigger",
        target_type="cloudflare",
        target_id="",
        summary=f"Triggered Cloudflare sync (dry_run={dry_run})",
    )
    return {"ok": True, "job_id": job_id}
