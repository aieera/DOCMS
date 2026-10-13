"""REST endpoints — thin shell over Redis-cached manifest + presigned URLs.

All thumbnail / page reads are served as 302 redirects to short-lived
presigned S3 URLs so byte transfer stays on the object store.

H-17 / DEC-4: this API is INTERNAL-ONLY. The tenant arrives as a query
parameter, so only an in-cluster service that has already authorised the
caller (the document service) may call it: every route requires
X-Internal-Service-Key == SEDOC_INTERNAL_API_KEY, or the preview service
key (X-Service-Key == SEDOC_SERVICE_API_KEY) the document service already
holds. The web client never calls it — previews go through the document
service — so the public gateway route is removed (WS-DEPLOY).
"""
from __future__ import annotations

import hmac
import json
import logging
from typing import Optional

import redis
from fastapi import APIRouter, Depends, Header, HTTPException, Query
from fastapi.responses import JSONResponse, RedirectResponse

from app.config import settings
from app.s3_client import s3
from app.tasks.preview import generate_preview

log = logging.getLogger(__name__)


def _key_matches(got: Optional[str], want: str) -> bool:
    """Constant-time compare; an empty configured key matches nothing."""
    if not got or not want:
        return False
    return hmac.compare_digest(got.encode("utf-8"), want.encode("utf-8"))


def require_internal_caller(
    x_internal_service_key: Optional[str] = Header(default=None, alias="X-Internal-Service-Key"),
    x_service_key: Optional[str] = Header(default=None, alias="X-Service-Key"),
) -> None:
    internal_ok = _key_matches(x_internal_service_key, settings.internal_api_key)
    service_ok = _key_matches(x_service_key, settings.service_api_key)
    if not (internal_ok or service_ok):
        raise HTTPException(status_code=401, detail="internal service key required")


router = APIRouter(
    prefix="/api/v1/previews", tags=["previews"],
    dependencies=[Depends(require_internal_caller)],
)


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
    """Tenant comes from the ?tenant_id= the (authenticated, internal)
    caller supplies — see require_internal_caller."""
    if not header_value:
        raise HTTPException(status_code=400, detail="tenant_id required")
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
    source_bucket = manifest.get("source_bucket") or ""
    source_key = manifest.get("source_key") or ""
    if not (source_bucket and source_key):
        # Manifests cached before the source location was persisted have
        # nothing to regenerate from. Enqueueing anyway made the worker
        # HEAD an empty key and overwrite a READY manifest with `failed`.
        raise HTTPException(status_code=409, detail="source location unknown; re-upload or reprocess the version")
    generate_preview.apply_async(
        kwargs={
            "tenant_id": tenant,
            "document_id": document_id,
            "version_id": vid,
            "content_blob_id": manifest.get("content_blob_id", ""),
            "mime_type": manifest.get("mime_type", "application/octet-stream"),
            "region_pin": manifest.get("region", settings.s3_region),
            "storage_bucket": source_bucket,
            "storage_key": source_key,
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
