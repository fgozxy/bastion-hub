import os
import json
import httpx
from typing import List, Dict, Optional, Any

CLOUDFLARE_API_BASE = "https://api.cloudflare.com/client/v4"


class CloudflareClient:
    def __init__(self, api_token: str, account_id: str):
        self.api_token = api_token
        self.account_id = account_id
        self.client = httpx.AsyncClient(
            base_url=CLOUDFLARE_API_BASE,
            headers={"Authorization": f"Bearer {api_token}", "Content-Type": "application/json"},
            timeout=30.0,
        )

    async def close(self):
        await self.client.aclose()

    async def _request(self, method: str, path: str, **kwargs) -> dict:
        resp = await self.client.request(method, path, **kwargs)
        data = resp.json()
        if not data.get("success"):
            errors = data.get("errors", [])
            raise CloudflareAPIError(errors[0].get("message", "Unknown error") if errors else "Unknown error")
        return data.get("result", {})

    async def list_lists(self) -> List[dict]:
        """Get all IP Lists for the account."""
        data = await self._request("GET", f"/accounts/{self.account_id}/rules/lists")
        return data if isinstance(data, list) else []

    async def get_or_create_list(self, name: str, description: str = "Managed by Bastion Hub") -> dict:
        """Find existing list by name or create a new one."""
        lists = await self.list_lists()
        for lst in lists:
            if lst.get("name") == name:
                return lst
        # Create new list
        result = await self._request(
            "POST",
            f"/accounts/{self.account_id}/rules/lists",
            json={"name": name, "kind": "ip", "description": description},
        )
        return result

    async def get_list_items(self, list_id: str) -> List[dict]:
        """Get all items in a list."""
        data = await self._request("GET", f"/accounts/{self.account_id}/rules/lists/{list_id}/items")
        return data if isinstance(data, list) else []

    async def add_list_items(self, list_id: str, items: List[dict]) -> dict:
        """Add items to a list. items: [{"ip": "x.x.x.x", "comment": "..."}]"""
        return await self._request(
            "POST",
            f"/accounts/{self.account_id}/rules/lists/{list_id}/items",
            json=items,
        )

    async def delete_list_items(self, list_id: str, item_ids: List[str]) -> dict:
        """Delete items from a list by their IDs."""
        return await self._request(
            "DELETE",
            f"/accounts/{self.account_id}/rules/lists/{list_id}/items",
            json={"items": [{"id": iid} for iid in item_ids]},
        )

    async def verify_token(self) -> bool:
        """Quick token validation."""
        try:
            data = await self._request("GET", "/user/tokens/verify")
            return data.get("status") == "active"
        except Exception:
            return False


class CloudflareAPIError(Exception):
    pass


def build_managed_comment(node: dict) -> str:
    """Build comment marker for managed IPs."""
    parts = [
        "managed-by:bastion-hub",
        f"node:{node.get('uuid', 'unknown')}",
        f"hostname:{node.get('hostname', 'unknown')}",
        f"env:{node.get('env', 'unknown')}",
        f"role:{node.get('role', 'unknown')}",
    ]
    return " ".join(parts)


def parse_managed_comment(comment: str) -> Optional[dict]:
    """Parse a managed comment to verify it was created by us."""
    if not comment:
        return None
    if "managed-by:bastion-hub" not in comment:
        return None
    result = {}
    for part in comment.split():
        if ":" in part:
            key, val = part.split(":", 1)
            result[key] = val
    return result


def is_managed_item(item: dict) -> bool:
    """Check if a list item was managed by Bastion Hub."""
    comment = item.get("comment", "")
    return "managed-by:bastion-hub" in comment
