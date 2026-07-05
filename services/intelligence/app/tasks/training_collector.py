"""Active-learning training example collector (ADR 0060).

Triggered by: dms.classify.corrected.v1 (ADR 0059 outbox)
Persists to:  training_examples
Dispatches:   model_retrain (when threshold met)
Emits:        dms.training_example.collected.v1

Cheap task — one DB read (correction → OCR text), one DB write
(training_examples insert), one count, optional retrain dispatch.
Runs on the default intelligence queue. Idempotent via
intel_processed_events ledger.
"""
from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import time
import uuid
from datetime import datetime, timezone

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
from app.events.subjects import COLLECTED_SUBJECT

log = logging.getLogger(__name__)

CONSUMER = "training_collector"

DEFAULT_CONFIG = {
    "enabled": False,
    "min_examples_for_retrain": 50,
    "retrain_increment": 25,
    "train_validation_split": 0.10,
    "train_test_split": 0.10,
    "gpu_queue": "intelligence-gpu",
}


@celery_app.task(
    name="app.tasks.training_collector.collect",
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
def collect(
    self,
    tenant_id: str,
    correction_id: str,
    document_id: str,
    version_id: str,
    original_category: str = "",
    corrected_category: str = "",
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id, correction_id=correction_id,
        document_id=document_id, version_id=version_id,
        original_category=original_category,
        corrected_category=corrected_category,
        event_id=event_id, correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 3,
    ))


async def _run_async(
    *, tenant_id, correction_id, document_id, version_id,
    original_category, corrected_category, event_id, correlation_id,
    attempt, is_terminal,
) -> dict:
    start = time.monotonic()
    if not (tenant_id and correction_id and document_id and version_id and corrected_category):
        return {"status": "skipped", "reason": "missing args"}

    try:
        if event_id and await already_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ):
            return {"status": "duplicate", "event_id": event_id}
        await mark_enqueued(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
            document_id=document_id, version_id=version_id,
        )

        cfg = await _load_config(tenant_id)
        if not cfg["enabled"]:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "disabled"}

        text = await _fetch_ocr_text(tenant_id, version_id)
        if not text or len(text.strip()) < 20:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "skipped", "reason": "no usable text"}

        split = _assign_split(correction_id, cfg)

        await _insert_example(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            text=text, label=corrected_category, original=original_category,
            correction_id=correction_id, split=split,
            correlation_id=correlation_id,
        )

        unused = await _count_unused(tenant_id)
        retrain_dispatched = False
        if _should_retrain(unused, cfg):
            from app.tasks.model_retrain import retrain
            retrain.apply_async(
                kwargs={
                    "tenant_id": tenant_id,
                    "model_type": "classification",
                    "correlation_id": correlation_id,
                    "event_id": str(uuid.uuid4()),
                },
                queue=cfg.get("gpu_queue") or "intelligence-gpu",
            )
            retrain_dispatched = True

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "training_collector.completed",
            extra={
                "tenant_id": tenant_id,
                "correction_id": correction_id,
                "split": split,
                "unused_count": unused,
                "retrain_dispatched": retrain_dispatched,
                "elapsed_ms": elapsed_ms,
            },
        )
        return {
            "status": "completed",
            "split": split,
            "unused_count": unused,
            "retrain_dispatched": retrain_dispatched,
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
                log.exception("training_collector terminal DLQ publish failed")
        raise


# ---- pure helpers --------------------------------------------------------

def _assign_split(correction_id: str, cfg: dict) -> str:
    """Deterministic split assignment based on correction_id hash so
    re-runs of the same correction always land in the same bucket."""
    h = int(hashlib.sha256(correction_id.encode()).hexdigest(), 16)
    bucket = (h % 1000) / 1000.0  # 0.000 - 0.999
    val = float(cfg["train_validation_split"])
    test = float(cfg["train_test_split"])
    if bucket < val:
        return "validation"
    if bucket < val + test:
        return "test"
    return "train"


def _should_retrain(unused: int, cfg: dict) -> bool:
    min_first = int(cfg["min_examples_for_retrain"])
    inc = max(1, int(cfg["retrain_increment"]))
    if unused < min_first:
        return False
    # First trigger at exactly min_first; subsequent triggers every `inc`.
    if unused == min_first:
        return True
    return (unused - min_first) % inc == 0


# ---- DB helpers ----------------------------------------------------------

async def _load_config(tenant_id: str) -> dict:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT enabled, min_examples_for_retrain, retrain_increment,
                       train_validation_split, train_test_split, gpu_queue
                  FROM active_learning_config WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return dict(DEFAULT_CONFIG)
    return {
        "enabled":                  row["enabled"],
        "min_examples_for_retrain": int(row["min_examples_for_retrain"]),
        "retrain_increment":        int(row["retrain_increment"]),
        "train_validation_split":   float(row["train_validation_split"]),
        "train_test_split":         float(row["train_test_split"]),
        "gpu_queue":                row["gpu_queue"] or "intelligence-gpu",
    }


async def _fetch_ocr_text(tenant_id: str, version_id: str) -> str:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT COALESCE(full_text, '') AS full_text
                  FROM ocr_results
                 WHERE tenant_id = $1 AND version_id = $2
                 ORDER BY created_at DESC LIMIT 1
                """,
                tenant_id, version_id,
            )
    return (row["full_text"] if row else "") or ""


async def _insert_example(
    *, tenant_id: str, document_id: str, version_id: str,
    text: str, label: str, original: str, correction_id: str,
    split: str, correlation_id: str,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO training_examples
                    (tenant_id, document_id, version_id, text_content,
                     label, original_prediction, correction_id, split)
                VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8)
                ON CONFLICT (tenant_id, correction_id) DO NOTHING
                """,
                tenant_id, document_id, version_id, text, label, original,
                correction_id, split,
            )
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'training_example', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                COLLECTED_SUBJECT, correction_id,
                json.dumps({
                    "tenant_id": tenant_id,
                    "correction_id": correction_id,
                    "split": split,
                    "label": label,
                    "correlation_id": correlation_id,
                    "emitted_at": datetime.now(timezone.utc).isoformat(),
                }),
            )


async def _count_unused(tenant_id: str) -> int:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT COUNT(*) AS n FROM training_examples
                 WHERE tenant_id = $1 AND used_in_version IS NULL
                """,
                tenant_id,
            )
    return int(row["n"]) if row else 0
