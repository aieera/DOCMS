"""Celery task that generates the full preview bundle for one version.

Flow:
  1. Resolve processor by MIME type.
  2. Download source blob to a tempdir.
  3. Run the processor.
  4. Upload artifacts (thumb, pages, metadata.json) to S3.
  5. Cache the manifest in Redis for the REST layer to serve.
  6. Publish dms.version.preview_ready.v1 via NATS (best-effort).
  7. Always clean the tempdir.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import shutil
import tempfile
from datetime import datetime, timezone

import redis
from celery.exceptions import SoftTimeLimitExceeded

from app.config import settings
from app.processors import ProcessResult
from app.processors import email as email_proc
from app.processors import image as image_proc
from app.processors import office as office_proc
from app.processors import pdf as pdf_proc
from app.processors import text as text_proc
from app.processors import video as video_proc
from app.s3_client import preview_bucket, preview_key, s3
from app.worker import celery_app

log = logging.getLogger(__name__)

_OFFICE_MIME_PREFIXES = (
    "application/vnd.openxmlformats-officedocument.",
    "application/vnd.ms-",
    "application/msword",
    "application/vnd.oasis.opendocument.",
)

_TEXT_MIME_PREFIXES = ("text/",)
_TEXT_MIME_EXACT = {
    "application/json",
    "application/xml",
    "application/x-yaml",
    "application/x-sh",
    "application/javascript",
}


def _select_processor(mime: str):
    """Returns (module, kwargs_for_process). Unknown → None."""
    m = (mime or "").lower()
    if m == "application/pdf":
        return pdf_proc, {}
    if m.startswith("image/"):
        return image_proc, {}
    if m.startswith("video/"):
        return video_proc, {}
    if m == "message/rfc822" or m == "application/vnd.ms-outlook":
        return email_proc, {"mime_type": m}
    if any(m.startswith(p) for p in _OFFICE_MIME_PREFIXES):
        return office_proc, {}
    if m in _TEXT_MIME_EXACT or any(m.startswith(p) for p in _TEXT_MIME_PREFIXES):
        return text_proc, {"mime_type": m}
    return None, {}


def _cache_manifest(redis_url: str, tenant_id: str, version_id: str, manifest: dict) -> None:
    r = redis.Redis.from_url(redis_url)
    try:
        r.setex(
            f"preview:{tenant_id}:{version_id}",
            settings.preview_cache_ttl_seconds,
            json.dumps(manifest),
        )
    finally:
        r.close()


async def _publish_ready(subject: str, payload: dict) -> None:
    """Fire-and-forget NATS publish. Failures log-only — the manifest in
    Redis is the source of truth."""
    try:
        import nats
        nc = await nats.connect(settings.nats_url)
        try:
            await nc.publish(subject, json.dumps(payload).encode("utf-8"))
            await nc.flush()
        finally:
            await nc.close()
    except Exception as e:
        log.warning("nats publish preview_ready failed: %s", e)


def _publish_ready_sync(subject: str, payload: dict) -> None:
    try:
        asyncio.run(_publish_ready(subject, payload))
    except RuntimeError:
        # Already in an event loop (e.g. running under pytest-asyncio).
        loop = asyncio.new_event_loop()
        try:
            loop.run_until_complete(_publish_ready(subject, payload))
        finally:
            loop.close()


def _upload_artifacts(bucket: str, tenant_id: str, document_id: str,
                     version_id: str, result: ProcessResult) -> dict:
    """Pushes thumb + page renders + metadata.json. Returns the manifest
    that gets cached in Redis."""
    s3.ensure_bucket(bucket)
    uploaded = {
        "thumbnail_key": None,
        "preview_page_keys": [],
        "metadata_key": None,
    }
    if result.thumbnail_path:
        k = preview_key(tenant_id, document_id, version_id, "thumb.jpg")
        s3.upload_file(result.thumbnail_path, bucket, k, "image/jpeg")
        uploaded["thumbnail_key"] = k
    for i, p in enumerate(result.preview_paths, start=1):
        ext = os.path.splitext(p)[1].lstrip(".") or "png"
        ctype = "image/png" if ext == "png" else "image/jpeg"
        k = preview_key(tenant_id, document_id, version_id, f"page_{i}.{ext}")
        s3.upload_file(p, bucket, k, ctype)
        uploaded["preview_page_keys"].append(k)
    meta_payload = {
        "metadata": result.metadata,
        "status": result.status,
        "text_preview": result.text_preview,
        "language_hint": result.language_hint,
    }
    meta_key = preview_key(tenant_id, document_id, version_id, "metadata.json")
    import io
    s3.upload_fileobj(
        io.BytesIO(json.dumps(meta_payload).encode("utf-8")),
        bucket, meta_key, "application/json",
    )
    uploaded["metadata_key"] = meta_key
    return uploaded


@celery_app.task(
    name="app.tasks.preview.generate_preview",
    bind=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=120,
    retry_jitter=False,
)
def generate_preview(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    content_blob_id: str,
    mime_type: str,
    region_pin: str,
    storage_bucket: str,
    storage_key: str,
):
    """Main entry point. Celery re-delivers on worker crash (task_acks_late)
    and retries with 5s→30s→120s backoff on exceptions.

    Non-retryable outcomes (password_protected, too_large, failed) set the
    manifest status and return — do NOT raise, so Celery does not retry."""
    workdir = tempfile.mkdtemp(prefix="preview-")
    src_path = os.path.join(workdir, "source")

    manifest = {
        "tenant_id": tenant_id,
        "document_id": document_id,
        "version_id": version_id,
        "content_blob_id": content_blob_id,
        "mime_type": mime_type,
        "region": region_pin,
        "status": "processing",
        "generated_at": datetime.now(timezone.utc).isoformat(),
    }

    try:
        # --- Size gate ------------------------------------------------------
        try:
            head = s3.head(storage_bucket, storage_key)
            size = int(head.get("ContentLength", 0))
        except Exception as e:
            log.error("head failed tenant=%s version=%s err=%s",
                      tenant_id, version_id, e)
            manifest["status"] = "failed"
            manifest["error"] = "source object not found"
            _cache_manifest(settings.redis_cache_url, tenant_id, version_id, manifest)
            return manifest

        if size > settings.max_file_size_bytes:
            manifest["status"] = "too_large"
            manifest["error"] = f"{size} bytes exceeds limit"
            _cache_manifest(settings.redis_cache_url, tenant_id, version_id, manifest)
            return manifest

        # --- Download -------------------------------------------------------
        s3.download_to(storage_bucket, storage_key, src_path)

        # --- Dispatch -------------------------------------------------------
        proc, kwargs = _select_processor(mime_type)
        if proc is None:
            manifest["status"] = "unsupported"
            manifest["error"] = f"no processor for mime {mime_type}"
            _cache_manifest(settings.redis_cache_url, tenant_id, version_id, manifest)
            return manifest

        try:
            result: ProcessResult = proc.process(src_path, workdir, **kwargs)
        except SoftTimeLimitExceeded:
            manifest["status"] = "conversion_timeout"
            manifest["error"] = "soft time limit exceeded"
            _cache_manifest(settings.redis_cache_url, tenant_id, version_id, manifest)
            raise  # let Celery surface the timeout

        if result.status != "ready":
            manifest["status"] = result.status
            manifest["error"] = result.error
            _cache_manifest(settings.redis_cache_url, tenant_id, version_id, manifest)
            return manifest

        # --- Upload ---------------------------------------------------------
        bucket = preview_bucket(region_pin)
        uploaded = _upload_artifacts(bucket, tenant_id, document_id, version_id, result)

        manifest.update({
            "status": "ready",
            "bucket": bucket,
            "thumbnail_key": uploaded["thumbnail_key"],
            "preview_page_keys": uploaded["preview_page_keys"],
            "metadata_key": uploaded["metadata_key"],
            "page_count": result.metadata.get("page_count", 1),
            "has_text": result.metadata.get("has_text", False),
            "processor_metadata": result.metadata,
        })
        if result.text_preview:
            manifest["text_preview"] = result.text_preview
            manifest["language_hint"] = result.language_hint

        _cache_manifest(settings.redis_cache_url, tenant_id, version_id, manifest)

        _publish_ready_sync(
            settings.nats_subject_preview_ready,
            {
                "specversion": "1.0",
                "type": settings.nats_subject_preview_ready,
                "source": f"/vaultdms/{settings.service_name}",
                "id": f"{tenant_id}:{version_id}:ready",
                "time": manifest["generated_at"],
                "datacontenttype": "application/json",
                "data": {
                    "tenant_id": tenant_id,
                    "document_id": document_id,
                    "version_id": version_id,
                    "content_blob_id": content_blob_id,
                    "bucket": bucket,
                    "thumbnail_key": uploaded["thumbnail_key"],
                    "page_count": manifest["page_count"],
                },
            },
        )
        return manifest

    finally:
        shutil.rmtree(workdir, ignore_errors=True)


# Exposed for tests.
__all__ = ["generate_preview", "_select_processor"]
