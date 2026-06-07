import hashlib
from fastapi import APIRouter, Request
from fastapi.responses import PlainTextResponse

router = APIRouter()


@router.get("/assets/bootstrap.sh")
async def get_bootstrap(request: Request):
    from pathlib import Path
    path = Path(__file__).parent.parent.parent / "agent" / "bootstrap.sh"
    if not path.exists():
        return PlainTextResponse("# bootstrap.sh not found\n", status_code=404)
    return PlainTextResponse(path.read_text(), media_type="text/x-sh")


@router.get("/assets/agent.sh")
async def get_agent(request: Request):
    from pathlib import Path
    path = Path(__file__).parent.parent.parent / "agent" / "agent.sh"
    if not path.exists():
        return PlainTextResponse("# agent.sh not found\n", status_code=404)
    return PlainTextResponse(path.read_text(), media_type="text/x-sh")


@router.get("/assets/policy-apply.sh")
async def get_policy_apply(request: Request):
    from pathlib import Path
    path = Path(__file__).parent.parent.parent / "agent" / "policy-apply.sh"
    if not path.exists():
        return PlainTextResponse("# policy-apply.sh not found\n", status_code=404)
    return PlainTextResponse(path.read_text(), media_type="text/x-sh")


@router.get("/assets/maintenance.sh")
async def get_maintenance(request: Request):
    from pathlib import Path
    path = Path(__file__).parent.parent.parent / "agent" / "maintenance.sh"
    if not path.exists():
        return PlainTextResponse("# maintenance.sh not found\n", status_code=404)
    return PlainTextResponse(path.read_text(), media_type="text/x-sh")


@router.get("/assets/bastion-hub.pub")
async def get_ssh_pubkey(request: Request):
    from pathlib import Path
    path = Path(__file__).parent.parent.parent.parent / "data" / "ssh" / "bastion-hub.pub"
    if not path.exists():
        return PlainTextResponse("# pubkey not found\n", status_code=404)
    return PlainTextResponse(path.read_text(), media_type="text/plain")


@router.get("/assets/checksums")
async def get_checksums():
    from pathlib import Path
    agent_dir = Path(__file__).parent.parent.parent / "agent"
    checksums = {}
    for filename in ("agent.sh", "policy-apply.sh", "maintenance.sh"):
        fpath = agent_dir / filename
        if fpath.exists():
            h = hashlib.sha256()
            h.update(fpath.read_bytes())
            checksums[filename] = f"sha256:{h.hexdigest()}"
    return {"checksums": checksums}
