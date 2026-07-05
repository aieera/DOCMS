"""Dedupe + DLQ helpers for the intelligence consumer.

At-least-once delivery means the NATS broker may redeliver a
`dms.version.uploaded.v1` that this service has already enqueued. The
ocr_processed_events table is the source of truth: check before
enqueue; upsert on success; fall through (no-op) if the row already
exists.

Retention: rows older than 14 days are cleaned by
`gc_processed_events` run from a Temporal cron (Wave 8.1) or manually.
"""
from __future__ import annotations

import asyncio
import json
import logging
import uuid
from datetime import datetime, timezone
from typing import Optional

from app.db.pool import get_pool
from app.events.publisher import publish_cloudevent
from app.events.subjects import OCR_FAILED_SUBJECT
from app.metrics import ocr_dedupe_hits_total, ocr_dlq_total
from app.storage_uri import parse_storage_uri  # re-exported for callers

log = logging.getLogger(__name__)

OCR_DLQ_SUBJECT = "dms.dlq.intel_events.ocr"


async def already_processed(
    tenant_id: str, event_id: str, *, completed_only: bool = False
) -> bool:
    """Return True when this event was seen before.

    When `completed_only` is True, re-enqueue is allowed for rows in
    status='enqueued' (the previous run crashed before marking
    completed). Use False when you want to suppress any duplicate
    enqueue regardless of outcome.
    """
    if not tenant_id or not event_id:
        return False
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            q = "SELECT status FROM ocr_processed_events WHERE tenant_id = $1 AND event_id = $2"
            row = await conn.fetchrow(q, tenant_id, event_id)
    if row is None:
        return False
    if completed_only:
        return row["status"] == "completed"
    return True


async def mark_enqueued(
    *,
    tenant_id: str,
    event_id: str,
    document_id: str,
    version_id: str,
) -> None:
    """Insert (or bump attempts on) the dedupe row at enqueue time."""
    if not event_id:
        event_id = str(uuid.uuid4())
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO ocr_processed_events
                    (tenant_id, event_id, document_id, version_id, status, attempts, processed_at)
                VALUES ($1, $2, $3, $4, 'enqueued', 1, NOW())
                ON CONFLICT (tenant_id, event_id) DO UPDATE
                    SET attempts = ocr_processed_events.attempts + 1,
                        processed_at = NOW()
                """,
                tenant_id,
                event_id,
                document_id,
                version_id,
            )


async def mark_completed(
    *, tenant_id: str, event_id: str
) -> None:
    if not (tenant_id and event_id):
        return
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE ocr_processed_events
                   SET status = 'completed', completed_at = NOW()
                 WHERE tenant_id = $1 AND event_id = $2
                """,
                tenant_id,
                event_id,
            )


async def mark_failed(
    *, tenant_id: str, event_id: str, error: str
) -> None:
    if not (tenant_id and event_id):
        return
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE ocr_processed_events
                   SET status = 'failed', last_error = $3, completed_at = NOW()
                 WHERE tenant_id = $1 AND event_id = $2
                """,
                tenant_id,
                event_id,
                (error or "")[:4000],
            )


async def publish_dlq(
    *,
    reason: str,
    tenant_id: str,
    document_id: str,
    version_id: str,
    event_id: str,
    error: str,
    attempts: int,
    correlation_id: str = "",
) -> None:
    """Publish the poisoned event to the DLQ subject with full context."""
    envelope = {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "dms.intelligence",
        "type": OCR_FAILED_SUBJECT,
        "subject": f"version/{version_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "original_event_id": event_id,
            "reason": reason,
            "error": (error or "")[:4000],
            "attempts": attempts,
            "dlq_ts": datetime.now(timezone.utc).isoformat(),
        },
    }
    dlq_subject = f"{OCR_DLQ_SUBJECT}.{reason}"
    try:
        await publish_cloudevent(dlq_subject, envelope, correlation_id=correlation_id)
        ocr_dlq_total.labels(reason=reason).inc()
        log.error(
            "ocr.dlq",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "reason": reason,
                "attempts": attempts,
            },
        )
    except Exception:
        # Last-resort: log the full envelope so operators can manually
        # replay. We deliberately do NOT raise — a DLQ publish failure
        # must not block ACK of the original message.
        log.exception("dlq publish failed; envelope=%s", json.dumps(envelope)[:2000])


def record_dedupe_hit() -> None:
    ocr_dedupe_hits_total.inc()


__all__ = [
    "OCR_DLQ_SUBJECT",
    "already_processed",
    "mark_enqueued",
    "mark_completed",
    "mark_failed",
    "publish_dlq",
    "record_dedupe_hit",
    "parse_storage_uri",
]
