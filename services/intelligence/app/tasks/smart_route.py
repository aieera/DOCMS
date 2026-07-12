"""Smart routing — three strategies, never auto-moves (ADR 0053).

Triggered by: dms.classify.completed.v1
Persists to:  route_suggestions  (status='pending')
Emits:        dms.routing.completed.v1

Strategies (each emits 0..N candidates independently):
  rule       admin-defined routing_rules row matches the version's
             category_key. Confidence = classification.confidence × 0.95.
  history    filing_history rows for the same category_key are
             grouped by folder; confidence reflects how concentrated
             past filings were on that folder.
  similarity Qdrant top-K vector neighbors filtered by tenant; for
             each neighbor we look up its current folder; confidence
             reflects how concentrated neighbors are on a folder.

The merge step deduplicates by suggested_folder_id, keeping the
highest-confidence candidate (with its source recorded). The partial
unique index on tag_suggestions does the same job at the DB layer
to suppress at-least-once redelivery.

Auto-move is intentionally disabled in v1: the threshold survives
in the schema for UI emphasis only. See ADR 0053 §"Why not auto-move".
"""
from __future__ import annotations

import asyncio
import json
import logging
import time
import uuid
from collections import Counter, defaultdict
from datetime import datetime, timezone
from statistics import mean
from typing import Any

from app.config import settings
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
from app.events.subjects import ROUTING_COMPLETED_SUBJECT

log = logging.getLogger(__name__)

CONSUMER = "smart_route"

DEFAULT_CONFIG = {
    "enabled": True,
    "auto_move_threshold": 0.98,
    "suggest_threshold": 0.50,
    "max_suggestions": 5,
    "learn_from_history": True,
    "use_similarity": True,
}

# Tunables for the three strategies.
RULE_CONFIDENCE_MULTIPLIER = 0.95   # see ADR 0053 — explicit rules trusted higher
SIMILARITY_TOP_K = 10
HISTORY_LOOKBACK = 1000             # cap on rows examined per category_key


@celery_app.task(
    name="app.tasks.smart_route.smart_route",
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
def smart_route(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id,
        document_id=document_id,
        version_id=version_id,
        event_id=event_id,
        correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 3,
    ))


# ---- entry point -------------------------------------------------------

async def _run_async(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
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

        cls = await _fetch_primary_classification(tenant_id, version_id)
        if not cls or not cls.get("category_key"):
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "skipped", "reason": "no classification"}

        candidates: list[dict] = []
        candidates.extend(await _from_rules(tenant_id, cls))
        if cfg["learn_from_history"]:
            candidates.extend(await _from_history(tenant_id, cls))
        if cfg["use_similarity"]:
            candidates.extend(await _from_similarity(
                tenant_id=tenant_id, document_id=document_id, cls=cls,
            ))

        candidates = _dedupe(candidates)
        candidates = [c for c in candidates if c["confidence"] >= cfg["suggest_threshold"]]
        candidates.sort(key=lambda c: c["confidence"], reverse=True)
        candidates = candidates[: cfg["max_suggestions"]]

        # Resolve folder paths for human-readable display.
        await _resolve_folder_paths(tenant_id, candidates)

        await _persist(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            candidates=candidates,
            classification=cls,
            correlation_id=correlation_id,
        )

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "smart_route.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "candidate_count": len(candidates),
                "elapsed_ms": elapsed_ms,
                "correlation_id": correlation_id,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "suggestion_count": len(candidates),
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
                log.exception("smart_route terminal DLQ publish failed")
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
                SELECT enabled, auto_move_threshold, suggest_threshold,
                       max_suggestions, learn_from_history, use_similarity
                  FROM smart_routing_config
                 WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return dict(DEFAULT_CONFIG)
    return {
        "enabled": row["enabled"],
        "auto_move_threshold": float(row["auto_move_threshold"]),
        "suggest_threshold": float(row["suggest_threshold"]),
        "max_suggestions": int(row["max_suggestions"]),
        "learn_from_history": row["learn_from_history"],
        "use_similarity": row["use_similarity"],
    }


async def _fetch_primary_classification(tenant_id: str, version_id: str) -> dict | None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT category_key, confidence, method, model_version
                  FROM document_classifications
                 WHERE tenant_id = $1 AND version_id = $2
                 LIMIT 1
                """,
                tenant_id, version_id,
            )
    if not row:
        return None
    return {
        "category_key": row["category_key"],
        "confidence": float(row["confidence"]),
        "method": row["method"],
        "model_version": row["model_version"] or "",
    }


# ---- strategy: rule ----------------------------------------------------

async def _from_rules(tenant_id: str, cls: dict) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT id, name, target_folder_id, target_workspace_id, priority
                  FROM routing_rules
                 WHERE tenant_id = $1 AND category_key = $2 AND enabled = true
                 ORDER BY priority DESC
                 LIMIT 20
                """,
                tenant_id, cls["category_key"],
            )
    base = float(cls["confidence"]) * RULE_CONFIDENCE_MULTIPLIER
    out = []
    for r in rows:
        out.append({
            "suggested_folder_id": str(r["target_folder_id"]),
            "suggested_workspace_id": str(r["target_workspace_id"]) if r["target_workspace_id"] else None,
            "match_source": "rule",
            "match_detail": {
                "rule_id": str(r["id"]),
                "rule_name": r["name"],
                "priority": r["priority"],
                "category_key": cls["category_key"],
            },
            "confidence": max(0.0, min(1.0, base)),
        })
    return out


# ---- strategy: history -------------------------------------------------

async def _from_history(tenant_id: str, cls: dict) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                WITH recent AS (
                  SELECT filed_folder_id, filed_workspace_id
                    FROM filing_history
                   WHERE tenant_id = $1 AND category_key = $2
                   ORDER BY filed_at DESC
                   LIMIT $3
                )
                SELECT filed_folder_id, filed_workspace_id, COUNT(*) AS freq
                  FROM recent
                 GROUP BY filed_folder_id, filed_workspace_id
                 ORDER BY freq DESC
                 LIMIT 5
                """,
                tenant_id, cls["category_key"], HISTORY_LOOKBACK,
            )
    if not rows:
        return []
    total = sum(int(r["freq"]) for r in rows)
    if total == 0:
        return []
    base = float(cls["confidence"])
    out = []
    for r in rows:
        share = int(r["freq"]) / total
        out.append({
            "suggested_folder_id": str(r["filed_folder_id"]),
            "suggested_workspace_id": str(r["filed_workspace_id"]) if r["filed_workspace_id"] else None,
            "match_source": "history",
            "match_detail": {
                "frequency": int(r["freq"]),
                "share_of_recent": round(share, 4),
                "lookback_rows": HISTORY_LOOKBACK,
                "category_key": cls["category_key"],
            },
            "confidence": max(0.0, min(1.0, share * base)),
        })
    return out


# ---- strategy: similarity ----------------------------------------------

async def _from_similarity(*, tenant_id: str, document_id: str, cls: dict) -> list[dict]:
    """Top-K Qdrant neighbours; group their current folder; confidence
    reflects neighbour concentration. Returns [] when this doc has no
    embedded chunks yet."""
    try:
        from qdrant_client import QdrantClient
        from qdrant_client.models import FieldCondition, Filter, MatchValue
    except Exception:
        return []

    client = QdrantClient(url=settings.qdrant_url)
    coll = settings.qdrant_collection

    own_filter = Filter(must=[
        FieldCondition(key="tenant_id", match=MatchValue(value=tenant_id)),
        FieldCondition(key="document_id", match=MatchValue(value=document_id)),
    ])
    try:
        own_points, _ = client.scroll(
            collection_name=coll, scroll_filter=own_filter,
            limit=200, with_payload=False, with_vectors=True,
        )
    except Exception:
        return []
    own_vectors = [p.vector for p in own_points if getattr(p, "vector", None)]
    if not own_vectors:
        return []
    centroid = _mean_vector(own_vectors)

    peer_filter = Filter(
        must=[FieldCondition(key="tenant_id", match=MatchValue(value=tenant_id))],
        must_not=[FieldCondition(key="document_id", match=MatchValue(value=document_id))],
    )
    try:
        hits = client.search(
            collection_name=coll, query_vector=centroid,
            query_filter=peer_filter, limit=SIMILARITY_TOP_K,
            with_payload=True,
        )
    except Exception:
        return []
    if not hits:
        return []

    by_doc: dict[str, list[float]] = defaultdict(list)
    for h in hits:
        peer = (h.payload or {}).get("document_id")
        if peer and peer != document_id:
            by_doc[peer].append(float(h.score))
    if not by_doc:
        return []

    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT id::text AS doc_id, folder_id, workspace_id
                  FROM documents
                 WHERE tenant_id = $1 AND id = ANY($2::uuid[])
                """,
                tenant_id, list(by_doc.keys()),
            )
    by_folder: dict[tuple[str, str | None], list[float]] = defaultdict(list)
    for r in rows:
        scores = by_doc.get(r["doc_id"]) or []
        if not scores:
            continue
        key = (str(r["folder_id"]), str(r["workspace_id"]) if r["workspace_id"] else None)
        by_folder[key].extend(scores)

    out = []
    total_neighbours = SIMILARITY_TOP_K
    for (folder_id, workspace_id), scores in by_folder.items():
        share = len(scores) / float(total_neighbours)
        avg_score = mean(scores)
        out.append({
            "suggested_folder_id": folder_id,
            "suggested_workspace_id": workspace_id,
            "match_source": "similarity",
            "match_detail": {
                "neighbour_count": len(scores),
                "top_k": SIMILARITY_TOP_K,
                "mean_score": round(avg_score, 4),
                "category_key": cls["category_key"],
            },
            "confidence": max(0.0, min(1.0, share * avg_score)),
        })
    return out


def _mean_vector(vectors: list[list[float]]) -> list[float]:
    dim = len(vectors[0])
    sums = [0.0] * dim
    for v in vectors:
        for i, x in enumerate(v):
            sums[i] += x
    return [s / len(vectors) for s in sums]


# ---- merge -------------------------------------------------------------

def _dedupe(candidates: list[dict]) -> list[dict]:
    """Keep highest-confidence candidate per suggested_folder_id."""
    by_folder: dict[str, dict] = {}
    for c in candidates:
        f = c["suggested_folder_id"]
        cur = by_folder.get(f)
        if cur is None or c["confidence"] > cur["confidence"]:
            by_folder[f] = c
    return list(by_folder.values())


# ---- folder path resolution -------------------------------------------

async def _resolve_folder_paths(tenant_id: str, candidates: list[dict]) -> None:
    if not candidates:
        return
    folder_ids = list({c["suggested_folder_id"] for c in candidates})
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT id::text AS fid, name, path::text AS path_str
                  FROM folders
                 WHERE tenant_id = $1 AND id = ANY($2::uuid[])
                """,
                tenant_id, folder_ids,
            )
    by_id = {r["fid"]: r for r in rows}
    for c in candidates:
        fid = c["suggested_folder_id"]
        r = by_id.get(fid)
        if r is None:
            c["folder_path"] = "(unknown folder)"
        else:
            # ltree path uses dots between folder ids; we want a name
            # breadcrumb. Without a recursive lookup the safest cheap
            # answer is the folder's own name.
            c["folder_path"] = r["name"]


# ---- persistence ------------------------------------------------------

async def _persist(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    candidates: list[dict],
    classification: dict,
    correlation_id: str,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            for c in candidates:
                await conn.execute(
                    """
                    INSERT INTO route_suggestions
                        (tenant_id, document_id, version_id, suggested_folder_id,
                         suggested_workspace_id, folder_path, match_source,
                         match_detail, confidence, status)
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, 'pending')
                    ON CONFLICT DO NOTHING
                    """,
                    tenant_id, document_id, version_id,
                    c["suggested_folder_id"],
                    c.get("suggested_workspace_id"),
                    c.get("folder_path", ""),
                    c["match_source"],
                    json.dumps(c.get("match_detail") or {}),
                    float(c["confidence"]),
                )

            payload = {
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "category_key": classification.get("category_key"),
                "suggestion_count": len(candidates),
                "top_confidence": (candidates[0]["confidence"] if candidates else 0.0),
                "top_folder_id": (candidates[0]["suggested_folder_id"] if candidates else None),
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
                ROUTING_COMPLETED_SUBJECT, document_id,
                json.dumps(payload),
            )
