"""REST endpoints — thin shell over Redis-cached manifest + presigned URLs.

All thumbnail / page reads are served as 302 redirects to short-lived
presigned S3 URLs so byte transfer stays on the object store.
"""
from __future__ import annotations

import json
import logging
from typing import Optional

import redis
from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import JSONResponse, RedirectResponse

from app.config import settings
from app.s3_client import s3
from app.tasks.preview import generate_preview

log = logging.getLogger(__name__)
router = APIRouter(prefix="/api/v1/previews", tags=["previews"])


def _redis() -> redis.Redis:
    return redis.Redis.from_url(settings.redis_cache_url)


def _load_manifest(tenant_id: str, version_id: str) -> Optional[dict]:
    r = _redis()
    try:
        raw = r.get(f"preview:{tenant_id}:{version_id}")
    finally:
        r.close()
    if not raw:
        return None
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return None


def _require_tenant(header_value: Optional[str]) -> str:
    """Tenant comes from the X-Tenant-ID header set by the gateway. No
    auth logic here — the gateway enforces it before traffic lands."""
    if not header_value:
        raise HTTPException(status_code=400, detail="X-Tenant-ID header required")
    return header_value


# NOTE: the URL scheme in the spec is per-document, but previews are
# per-version. We accept an optional ?version_id=... and default to the
# "latest" manifest cached under the doc id. Document-service writes that
# alias on version promotion.


def _resolve_version_id(tenant_id: str, document_id: str, version_id: Optional[str]) -> str:
    if version_id:
        return version_id
    r = _redis()
    try:
        v = r.get(f"preview_latest:{tenant_id}:{document_id}")
    finally:
        r.close()
    if not v:
        raise HTTPException(status_code=404, detail="no preview available for document")
    return v.decode("utf-8") if isinstance(v, bytes) else v


@router.get("/{document_id}/thumbnail")
def get_thumbnail(
    document_id: str,
    x_tenant_id: Optional[str] = Query(default=None, alias="tenant_id"),
    version_id: Optional[str] = None,
):
    tenant = _require_tenant(x_tenant_id)
    vid = _resolve_version_id(tenant, document_id, version_id)
    manifest = _load_manifest(tenant, vid)
    if not manifest or manifest.get("status") != "ready":
        raise HTTPException(status_code=404, detail="thumbnail not ready")
    key = manifest.get("thumbnail_key")
    bucket = manifest.get("bucket")
    if not (key and bucket):
        raise HTTPException(status_code=404, detail="thumbnail missing")
    url = s3.presigned_get(bucket, key, settings.preview_presign_ttl_seconds)
    return RedirectResponse(url=url, status_code=302)


@router.get("/{document_id}/pages/{page_number}")
def get_page(
    document_id: str,
    page_number: int,
    x_tenant_id: Optional[str] = Query(default=None, alias="tenant_id"),
    version_id: Optional[str] = None,
):
    tenant = _require_tenant(x_tenant_id)
    vid = _resolve_version_id(tenant, document_id, version_id)
    manifest = _load_manifest(tenant, vid)
    if not manifest or manifest.get("status") != "ready":
        raise HTTPException(status_code=404, detail="preview not ready")
    keys = manifest.get("preview_page_keys") or []
    if page_number < 1 or page_number > len(keys):
        raise HTTPException(status_code=404, detail="page out of range")
    url = s3.presigned_get(manifest["bucket"], keys[page_number - 1],
                           settings.preview_presign_ttl_seconds)
    return RedirectResponse(url=url, status_code=302)


@router.post("/{document_id}/regenerate")
def regenerate(
    document_id: str,
    x_tenant_id: Optional[str] = Query(default=None, alias="tenant_id"),
    version_id: Optional[str] = None,
):
    tenant = _require_tenant(x_tenant_id)
    vid = _resolve_version_id(tenant, document_id, version_id)
    manifest = _load_manifest(tenant, vid)
    if not manifest:
        raise HTTPException(status_code=404, detail="unknown version")
    generate_preview.apply_async(
        kwargs={
            "tenant_id": tenant,
            "document_id": document_id,
            "version_id": vid,
            "content_blob_id": manifest.get("content_blob_id", ""),
            "mime_type": manifest.get("mime_type", "application/octet-stream"),
            "region_pin": manifest.get("region", settings.s3_region),
            "storage_bucket": manifest.get("source_bucket") or manifest.get("bucket", ""),
            "storage_key": manifest.get("source_key", ""),
        },
        queue="preview",
    )
    return JSONResponse({"status": "queued", "version_id": vid})


@router.get("/{document_id}/status")
def status(
    document_id: str,
    x_tenant_id: Optional[str] = Query(default=None, alias="tenant_id"),
    version_id: Optional[str] = None,
):
    tenant = _require_tenant(x_tenant_id)
    vid = _resolve_version_id(tenant, document_id, version_id)
    manifest = _load_manifest(tenant, vid)
    if not manifest:
        raise HTTPException(status_code=404, detail="no preview state")
    return {
        "status": manifest.get("status", "unknown"),
        "page_count": manifest.get("page_count"),
        "error": manifest.get("error"),
        "generated_at": manifest.get("generated_at"),
        "version_id": vid,
    }
