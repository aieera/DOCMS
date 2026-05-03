"""Shared dedupe + DLQ helpers for classify / NER / embed consumers.

Mirrors `app.dedupe` (which is OCR-specific) but against the generic
`intel_processed_events` ledger. The `consumer` column is what makes
the same event_id safe to process three times — classify, NER, and
embed each get their own row per tenant+event.
"""
from __future__ import annotations

import json
import logging
import uuid
from datetime import datetime, timezone
from typing import Literal

from app.db.pool import get_pool
from app.error_classifier import DLQ_SUBJECT_PREFIX, classify_error_reason  # noqa: F401  (re-export)
from app.events.publisher import publish_cloudevent

log = logging.getLogger(__name__)

Consumer = Literal["classify", "ner", "embed", "auto_tag", "smart_route", "compliance_scan", "lang_detect", "translate", "ocr_quality"]

# DLQ_SUBJECT_PREFIX re-exported from error_classifier (zero-dep module)


async def already_completed(
    *, tenant_id: str, consumer: Consumer, event_id: str
) -> bool:
    """Return True when this (tenant, consumer, event_id) is already
    in status='completed'. Enqueued-but-not-completed rows return False
    so a crashed task can be retried."""
    if not (tenant_id and event_id):
        return False
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT status FROM intel_processed_events
                 WHERE tenant_id = $1 AND consumer = $2 AND event_id = $3
                """,
                tenant_id,
                consumer,
                event_id,
            )
    return bool(row and row["status"] == "completed")


async def mark_enqueued(
    *,
    tenant_id: str,
    consumer: Consumer,
    event_id: str,
    document_id: str,
    version_id: str,
) -> None:
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
                INSERT INTO intel_processed_events
                    (tenant_id, consumer, event_id, document_id, version_id, status, attempts, processed_at)
                VALUES ($1, $2, $3, $4, $5, 'enqueued', 1, NOW())
                ON CONFLICT (tenant_id, consumer, event_id) DO UPDATE
                    SET attempts = intel_processed_events.attempts + 1,
                        processed_at = NOW()
                """,
                tenant_id,
                consumer,
                event_id,
                document_id,
                version_id,
            )


async def mark_completed(
    *, tenant_id: str, consumer: Consumer, event_id: str
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
                UPDATE intel_processed_events
                   SET status='completed', completed_at=NOW()
                 WHERE tenant_id=$1 AND consumer=$2 AND event_id=$3
                """,
                tenant_id,
                consumer,
                event_id,
            )


async def mark_failed(
    *, tenant_id: str, consumer: Consumer, event_id: str, error: str
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
                UPDATE intel_processed_events
                   SET status='failed', last_error=$4, completed_at=NOW()
                 WHERE tenant_id=$1 AND consumer=$2 AND event_id=$3
                """,
                tenant_id,
                consumer,
                event_id,
                (error or "")[:4000],
            )


async def publish_dlq(
    *,
    consumer: Consumer,
    reason: str,
    tenant_id: str,
    document_id: str,
    version_id: str,
    event_id: str,
    error: str,
    attempts: int,
    correlation_id: str = "",
) -> None:
    subject = f"{DLQ_SUBJECT_PREFIX}.{consumer}.{reason}"
    envelope = {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": f"dms.intelligence.{consumer}",
        "type": f"dms.{consumer}.failed.v1",
        "subject": f"version/{version_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "consumer": consumer,
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "original_event_id": event_id,
            "reason": reason,
            "error": (error or "")[:4000],
            "attempts": attempts,
        },
    }
    try:
        await publish_cloudevent(subject, envelope, correlation_id=correlation_id)
        log.error(
            "intel.dlq",
            extra={
                "consumer": consumer,
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "reason": reason,
                "attempts": attempts,
            },
        )
    except Exception:
        log.exception("dlq publish failed; envelope=%s", json.dumps(envelope)[:2000])


# classify_error_reason now lives in app.error_classifier (pure).
# Imported at top of this module and re-exported for existing callers.
