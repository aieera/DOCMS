"""ADR 0104 Phase 2 — clause detection against uploaded documents.

Chained AFTER generate_embeddings in the ocr_completed fan-out (the
document's chunks must already be in Qdrant). For each non-deleted
clause in the tenant's library: ensure a cached embedding
(clause_embeddings, keyed by body_sha256 — self-healing on edit), run
one Qdrant search filtered to this document's chunks, and record a
clause_matches row when cosine score >= settings.clause_match_threshold.
Idempotent per (tenant, document, version): delete-then-insert.
"""
from __future__ import annotations

import asyncio
import hashlib
import logging
import re

from app.config import settings
from app.models.embedder import embed_single
from app.tasks.embed import _qdrant
from app.worker import celery_app

from qdrant_client.models import FieldCondition, Filter, MatchValue

log = logging.getLogger(__name__)


def _body_sha(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _normalize_text(text: str) -> str:
    """Lowercase -> collapse whitespace runs to one space -> trim.
    The document service's variations endpoint groups by
    md5(btrim(regexp_replace(lower(matched_text), '\\s+', ' ', 'g'))) —
    this function MUST stay byte-equivalent to that expression."""
    return re.sub(r"\s+", " ", text.lower()).strip()


def _matches_for_clauses(client, *, tenant_id: str, document_id: str, version_id: str,
                         clauses: list[dict], threshold: float) -> list[dict]:
    """One Qdrant search per clause, filtered to this document VERSION's
    chunks. version_id scoping matters: points from prior versions of the
    same document persist in Qdrant (uuid5 ids per version), so an
    unscoped top-1 could return a stale version's chunk (review finding)."""
    out: list[dict] = []
    flt = Filter(must=[
        FieldCondition(key="tenant_id", match=MatchValue(value=tenant_id)),
        FieldCondition(key="document_id", match=MatchValue(value=document_id)),
        FieldCondition(key="version_id", match=MatchValue(value=version_id)),
    ])
    for c in clauses:
        hits = client.search(
            collection_name=settings.qdrant_collection,
            query_vector=c["vector"],
            query_filter=flt,
            limit=1,
        )
        if not hits:
            continue
        top = hits[0]
        if top.score >= threshold:
            payload = top.payload or {}
            out.append({
                "clause_id": c["id"],
                "similarity": float(top.score),
                "chunk_index": int(payload.get("chunk_index", 0)),
                "matched_text": (payload.get("text_snippet")
                                 or payload.get("text") or ""),
            })
    return out


async def _load_clauses_with_vectors(tenant_id: str) -> list[dict]:
    """Load non-deleted clauses + cached embeddings; re-embed stale or
    missing entries inline and upsert the cache."""
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id)
            rows = await conn.fetch(
                """
                SELECT c.id::text AS id, c.body_text,
                       e.body_sha256, e.vector
                  FROM clauses c
             LEFT JOIN clause_embeddings e
                    ON e.tenant_id = c.tenant_id AND e.clause_id = c.id
                 WHERE c.tenant_id = $1 AND c.deleted_at IS NULL
                """,
                tenant_id)
    out: list[dict] = []
    stale: list[tuple[str, str, list[float]]] = []
    for r in rows:
        sha = _body_sha(r["body_text"])
        if r["body_sha256"] == sha and r["vector"]:
            out.append({"id": r["id"], "vector": list(r["vector"])})
            continue
        vec = embed_single(r["body_text"])
        out.append({"id": r["id"], "vector": vec})
        stale.append((r["id"], sha, vec))
    if stale:
        async with pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_tenant', $1, true)", tenant_id)
                for cid, sha, vec in stale:
                    await conn.execute(
                        """
                        INSERT INTO clause_embeddings
                            (tenant_id, clause_id, body_sha256, vector, updated_at)
                        VALUES ($1, $2, $3, $4, now())
                        ON CONFLICT (tenant_id, clause_id) DO UPDATE
                           SET body_sha256 = EXCLUDED.body_sha256,
                               vector      = EXCLUDED.vector,
                               updated_at  = now()
                        """,
                        tenant_id, cid, sha, vec)
    return out


async def _replace_matches(tenant_id: str, document_id: str, version_id: str,
                           matches: list[dict]) -> None:
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id)
            await conn.execute(
                """
                DELETE FROM clause_matches
                 WHERE tenant_id = $1 AND document_id = $2 AND version_id = $3
                """,
                tenant_id, document_id, version_id)
            for m in matches:
                await conn.execute(
                    """
                    INSERT INTO clause_matches
                        (tenant_id, document_id, version_id, clause_id,
                         similarity, chunk_index, matched_text)
                    VALUES ($1, $2, $3, $4, $5, $6, $7)
                    """,
                    tenant_id, document_id, version_id, m["clause_id"],
                    m["similarity"], m["chunk_index"], m["matched_text"])


@celery_app.task(
    name="app.tasks.clause_match.detect_clauses",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
)
def detect_clauses(self, tenant_id: str, document_id: str, version_id: str,
                   **_ignored) -> dict:
    """**_ignored swallows chain-passed args (celery chains pass the
    parent task's return as the first positional unless .si() is used;
    the consumer uses .si(), but keep the guard for manual dispatch)."""
    clauses = asyncio.run(_load_clauses_with_vectors(tenant_id))
    if not clauses:
        return {"matches": 0, "clauses": 0}
    client = _qdrant()
    matches = _matches_for_clauses(
        client, tenant_id=tenant_id, document_id=document_id,
        version_id=version_id, clauses=clauses,
        threshold=settings.clause_match_threshold,
    )
    asyncio.run(_replace_matches(tenant_id, document_id, version_id, matches))
    log.info("clause detection: %d/%d matched for doc %s",
             len(matches), len(clauses), document_id)
    return {"matches": len(matches), "clauses": len(clauses)}
