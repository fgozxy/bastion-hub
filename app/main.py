import os
import json
from fastapi import FastAPI, Request, Depends, HTTPException
from fastapi.responses import HTMLResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from contextlib import asynccontextmanager

from app.core.db import init_db
from app.core.config import get_settings
from app.api import auth, nodes, agent, jobs, assets, policies, credentials, github, cloudflare
from app.api.auth import get_current_admin, decode_admin_token
from app.services.node_service import get_nodes_summary
from app.models.repositories import (
    NodeRepository, NodeAddressRepository, JobRepository, SSHPolicyRepository,
    CredentialRepository, CredentialBindingRepository,
    GitHubSettingsRepository, GitHubRepoRepository, NodeGitHubBindingRepository,
)


@asynccontextmanager
async def lifespan(app: FastAPI):
    init_db()
    yield


app = FastAPI(title="Bastion Hub", lifespan=lifespan)

# Static files
static_path = os.path.join(os.path.dirname(__file__), "static")
if os.path.isdir(static_path):
    app.mount("/static", StaticFiles(directory=static_path), name="static")

templates = Jinja2Templates(directory=os.path.join(os.path.dirname(__file__), "templates"))


def _compose_status_cn(status: str) -> str:
    if not status:
        return "-"
    s = status.replace("running", "运行中").replace("exited", "已退出").replace("stopped", "已停止")
    return s


templates.env.filters["compose_status_cn"] = _compose_status_cn
templates.env.filters["fromjson"] = lambda s: json.loads(s) if s else {}

# API routes
app.include_router(auth.router)
app.include_router(nodes.router)
app.include_router(agent.router)
app.include_router(jobs.router)
app.include_router(assets.router)
app.include_router(policies.router)
app.include_router(credentials.router)
app.include_router(github.router)
app.include_router(cloudflare.router)


@app.get("/", response_class=HTMLResponse)
async def root(request: Request):
    return RedirectResponse(url="/login")


@app.get("/login", response_class=HTMLResponse)
async def login_page(request: Request):
    token = request.cookies.get("admin_token")
    if token and decode_admin_token(token):
        return RedirectResponse(url="/dashboard")
    return templates.TemplateResponse("login.html", {"request": request})


@app.get("/dashboard", response_class=HTMLResponse)
async def dashboard(request: Request, admin=Depends(get_current_admin)):
    summary = get_nodes_summary()
    from app.core.db import get_cursor
    with get_cursor() as cur:
        cur.execute("SELECT * FROM jobs ORDER BY created_at DESC LIMIT 10")
        recent_jobs = [dict(row) for row in cur.fetchall()]
    return templates.TemplateResponse("dashboard.html", {
        "request": request,
        "admin": admin,
        "summary": summary,
        "recent_jobs": recent_jobs,
    })


@app.get("/nodes", response_class=HTMLResponse)
async def nodes_page(request: Request, admin=Depends(get_current_admin)):
    nodes = NodeRepository.list_all()
    for node in nodes:
        node["addresses"] = NodeAddressRepository.get_by_node(node["id"])
    policies = SSHPolicyRepository.list_all()
    return templates.TemplateResponse("nodes.html", {
        "request": request,
        "admin": admin,
        "nodes": nodes,
        "policies": policies,
    })


@app.get("/nodes/{node_id}", response_class=HTMLResponse)
async def node_detail_page(request: Request, node_id: int, admin=Depends(get_current_admin)):
    node = NodeRepository.get_by_id(node_id)
    if not node:
        raise HTTPException(status_code=404, detail="Node not found")
    node["addresses"] = NodeAddressRepository.get_by_node(node_id)
    from app.core.db import get_cursor
    with get_cursor() as cur:
        cur.execute("SELECT * FROM docker_snapshots WHERE node_id = ? ORDER BY collected_at DESC LIMIT 1", (node_id,))
        docker = cur.fetchone()
        if docker:
            docker = dict(docker)
        cur.execute("SELECT * FROM jobs WHERE payload_json LIKE ? ORDER BY created_at DESC LIMIT 10", (f'%"node_id": {node_id}%',))
        node_jobs = [dict(row) for row in cur.fetchall()]
    from app.models.repositories import ComposeProjectRepository
    compose_projects = ComposeProjectRepository.get_by_node(node_id)
    policy = None
    if node.get("policy_id"):
        policy = SSHPolicyRepository.get_by_id(node["policy_id"])
    credential_bindings = CredentialBindingRepository.get_by_node(node_id)
    github_bindings = NodeGitHubBindingRepository.get_by_node(node_id)
    github_repos = GitHubRepoRepository.list_all()
    from app.models.repositories import MaintenanceLogRepository
    maintenance_logs = MaintenanceLogRepository.get_by_node(node_id, limit=5)
    return templates.TemplateResponse("node_detail.html", {
        "request": request,
        "admin": admin,
        "node": node,
        "docker": docker,
        "node_jobs": node_jobs,
        "compose_projects": compose_projects,
        "policy": policy,
        "credential_bindings": credential_bindings,
        "github_bindings": github_bindings,
        "github_repos": github_repos,
        "maintenance_logs": maintenance_logs,
    })


@app.get("/jobs", response_class=HTMLResponse)
async def jobs_page(request: Request, admin=Depends(get_current_admin)):
    from app.core.db import get_cursor
    with get_cursor() as cur:
        cur.execute("SELECT * FROM jobs ORDER BY created_at DESC LIMIT 200")
        jobs = [dict(row) for row in cur.fetchall()]
    return templates.TemplateResponse("jobs.html", {
        "request": request,
        "admin": admin,
        "jobs": jobs,
    })


@app.get("/bootstrap", response_class=HTMLResponse)
async def bootstrap_page(request: Request, admin=Depends(get_current_admin)):
    return templates.TemplateResponse("bootstrap.html", {
        "request": request,
        "admin": admin,
        "command": None,
    })


@app.get("/settings", response_class=HTMLResponse)
async def settings_page(request: Request, admin=Depends(get_current_admin)):
    settings = get_settings()
    return templates.TemplateResponse("settings.html", {
        "request": request,
        "admin": admin,
        "settings": settings,
    })


@app.get("/policies", response_class=HTMLResponse)
async def policies_page(request: Request, admin=Depends(get_current_admin)):
    policies = SSHPolicyRepository.list_all()
    return templates.TemplateResponse("policies.html", {
        "request": request,
        "admin": admin,
        "policies": policies,
    })


@app.get("/policies/{policy_id}", response_class=HTMLResponse)
async def policy_detail_page(request: Request, policy_id: int, admin=Depends(get_current_admin)):
    policy = SSHPolicyRepository.get_by_id(policy_id)
    if not policy:
        raise HTTPException(status_code=404, detail="策略不存在")
    nodes = NodeRepository.list_all()
    return templates.TemplateResponse("policy_detail.html", {
        "request": request,
        "admin": admin,
        "policy": policy,
        "nodes": nodes,
    })


@app.get("/credentials", response_class=HTMLResponse)
async def credentials_page(request: Request, admin=Depends(get_current_admin)):
    credentials = CredentialRepository.list_all()
    return templates.TemplateResponse("credentials.html", {
        "request": request,
        "admin": admin,
        "credentials": credentials,
    })


@app.get("/credentials/{credential_id}", response_class=HTMLResponse)
async def credential_detail_page(request: Request, credential_id: int, admin=Depends(get_current_admin)):
    credential = CredentialRepository.get_by_id(credential_id)
    if not credential:
        raise HTTPException(status_code=404, detail="凭证不存在")
    bindings = CredentialBindingRepository.get_by_credential(credential_id)
    nodes = NodeRepository.list_all()
    return templates.TemplateResponse("credential_detail.html", {
        "request": request,
        "admin": admin,
        "credential": credential,
        "bindings": bindings,
        "nodes": nodes,
    })
