from typing import Optional, Dict, Any
from app.models.repositories import NodeRepository, NodeAddressRepository


def get_node_with_addresses(node_id: int) -> Optional[Dict[str, Any]]:
    node = NodeRepository.get_by_id(node_id)
    if not node:
        return None
    node["addresses"] = NodeAddressRepository.get_by_node(node_id)
    return node


def get_nodes_summary() -> Dict[str, Any]:
    from app.core.db import get_cursor
    with get_cursor() as cur:
        cur.execute("SELECT status, COUNT(*) as cnt FROM nodes GROUP BY status")
        status_counts = {row["status"]: row["cnt"] for row in cur.fetchall()}
        cur.execute("SELECT COUNT(*) as total FROM nodes")
        total = cur.fetchone()["total"]
        cur.execute("SELECT COUNT(*) as unconverged FROM nodes WHERE desired_policy_revision != applied_policy_revision")
        unconverged = cur.fetchone()["unconverged"]
        cur.execute("SELECT COUNT(*) as recent FROM nodes WHERE last_seen_at > datetime('now', '-10 minutes')")
        recent_online = cur.fetchone()["recent"]
    return {
        "total": total,
        "online": status_counts.get("online", 0),
        "offline": status_counts.get("offline", 0),
        "pending": status_counts.get("pending", 0),
        "unconverged": unconverged,
        "recent_online": recent_online,
    }
