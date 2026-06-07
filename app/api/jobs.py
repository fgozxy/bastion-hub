from typing import Optional
from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from app.api.auth import get_current_admin
from app.models.repositories import JobRepository

router = APIRouter()


class JobRetry(BaseModel):
    pass


@router.get("/api/jobs")
async def list_jobs(admin=Depends(get_current_admin)):
    # Simple list all for now
    from app.core.db import get_cursor
    with get_cursor() as cur:
        cur.execute("SELECT * FROM jobs ORDER BY created_at DESC LIMIT 100")
        jobs = [dict(row) for row in cur.fetchall()]
    return {"jobs": jobs}


@router.get("/api/jobs/{job_id}")
async def get_job(job_id: int, admin=Depends(get_current_admin)):
    from app.core.db import get_cursor
    with get_cursor() as cur:
        cur.execute("SELECT * FROM jobs WHERE id = ?", (job_id,))
        row = cur.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="任务不存在")
    return dict(row)


@router.post("/api/jobs/{job_id}/retry")
async def retry_job(job_id: int, admin=Depends(get_current_admin)):
    from app.core.db import get_cursor
    from datetime import datetime, timezone
    t = datetime.now(timezone.utc).isoformat()
    with get_cursor() as cur:
        cur.execute(
            "UPDATE jobs SET status = 'pending', attempts = 0, updated_at = ? WHERE id = ?",
            (t, job_id),
        )
    return {"ok": True}
