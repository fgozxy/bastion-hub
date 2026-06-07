import os
import sys
import json
import time
import signal
import logging
import asyncio
from typing import Optional
from datetime import datetime, timezone

# Allow imports from project root
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))

from app.core.db import init_db, get_cursor
from app.models.repositories import (
    JobRepository, NodeGitHubBindingRepository, NodeRepository,
    NodeAddressRepository, AuditRepository, ComposeProjectRepository,
    CloudflareSettingsRepository, CloudflareSyncLogRepository,
)
from app.api.nodes import ssh_exec
from app.core.security import decrypt_payload
from app.integrations.cloudflare import (
    CloudflareClient, CloudflareAPIError,
    build_managed_comment, is_managed_item,
)

MASTER_KEY = os.environ.get("PANEL_SECRET_KEY", "")


def _decrypt_token(encrypted: str) -> Optional[str]:
    if not encrypted or not MASTER_KEY:
        return encrypted
    try:
        return decrypt_payload(MASTER_KEY, encrypted)
    except Exception:
        return None

SSH_KEY_PATH = os.environ.get("SSH_KEY_PATH", "/data/ssh/bastion-hub")
SSH_OPTS = [
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "ConnectTimeout=10",
    "-o", "BatchMode=yes",
]

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
logger = logging.getLogger("worker")

WORKER_ID = f"worker-{os.getpid()}"
SHUTDOWN = False


def handle_signal(signum, frame):
    global SHUTDOWN
    logger.info("Received signal %s, shutting down...", signum)
    SHUTDOWN = True


signal.signal(signal.SIGTERM, handle_signal)
signal.signal(signal.SIGINT, handle_signal)


def lock_job(job_id: int) -> bool:
    t = datetime.now(timezone.utc).isoformat()
    try:
        with get_cursor() as cur:
            cur.execute(
                "UPDATE jobs SET status = 'running', locked_by = ?, locked_at = ?, attempts = attempts + 1, updated_at = ? WHERE id = ? AND status = 'pending'",
                (WORKER_ID, t, t, job_id),
            )
            return cur.rowcount > 0
    except Exception:
        return False


def complete_job(job_id: int, result: dict):
    t = datetime.now(timezone.utc).isoformat()
    with get_cursor() as cur:
        cur.execute("SELECT payload_json FROM jobs WHERE id = ?", (job_id,))
        row = cur.fetchone()
        payload = json.loads(row["payload_json"]) if row and row["payload_json"] else {}
        payload["result"] = result
        cur.execute(
            "UPDATE jobs SET status = 'completed', payload_json = ?, updated_at = ? WHERE id = ?",
            (json.dumps(payload), t, job_id),
        )


def fail_job(job_id: int, error: str):
    t = datetime.now(timezone.utc).isoformat()
    with get_cursor() as cur:
        cur.execute(
            "SELECT max_attempts, attempts FROM jobs WHERE id = ?",
            (job_id,),
        )
        row = cur.fetchone()
        if row and row["attempts"] >= row["max_attempts"]:
            new_status = "failed"
        else:
            new_status = "pending"
        cur.execute("SELECT payload_json FROM jobs WHERE id = ?", (job_id,))
        prow = cur.fetchone()
        payload = json.loads(prow["payload_json"]) if prow and prow["payload_json"] else {}
        payload["last_error"] = error
        cur.execute(
            "UPDATE jobs SET status = ?, payload_json = ?, updated_at = ? WHERE id = ?",
            (new_status, json.dumps(payload), t, job_id),
        )


async def _do_github_deploy(payload: dict) -> dict:
    node_id = payload.get("node_id")
    binding_id = payload.get("binding_id")
    compose_project_name = payload.get("compose_project_name")
    node = NodeRepository.get_by_id(node_id)
    if not node:
        return {"ok": False, "message": "Node not found"}

    addresses = NodeAddressRepository.get_by_node(node_id)
    pub4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "public"]
    pri4 = [a["address"] for a in addresses if a["family"] == "ipv4" and a["scope"] == "private"]
    target_host = pub4[0] if pub4 else (pri4[0] if pri4 else None)
    if not target_host:
        return {"ok": False, "message": "No reachable address"}
    if not os.path.exists(SSH_KEY_PATH):
        return {"ok": False, "message": "SSH key not found"}

    user = node.get("ssh_user", "root")
    port = node.get("ssh_port", 22)

    if binding_id:
        binding = NodeGitHubBindingRepository.get_by_id(binding_id)
        if not binding:
            return {"ok": False, "message": "Binding not found"}
        project_path = binding.get("project_path", "")
        compose_file = binding.get("compose_file", "docker-compose.yml")
    elif compose_project_name:
        projects = ComposeProjectRepository.get_by_node(node_id)
        project = next((p for p in projects if p.get("name") == compose_project_name), None)
        if not project:
            return {"ok": False, "message": "Compose project not found"}
        raw_path = project.get("project_path", "")
        if raw_path.endswith((".yml", ".yaml")):
            compose_file = os.path.basename(raw_path)
            project_path = os.path.dirname(raw_path)
        else:
            compose_file = "docker-compose.yml"
            project_path = raw_path
    else:
        return {"ok": False, "message": "No binding_id or compose_project_name provided"}

    try:
        cmd = (
            f"cd '{project_path}' && "
            f"docker compose -f '{compose_file}' pull && "
            f"docker compose -f '{compose_file}' up -d"
        )
        output = await ssh_exec(target_host, port, user, cmd)
        return {"ok": True, "message": "Deployed", "output": output[:500]}
    except Exception as e:
        return {"ok": False, "message": f"Deploy failed: {e}"}


async def _do_cloudflare_reconcile(payload: dict) -> dict:
    settings = CloudflareSettingsRepository.get()
    if not settings or not settings.get("enabled"):
        return {"ok": False, "message": "Cloudflare sync not enabled"}

    token = _decrypt_token(settings.get("api_token_encrypted"))
    if not token:
        return {"ok": False, "message": "API token decrypt failed"}

    account_id = settings["account_id"]
    mode = settings.get("mode", "add_only")
    dry_run = payload.get("dry_run", False)

    # Collect desired public IPs from all non-disabled nodes
    nodes = NodeRepository.list_all()
    desired_ipv4 = []
    desired_ipv6 = []

    for node in nodes:
        if node.get("status") == "disabled":
            continue
        node_id = node["id"]
        addresses = NodeAddressRepository.get_by_node(node_id)
        comment = build_managed_comment(node)
        for addr in addresses:
            if addr.get("scope") != "public":
                continue
            family = addr.get("family")
            ip = addr.get("address")
            if not ip:
                continue
            if family == "ipv4":
                desired_ipv4.append({"ip": ip, "comment": comment})
            elif family == "ipv6":
                desired_ipv6.append({"ip": ip, "comment": comment})

    # Deduplicate by IP (keep first comment)
    def dedup(items):
        seen = set()
        out = []
        for item in items:
            if item["ip"] not in seen:
                seen.add(item["ip"])
                out.append(item)
        return out

    desired_ipv4 = dedup(desired_ipv4)
    desired_ipv6 = dedup(desired_ipv6)

    client = CloudflareClient(token, account_id)
    results = []

    try:
        for kind, desired, list_name_key in [
            ("ipv4", desired_ipv4, "list_name_ipv4"),
            ("ipv6", desired_ipv6, "list_name_ipv6"),
        ]:
            list_name = settings.get(list_name_key, f"bastion_nodes_{kind}")
            try:
                lst = await client.get_or_create_list(list_name)
                list_id = lst["id"]
                actual_items = await client.get_list_items(list_id)

                desired_ips = {d["ip"] for d in desired}
                actual_ips = {a.get("ip") for a in actual_items}

                to_add = [d for d in desired if d["ip"] not in actual_ips]
                to_remove = []
                if mode != "add_only":
                    for item in actual_items:
                        ip = item.get("ip")
                        if ip and ip not in desired_ips and is_managed_item(item):
                            to_remove.append(item)

                added = 0
                removed = 0
                skipped = 0

                if not dry_run:
                    if to_add:
                        chunk_size = 100
                        for i in range(0, len(to_add), chunk_size):
                            await client.add_list_items(list_id, to_add[i:i+chunk_size])
                        added = len(to_add)

                    if to_remove and mode != "add_only":
                        item_ids = [item["id"] for item in to_remove]
                        chunk_size = 100
                        for i in range(0, len(item_ids), chunk_size):
                            await client.delete_list_items(list_id, item_ids[i:i+chunk_size])
                        removed = len(to_remove)
                else:
                    skipped = len(to_add) + len(to_remove)

                CloudflareSyncLogRepository.create(
                    kind=kind,
                    mode=mode,
                    desired_count=len(desired),
                    actual_count=len(actual_items),
                    added_count=added,
                    removed_count=removed,
                    skipped_count=skipped,
                    dry_run=dry_run,
                    details={
                        "list_name": list_name,
                        "list_id": list_id,
                        "to_add": [d["ip"] for d in to_add],
                        "to_remove": [item.get("ip") for item in to_remove],
                    },
                )

                results.append({
                    "kind": kind,
                    "desired": len(desired),
                    "actual": len(actual_items),
                    "added": added,
                    "removed": removed,
                    "dry_run": dry_run,
                })
            except CloudflareAPIError as e:
                results.append({"kind": kind, "error": str(e)})

        await client.close()
        return {"ok": True, "results": results}
    except Exception as e:
        await client.close()
        return {"ok": False, "message": f"Cloudflare sync failed: {e}"}


def process_job(job: dict, worker_id: str):
    kind = job["kind"]
    payload = json.loads(job["payload_json"]) if job.get("payload_json") else {}
    logger.info("Processing job %s kind=%s", job["id"], kind)
    try:
        if kind == "asset.sync":
            result = {"message": "Asset sync not fully implemented", "nodes_checked": 0}
        elif kind == "cloudflare.ip_list.reconcile":
            result = asyncio.run(_do_cloudflare_reconcile(payload))
        elif kind == "probe.reconcile":
            result = {"message": "Probe reconcile not fully implemented"}
        elif kind == "node.verify_initialized":
            result = {"message": "Node verify not fully implemented"}
        elif kind == "github.deploy":
            result = asyncio.run(_do_github_deploy(payload))
        else:
            result = {"message": f"Job kind {kind} not implemented yet"}
        complete_job(job["id"], result)
        logger.info("Job %s completed", job["id"])
    except Exception as e:
        logger.exception("Job %s failed", job["id"])
        fail_job(job["id"], str(e))


def run_worker():
    init_db()
    logger.info("Worker %s started", WORKER_ID)
    while not SHUTDOWN:
        jobs = JobRepository.list_pending(limit=5)
        if not jobs:
            time.sleep(5)
            continue
        for job in jobs:
            if SHUTDOWN:
                break
            if lock_job(job["id"]):
                process_job(job, WORKER_ID)
        time.sleep(1)
    logger.info("Worker %s stopped", WORKER_ID)


if __name__ == "__main__":
    run_worker()
