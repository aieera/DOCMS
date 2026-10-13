"""NATS JetStream consumer — enqueues generate_preview on every
dms.version.uploaded.v1 event.

Runs as a background task under the FastAPI app. One durable consumer per
service name so restarts don't drop deliveries.
"""
from __future__ import annotations

import asyncio
import json
import logging
from typing import Optional

import nats
from nats.aio.client import Client as NATS
from nats.js.api import ConsumerConfig, DeliverPolicy

from app.config import settings
from app.tasks.preview import generate_preview

log = logging.getLogger(__name__)


def _resolve_location(data: dict) -> tuple[str, str]:
    """(bucket, key) from the document service's `storage_uri`
    (s3://bucket/key) or the legacy storage_bucket/storage_key pair;
    ("", "") when neither is usable."""
    uri = data.get("storage_uri") or ""
    if uri:
        if not uri.startswith("s3://"):
            return "", ""
        bucket, _, key = uri[len("s3://"):].partition("/")
        return (bucket, key) if bucket and key else ("", "")
    return data.get("storage_bucket") or "", data.get("storage_key") or ""


def build_task_kwargs(envelope: dict) -> Optional[dict]:
    """Map a dms.version.uploaded.v1 event to generate_preview kwargs, or
    None when it cannot be processed.

    The document service publishes a CloudEvents envelope whose `data` is
    model.VersionUploadedPayload: storage_uri + content_blob_id, no
    storage_bucket / storage_key / region_pin. generate_preview resolves
    the authoritative location, region and envelope metadata from
    content_blobs by content_blob_id, so either a location or a blob id is
    enough to proceed."""
    data = envelope.get("data") or envelope  # tolerate un-enveloped
    if not isinstance(data, dict):
        return None
    ids = {k: data.get(k) or "" for k in ("tenant_id", "document_id", "version_id", "mime_type")}
    if not all(ids.values()):
        return None
    bucket, key = _resolve_location(data)
    blob_id = data.get("content_blob_id") or ""
    if not (bucket and key) and not blob_id:
        return None
    return {
        **ids,
        "content_blob_id": blob_id,
        "region_pin": data.get("region_pin") or "",
        "storage_bucket": bucket,
        "storage_key": key,
    }


class PreviewConsumer:
    """Lifecycle: connect → durable subscribe → enqueue → ack on successful
    Celery apply_async, nak on transient errors, term on unusable events.
    See build_task_kwargs for the payload shape."""

    def __init__(self) -> None:
        self.nc: Optional[NATS] = None
        self._sub = None
        self._stopped = asyncio.Event()

    async def start(self) -> None:
        self.nc = await nats.connect(settings.nats_url, name=settings.service_name)
        js = self.nc.jetstream()
        # Subscribe by subject and let JetStream resolve the stream (the
        # document service publishes on DOC_EVENTS). The old explicit
        # consumer_info/add_consumer on a hard-coded "SEDOC" stream failed
        # before the subscription was ever made.
        self._sub = await js.subscribe(
            subject=settings.nats_subject_uploaded,
            durable=settings.nats_consumer_durable,
            cb=self._on_message,
            manual_ack=True,
            config=ConsumerConfig(
                deliver_policy=DeliverPolicy.ALL,
                ack_wait=60,
                max_deliver=5,
            ),
        )
        log.info("preview consumer subscribed to %s", settings.nats_subject_uploaded)

    async def _on_message(self, msg) -> None:
        try:
            envelope = json.loads(msg.data.decode("utf-8"))
        except Exception as e:
            log.error("invalid cloudevent payload: %s", e)
            await msg.term()  # malformed — don't retry
            return

        kwargs = build_task_kwargs(envelope) if isinstance(envelope, dict) else None
        if kwargs is None:
            log.error("unusable version.uploaded event; term'd")
            await msg.term()
            return

        try:
            generate_preview.apply_async(kwargs=kwargs, queue="preview")
            await msg.ack()
        except Exception as e:
            log.exception("failed to enqueue preview task: %s", e)
            await msg.nak(delay=5)

    async def stop(self) -> None:
        if self._sub is not None:
            try:
                await self._sub.unsubscribe()
            except Exception:
                pass
        if self.nc is not None:
            await self.nc.drain()
        self._stopped.set()
