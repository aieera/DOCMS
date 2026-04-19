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
from nats.js.errors import NotFoundError

from app.config import settings
from app.tasks.preview import generate_preview

log = logging.getLogger(__name__)


class PreviewConsumer:
    """Lifecycle: connect → ensure stream/consumer → subscribe → enqueue →
    ack on successful Celery apply_async, nak on transient errors.

    Payload shape (CloudEvents v1.0 envelope, .data field unpacked here):
      {
        tenant_id, document_id, version_id, content_blob_id,
        mime_type, region_pin, storage_bucket, storage_key
      }
    """

    def __init__(self) -> None:
        self.nc: Optional[NATS] = None
        self._sub = None
        self._stopped = asyncio.Event()

    async def start(self) -> None:
        self.nc = await nats.connect(settings.nats_url, name=settings.service_name)
        js = self.nc.jetstream()

        # Ensure a durable pull-less push-consumer exists.
        try:
            await js.consumer_info(settings.nats_stream, settings.nats_consumer_durable)
        except NotFoundError:
            await js.add_consumer(
                settings.nats_stream,
                ConsumerConfig(
                    durable_name=settings.nats_consumer_durable,
                    deliver_policy=DeliverPolicy.ALL,
                    filter_subject=settings.nats_subject_uploaded,
                    ack_wait=60,
                    max_deliver=5,
                ),
            )

        self._sub = await js.subscribe(
            subject=settings.nats_subject_uploaded,
            durable=settings.nats_consumer_durable,
            cb=self._on_message,
            manual_ack=True,
        )
        log.info("preview consumer subscribed to %s", settings.nats_subject_uploaded)

    async def _on_message(self, msg) -> None:
        try:
            envelope = json.loads(msg.data.decode("utf-8"))
        except Exception as e:
            log.error("invalid cloudevent payload: %s", e)
            await msg.term()  # malformed — don't retry
            return

        data = envelope.get("data") or envelope  # tolerate un-enveloped

        required = ("tenant_id", "document_id", "version_id",
                    "content_blob_id", "mime_type", "region_pin",
                    "storage_bucket", "storage_key")
        if any(not data.get(k) for k in required):
            log.error("missing fields in event: %s", sorted(k for k in required if not data.get(k)))
            await msg.term()
            return

        try:
            generate_preview.apply_async(
                kwargs={k: data[k] for k in required},
                queue="preview",
            )
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
