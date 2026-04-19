"""NATS JetStream publisher used by Celery tasks to emit CloudEvents.

The publisher creates a short-lived JetStream connection per publish — Celery
tasks are not long-lived async contexts, and reusing a connection across task
invocations would require a persistent event loop per worker. The connect +
publish + drain round-trip is ~10-20ms against a local NATS, which is
negligible next to an OCR run (seconds).

The durability story for publish failures is documented as a TODO: we do NOT
want to fail the OCR task on a NATS hiccup, because the ocr_results row is
already committed. A follow-up remediation will add a Python-side outbox.
"""
from __future__ import annotations

import json
import logging
from typing import Any, Mapping, Optional

import nats

from app.config import settings

log = logging.getLogger(__name__)


async def publish_cloudevent(
    subject: str,
    envelope: Mapping[str, Any],
    correlation_id: Optional[str] = None,
) -> None:
    """Publish a CloudEvents-shaped envelope to JetStream.

    `envelope` is the full CloudEvents document (specversion/id/type/etc).
    `correlation_id` is copied into a NATS header so downstream consumers can
    re-hydrate it into their tracing contexts. If the publish round-trip
    fails the exception propagates — callers decide whether to swallow it.
    """
    nc = await nats.connect(settings.nats_url, name=f"{settings.service_name}-pub")
    try:
        js = nc.jetstream()
        headers: dict[str, str] = {}
        if correlation_id:
            headers["correlation-id"] = correlation_id
        payload = json.dumps(envelope, default=str).encode("utf-8")
        await js.publish(subject, payload, headers=headers or None)
    finally:
        await nc.drain()
