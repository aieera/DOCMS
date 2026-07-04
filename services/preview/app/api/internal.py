"""Internal, service-authed stamping endpoints (§5).

Only the document service calls these — it owns viewer identity + the
tenant/classification watermark config, substitutes the final text, and asks
this service (which owns the rendering libs + the cached base pages) to draw it.
Never exposed through the public gateway; gated by a shared service key.
"""
from __future__ import annotations

import logging
from typing import Optional

from botocore.exceptions import BotoCoreError, ClientError
from fastapi import APIRouter, File, Form, Header, HTTPException, Response, UploadFile
from pydantic import BaseModel, Field

from app.api.routes import _load_manifest  # reuse the Redis manifest reader
from app.config import settings
from app.s3_client import s3
from app.watermark import WatermarkSpec, stamp_image_bytes, stamp_pdf_bytes

log = logging.getLogger(__name__)
router = APIRouter(prefix="/api/v1/previews/internal", tags=["previews-internal"])


def _require_service_key(x_service_key: Optional[str]) -> None:
    # Fail closed: an unset server-side key rejects everything.
    if not settings.service_api_key or x_service_key != settings.service_api_key:
        raise HTTPException(status_code=401, detail="invalid service key")


class PageStampRequest(BaseModel):
    tenant_id: str
    document_id: str
    version_id: str
    page_number: int = Field(default=1, ge=1)
    thumbnail: bool = False
    text: str = Field(default="", max_length=1000)
    opacity: int = Field(default=15, ge=0, le=100)
    rotation_deg: int = Field(default=30, ge=-360, le=360)
    tile: bool = True
    font_size: int = Field(default=28, ge=1, le=300)
    color: str = "#808080"

    def spec(self) -> WatermarkSpec:
        return WatermarkSpec(
            text=self.text, opacity=self.opacity, rotation_deg=self.rotation_deg,
            tile=self.tile, font_size=self.font_size, color=self.color,
        )


@router.post("/watermark/page")
def watermark_page(
    req: PageStampRequest,
    x_service_key: Optional[str] = Header(default=None, alias="X-Service-Key"),
):
    """Stamp the viewer's identity onto a cached rendered page (or thumbnail)."""
    _require_service_key(x_service_key)
    manifest = _load_manifest(req.tenant_id, req.version_id)
    if not manifest or manifest.get("status") != "ready":
        raise HTTPException(status_code=404, detail="preview not ready")
    bucket = manifest.get("bucket")
    if req.thumbnail:
        key = manifest.get("thumbnail_key")
    else:
        keys = manifest.get("preview_page_keys") or []
        if req.page_number < 1 or req.page_number > len(keys):
            raise HTTPException(status_code=404, detail="page out of range")
        key = keys[req.page_number - 1]
    if not (bucket and key):
        raise HTTPException(status_code=404, detail="page missing")
    try:
        base = s3.get_bytes(bucket, key)
    except (BotoCoreError, ClientError) as e:
        log.warning("watermark: base image fetch failed: %s", e)
        raise HTTPException(status_code=502, detail="base image fetch failed")
    try:
        stamped = stamp_image_bytes(base, req.spec())
    except Exception:  # noqa: BLE001 — return a clean error, never a stacktrace
        log.exception("watermark: page stamp failed")
        raise HTTPException(status_code=500, detail="stamp failed")
    # no-store: a per-viewer artifact must never be cached by a shared proxy.
    return Response(content=stamped, media_type="image/png", headers={"Cache-Control": "no-store"})


@router.post("/watermark/pdf")
def watermark_pdf(
    file: UploadFile = File(...),
    text: str = Form(...),
    opacity: int = Form(15),
    rotation_deg: int = Form(30),
    tile: bool = Form(True),
    font_size: int = Form(28),
    color: str = Form("#808080"),
    x_service_key: Optional[str] = Header(default=None, alias="X-Service-Key"),
):
    """Burn the viewer's identity into every page of a PDF (download / print)."""
    _require_service_key(x_service_key)
    pdf_bytes = file.file.read()
    if not pdf_bytes:
        raise HTTPException(status_code=400, detail="empty pdf")
    spec = WatermarkSpec(
        text=text, opacity=opacity, rotation_deg=rotation_deg, tile=tile,
        font_size=font_size, color=color,
    )
    try:
        stamped = stamp_pdf_bytes(pdf_bytes, spec)
    except Exception:  # noqa: BLE001 — malformed PDF etc. -> clean error, no stacktrace
        log.exception("watermark: pdf stamp failed")
        raise HTTPException(status_code=500, detail="pdf stamp failed")
    return Response(content=stamped, media_type="application/pdf", headers={"Cache-Control": "no-store"})
