"""Language detection — runs post-OCR, deterministic, cheap (~10ms).

Triggered by: dms.version.ocr_completed.v1
Persists to:  document_languages
Emits:        dms.language.detected.v1

Uses the langdetect library (port of Google's language-detection,
~3 MB pure-Python). Seeded for determinism so re-runs give the
same answer.
"""
from __future__ import annotations

import asyncio
import json
import logging
import time
import uuid
from datetime import datetime, timezone
from typing import Any

from app.error_classifier import classify_error_reason
from app.intel_dedupe import (
    already_completed,
    mark_completed,
    mark_enqueued,
    mark_failed,
    publish_dlq,
)
from app.worker import celery_app
from app.db.pool import get_pool

log = logging.getLogger(__name__)

LANGUAGE_DETECTED_SUBJECT = "dms.language.detected.v1"
CONSUMER = "lang_detect"

# Minimum characters before we trust detection. Below this we still
# detect but flag low confidence — tiny scraps are unreliable.
MIN_CHARS_FOR_CONFIDENCE = 50

# Cap text fed to detector — first ~10k chars is more than enough and
# keeps the call ~10ms even for 500-page documents.
DETECT_TEXT_CAP = 10_000


@celery_app.task(
    name="app.tasks.lang_detect.lang_detect",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=60,
    time_limit=120,
)
def lang_detect(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id, document_id=document_id, version_id=version_id,
        event_id=event_id, correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 3,
    ))


async def _run_async(
    *, tenant_id, document_id, version_id, event_id, correlation_id,
    attempt, is_terminal,
) -> dict:
    start = time.monotonic()
    if not (tenant_id and document_id and version_id):
        return {"status": "skipped", "reason": "missing ids"}

    try:
        if event_id and await already_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ):
            return {"status": "duplicate", "event_id": event_id}

        await mark_enqueued(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
            document_id=document_id, version_id=version_id,
        )

        text = await _fetch_ocr_text(tenant_id, version_id)
        if not text or len(text.strip()) < 5:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "skipped", "reason": "no text"}

        primary, confidence, secondary = _detect(text[:DETECT_TEXT_CAP])

        await _persist(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            primary=primary, confidence=confidence, secondary=secondary,
            correlation_id=correlation_id,
        )

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "lang_detect.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "language": primary,
                "confidence": confidence,
                "elapsed_ms": elapsed_ms,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "language": primary,
            "confidence": confidence,
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        if is_terminal:
            reason = classify_error_reason(exc)
            try:
                await publish_dlq(
                    consumer=CONSUMER, reason=reason, tenant_id=tenant_id,
                    document_id=document_id, version_id=version_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=attempt, correlation_id=correlation_id,
                )
                await mark_failed(
                    tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                )
            except Exception:
                log.exception("lang_detect terminal DLQ publish failed")
        raise


def _detect(text: str) -> tuple[str, float, list[dict]]:
    """Return (primary_iso, confidence, secondary_list).

    Wrapped so tests can stub langdetect without importing it.
    """
    from langdetect import DetectorFactory, detect_langs

    DetectorFactory.seed = 0  # deterministic
    results = detect_langs(text)
    if not results:
        return "und", 0.0, []
    primary = results[0]
    confidence = float(primary.prob)
    if len(text) < MIN_CHARS_FOR_CONFIDENCE:
        confidence = min(confidence, 0.5)
    secondary = [
        {"lang": str(r.lang), "confidence": round(float(r.prob), 4)}
        for r in results[1:3]
    ]
    return str(primary.lang), round(confidence, 4), secondary


# ---- DB ------------------------------------------------------------------

async def _fetch_ocr_text(tenant_id: str, version_id: str) -> str:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            # ocr_results is per-PAGE (columns: page_number, text_content);
            # there is no full_text column. Concatenate the pages in order so
            # detection sees the whole document, not one arbitrary page.
            row = await conn.fetchrow(
                """
                SELECT COALESCE(
                         string_agg(text_content, E'\n' ORDER BY page_number),
                         ''
                       ) AS full_text
                  FROM ocr_results
                 WHERE tenant_id = $1 AND version_id = $2
                """,
                tenant_id, version_id,
            )
    return (row["full_text"] if row else "") or ""


async def _persist(
    *, tenant_id: str, document_id: str, version_id: str,
    primary: str, confidence: float, secondary: list[dict],
    correlation_id: str,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO document_languages
                    (tenant_id, document_id, version_id,
                     detected_language, confidence, secondary_languages)
                VALUES ($1, $2, $3, $4, $5, $6::jsonb)
                ON CONFLICT (tenant_id, version_id) DO UPDATE
                  SET detected_language   = EXCLUDED.detected_language,
                      confidence          = EXCLUDED.confidence,
                      secondary_languages = EXCLUDED.secondary_languages,
                      detected_at         = NOW()
                """,
                tenant_id, document_id, version_id,
                primary, float(confidence), json.dumps(secondary),
            )

            payload = {
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "language": primary,
                "confidence": confidence,
                "secondary_languages": secondary,
                "correlation_id": correlation_id,
                "emitted_at": datetime.now(timezone.utc).isoformat(),
            }
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'document', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                LANGUAGE_DETECTED_SUBJECT, document_id,
                json.dumps(payload),
            )
