from datetime import datetime, timedelta, timezone
from fastapi import APIRouter, Request, Form, HTTPException, Depends
from fastapi.responses import RedirectResponse
from pydantic import BaseModel

from app.core.config import get_settings
from app.core.security import verify_password, create_admin_token, decode_admin_token
from app.models.repositories import AuditRepository

router = APIRouter()


class LoginForm(BaseModel):
    username: str
    password: str


def get_current_admin(request: Request):
    token = request.cookies.get("admin_token")
    if not token:
        raise HTTPException(status_code=401, detail="未登录")
    payload = decode_admin_token(token)
    if not payload:
        raise HTTPException(status_code=401, detail="登录已过期，请重新登录")
    return payload


@router.post("/api/auth/login")
async def api_login(username: str = Form(...), password: str = Form(...)):
    settings = get_settings()
    if username != settings.admin_username:
        raise HTTPException(status_code=401, detail="用户名或密码错误")
    if not verify_password(password, settings.admin_password_hash):
        raise HTTPException(status_code=401, detail="用户名或密码错误")
    token = create_admin_token({"sub": username})
    AuditRepository.log(
        actor_type="admin",
        actor_id=username,
        action="admin.login",
        summary=f"Admin {username} logged in",
    )
    response = RedirectResponse(url="/dashboard", status_code=302)
    response.set_cookie("admin_token", token, httponly=True, max_age=86400, samesite="lax")
    return response


@router.post("/api/auth/logout")
async def api_logout():
    response = RedirectResponse(url="/login", status_code=302)
    response.delete_cookie("admin_token")
    return response
