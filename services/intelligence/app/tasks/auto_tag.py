"""Auto-tagging — promote NER + classification signals into documents.tags.

Triggered by:
  * dms.classify.completed.v1
  * dms.ner.completed.v1

Strategy:
  1. Load tenant config (auto_apply_threshold, suggest_threshold,
     blocked_tags, source_weights). Code defaults if no row.
  2. Pull this version's classification + NER results.
  3. Generate tag candidates from each source, weight by source.
  4. Dedupe by tag_name (highest confidence wins), drop blocked tags,
     trim to max_tags_per_document.
  5. In one transaction:
       - INSERT each candidate into tag_suggestions
         (status='auto_applied' if conf >= auto_apply_threshold,
          else status='pending')
       - For auto-applied: UPDATE documents.tags via array_append
         (uses array(SELECT DISTINCT ...) to dedupe).
       - INSERT one outbox row dms.autotag.completed.v1 so the
         document-service publisher forwards it to NATS for the
         search index update.

Idempotency: at-least-once redelivery is suppressed twice over —
intel_processed_events ledger plus the partial unique index
(tenant, document, tag_name, source) WHERE status='pending'. A
later run after model improvement can re-suggest a previously
rejected tag — by design.
"""
from __future__ import annotations

import asyncio
import json
import logging
import time
import uuid
from datetime import datetime, timezone
from typing import Any

from app.events.publisher import publish_cloudevent
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

AUTOTAG_COMPLETED_SUBJECT = "dms.autotag.completed.v1"
CONSUMER = "auto_tag"

DEFAULT_CONFIG = {
    "enabled": True,
    "auto_apply_threshold": 0.95,
    "suggest_threshold": 0.60,
    "max_tags_per_document": 20,
    "blocked_tags": [],
    "source_weights": {
        "ner": 1.0,
        "classification": 0.8,
        "llm": 0.9,
        "pattern": 0.7,
    },
}

# NER entity types that should be skipped entirely — too noisy as tags.
SKIP_ENTITY_TYPES = {"DATE", "MONEY", "CARDINAL", "ORDINAL", "PERCENT", "TIME", "QUANTITY"}

# Per-rule confidence floors before source-weight is applied.
PERSON_CONF_FLOOR = 0.80
GENERIC_CONF_FLOOR = 0.85


@celery_app.task(
    name="app.tasks.auto_tag.auto_tag",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=120,
    time_limit=180,
)
def auto_tag(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    source_event: str = "",
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id,
        document_id=document_id,
        version_id=version_id,
        source_event=source_event,
        event_id=event_id,
        correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 3,
    ))


# ---- main async entry --------------------------------------------------

async def _run_async(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    source_event: str,
    event_id: str,
    correlation_id: str,
    attempt: int,
    is_terminal: bool,
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

        cfg = await _load_config(tenant_id)
        if not cfg["enabled"]:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "disabled"}

        classifications = await _fetch_classifications(tenant_id, version_id)
        entities = await _fetch_entities(tenant_id, version_id)

        candidates: list[dict] = []
        for ent in entities:
            cand = _tag_from_entity(ent, cfg["source_weights"])
            if cand and cand["confidence"] >= cfg["suggest_threshold"]:
                candidates.append(cand)
        for cls in classifications:
            for cand in _tags_from_classification(cls, cfg["source_weights"]):
                if cand["confidence"] >= cfg["suggest_threshold"]:
                    candidates.append(cand)

        candidates = _dedupe(candidates)
        blocked = {t.lower() for t in cfg["blocked_tags"]}
        candidates = [c for c in candidates if c["tag_name"] not in blocked]
        candidates.sort(key=lambda c: c["confidence"], reverse=True)
        candidates = candidates[: cfg["max_tags_per_document"]]

        auto_applied: list[dict] = []
        suggestions: list[dict] = []
        for c in candidates:
            (auto_applied if c["confidence"] >= cfg["auto_apply_threshold"]
             else suggestions).append(c)

        await _persist(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            auto_applied=auto_applied,
            suggestions=suggestions,
            correlation_id=correlation_id,
        )

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)

        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "auto_tag.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "auto_applied_count": len(auto_applied),
                "suggestion_count": len(suggestions),
                "elapsed_ms": elapsed_ms,
                "correlation_id": correlation_id,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "auto_applied_count": len(auto_applied),
            "suggestion_count": len(suggestions),
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
                log.exception("auto_tag terminal DLQ publish failed")
        raise


# ---- config + lookup ---------------------------------------------------

async def _load_config(tenant_id: str) -> dict:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT enabled, auto_apply_threshold, suggest_threshold,
                       max_tags_per_document, blocked_tags, source_weights
                  FROM auto_tag_config
                 WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return dict(DEFAULT_CONFIG)
    sw = row["source_weights"]
    if isinstance(sw, str):
        sw = json.loads(sw)
    return {
        "enabled": row["enabled"],
        "auto_apply_threshold": float(row["auto_apply_threshold"]),
        "suggest_threshold": float(row["suggest_threshold"]),
        "max_tags_per_document": int(row["max_tags_per_document"]),
        "blocked_tags": list(row["blocked_tags"] or []),
        "source_weights": {**DEFAULT_CONFIG["source_weights"], **(sw or {})},
    }


async def _fetch_classifications(tenant_id: str, version_id: str) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT category_key, confidence, method, model_version,
                       COALESCE(top3, '[]'::jsonb) AS top3
                  FROM document_classifications
                 WHERE tenant_id = $1 AND version_id = $2
                """,
                tenant_id, version_id,
            )
    out = []
    for r in rows:
        top3 = r["top3"]
        if isinstance(top3, str):
            top3 = json.loads(top3)
        out.append({
            "category_key": r["category_key"],
            "confidence": float(r["confidence"]),
            "method": r["method"],
            "model_version": r["model_version"] or "",
            "top3": top3 or [],
        })
    return out


async def _fetch_entities(tenant_id: str, version_id: str) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT entity_type, entity_value, confidence
                  FROM document_entities
                 WHERE tenant_id = $1 AND version_id = $2
                """,
                tenant_id, version_id,
            )
    return [
        {
            "entity_type": r["entity_type"] or "",
            "entity_value": r["entity_value"] or "",
            "confidence": float(r["confidence"] or 0.0),
        }
        for r in rows
    ]


# ---- pure tag-generation rules -----------------------------------------

def _norm(text: str) -> str:
    return " ".join(text.lower().strip().split())


def _tag_from_entity(ent: dict, weights: dict) -> dict | None:
    etype = (ent.get("entity_type") or "").upper()
    val = ent.get("entity_value") or ""
    conf = float(ent.get("confidence") or 0.0)
    if not val or etype in SKIP_ENTITY_TYPES:
        return None
    if etype in ("ORG", "COMPANY", "ORGANIZATION"):
        tag = _norm(val)
    elif etype in ("LOCATION", "GPE", "LOC"):
        tag = _norm(val)
    elif etype == "PERSON":
        if conf < PERSON_CONF_FLOOR:
            return None
        tag = f"person:{_norm(val)}"
    else:
        if conf < GENERIC_CONF_FLOOR:
            return None
        tag = f"{etype.lower()}:{_norm(val)}"
    if not tag or len(tag) > 64:
        return None
    weighted = max(0.0, min(1.0, conf * float(weights.get("ner", 1.0))))
    return {
        "tag_name": tag,
        "source": "ner",
        "source_detail": {"entity_type": etype, "entity_text": val,
                          "raw_confidence": conf},
        "confidence": weighted,
    }


def _tags_from_classification(cls: dict, weights: dict) -> list[dict]:
    out: list[dict] = []
    w = float(weights.get("classification", 0.8))
    primary = (cls.get("category_key") or "").strip()
    if primary:
        out.append({
            "tag_name": _norm(primary),
            "source": "classification",
            "source_detail": {
                "category_key": primary,
                "method": cls.get("method", ""),
                "model_version": cls.get("model_version", ""),
                "is_primary": True,
            },
            "confidence": max(0.0, min(1.0, float(cls.get("confidence", 0.0)) * w)),
        })
    for alt in (cls.get("top3") or []):
        if not isinstance(alt, dict):
            continue
        cat = (alt.get("category") or alt.get("category_key") or "").strip()
        if not cat or cat == primary:
            continue
        out.append({
            "tag_name": _norm(cat),
            "source": "classification",
            "source_detail": {
                "category_key": cat,
                "method": alt.get("method", ""),
                "is_primary": False,
            },
            "confidence": max(0.0, min(1.0, float(alt.get("confidence", 0.0)) * w)),
        })
    return out


def _dedupe(candidates: list[dict]) -> list[dict]:
    by_name: dict[str, dict] = {}
    for c in candidates:
        n = c["tag_name"]
        cur = by_name.get(n)
        if cur is None or c["confidence"] > cur["confidence"]:
            by_name[n] = c
    return list(by_name.values())


# ---- persistence -------------------------------------------------------

async def _persist(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    auto_applied: list[dict],
    suggestions: list[dict],
    correlation_id: str,
) -> None:
    """Single asyncpg transaction: write all suggestions, append
    auto-applied tags to documents.tags, write outbox row."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            for c in auto_applied:
                await _insert_suggestion(conn, tenant_id, document_id,
                                         version_id, c, status="auto_applied")
            for c in suggestions:
                await _insert_suggestion(conn, tenant_id, document_id,
                                         version_id, c, status="pending")

            if auto_applied:
                await _append_tags(conn, tenant_id, document_id,
                                   [c["tag_name"] for c in auto_applied])

            payload = {
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "auto_applied_count": len(auto_applied),
                "suggestion_count": len(suggestions),
                "tags_auto_applied": [c["tag_name"] for c in auto_applied],
                "tags_suggested": [c["tag_name"] for c in suggestions],
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
                AUTOTAG_COMPLETED_SUBJECT, document_id,
                json.dumps(payload),
            )


async def _insert_suggestion(conn, tenant_id, document_id, version_id, c, *,
                              status: str) -> None:
    await conn.execute(
        """
        INSERT INTO tag_suggestions
            (tenant_id, document_id, version_id, tag_name, source,
             source_detail, confidence, status)
        VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)
        ON CONFLICT DO NOTHING
        """,
        tenant_id, document_id, version_id,
        c["tag_name"], c["source"],
        json.dumps(c.get("source_detail") or {}),
        float(c["confidence"]), status,
    )


async def _append_tags(conn, tenant_id: str, document_id: str,
                       new_tags: list[str]) -> None:
    """Merge new tags into documents.tags, deduped, preserving order."""
    if not new_tags:
        return
    await conn.execute(
        """
        UPDATE documents
           SET tags = (
               SELECT ARRAY(
                   SELECT DISTINCT t
                     FROM unnest(documents.tags || $3::text[]) AS t
               )
           )
         WHERE tenant_id = $1 AND id = $2
        """,
        tenant_id, document_id, new_tags,
    )
