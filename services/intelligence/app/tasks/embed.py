"""Embedding generation + Qdrant upsert.

Wave 5 Prompt 5.5 hardening:
- acks_late + 3 retries with jitter.
- Dedupe via `intel_processed_events` (consumer='embed').
- Payload per-spec: tenant_id, document_id, version_id, chunk_index,
  start_char, end_char, plus token_count + text-snippet for search
  hit rendering. Every point carries tenant_id so payload-scoped
  search can enforce cross-tenant isolation.
- Point id = uuid5(NAMESPACE_URL, f"{tenant}/{document}/{version}/{i}")
  so re-embedding the same version overwrites deterministically.
- Batch embed(32) with concurrency cap via chunked sequential calls
  (the embedder is sync; concurrency cap 4 is advisory pending an
  async embedder).
- Emit `dms.embed.completed.v1` on success.
- DLQ on terminal failure → `dms.dlq.intel_events.embed.<reason>`.

Cross-tenant isolation: every point's payload carries `tenant_id`.
The search service MUST add `{"key": "tenant_id", "match": {"value": tenant_id}}`
to every query filter. An integration test (tests/test_embed.py)
asserts that querying with a different tenant_id returns zero hits.
"""
from __future__ import annotations

import asyncio
import logging
import time
import uuid
from datetime import datetime, timezone

from qdrant_client import QdrantClient
from qdrant_client.models import Distance, PointStruct, VectorParams

from app.chunker import build_payload as _build_payload
from app.chunker import chunk_text
from app.chunker import point_id as _point_id
from app.config import settings
from app.error_classifier import classify_error_reason
from app.events.publisher import publish_cloudevent
from app.intel_dedupe import mark_completed, mark_enqueued, mark_failed, publish_dlq
from app.metrics import (
    embed_chunks_total,
    embed_dlq_total,
    embed_documents_total,
    embed_duration_seconds,
)
from app.models.embedder import embed
from app.worker import celery_app
from app.events.subjects import EMBED_COMPLETED_SUBJECT

log = logging.getLogger(__name__)

CONSUMER = "embed"

# Per spec: 32 embeddings per batch. Concurrency cap 4 is a no-op for
# the sync embedder; preserved as a constant for when the embedder
# gains async support.
EMBED_BATCH_SIZE = 32
EMBED_CONCURRENCY = 4
QDRANT_UPSERT_BATCH = 100


def _qdrant() -> QdrantClient:
    return QdrantClient(url=settings.qdrant_url)


def _ensure_collection(client: QdrantClient) -> None:
    collections = [c.name for c in client.get_collections().collections]
    if settings.qdrant_collection not in collections:
        client.create_collection(
            collection_name=settings.qdrant_collection,
            vectors_config=VectorParams(
                size=settings.embedding_dim, distance=Distance.COSINE
            ),
        )


def _embed_in_batches(texts: list[str]) -> list[list[float]]:
    """Run the sync embedder in chunks of EMBED_BATCH_SIZE.

    Returns a flat list of vectors in input order. A failure inside
    one batch is raised immediately so Celery's retry-with-jitter
    sees the error.
    """
    out: list[list[float]] = []
    for i in range(0, len(texts), EMBED_BATCH_SIZE):
        batch = texts[i:i + EMBED_BATCH_SIZE]
        vectors = embed(batch)
        out.extend(v.tolist() if hasattr(v, "tolist") else list(v) for v in vectors)
    return out


async def _load_workspace_id(tenant_id: str, document_id: str) -> str | None:
    """Look up the document's workspace so embed payloads can carry
    workspace_id. Used by the §6.8 /rag/query citation links — the
    UI can deep-link straight to the doc detail page only when each
    citation knows its workspace.

    Returns None on lookup failure; build_payload simply omits the key
    in that case. Cheap, single-row read so we don't hold the txn."""
    try:
        from app.persist import get_pool
    except ImportError:
        return None
    try:
        pool = await get_pool()
    except Exception:
        return None
    try:
        async with pool.acquire() as conn:
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
            )
            row = await conn.fetchrow(
                "SELECT workspace_id::text AS wid FROM documents "
                " WHERE tenant_id = $1 AND id = $2",
                tenant_id, document_id,
            )
            return row["wid"] if row else None
    except Exception:
        return None


async def _load_document_title(tenant_id: str, document_id: str) -> str | None:
    """Look up the document's title so embed payloads can carry it.
    Lets retrieval render named citations and answer "what documents
    are available?" without a per-query DB lookup. Returns None on
    lookup failure; build_payload omits the key in that case."""
    try:
        from app.persist import get_pool
    except ImportError:
        return None
    try:
        pool = await get_pool()
    except Exception:
        return None
    try:
        async with pool.acquire() as conn:
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
            )
            row = await conn.fetchrow(
                "SELECT title FROM documents "
                " WHERE tenant_id = $1 AND id = $2",
                tenant_id, document_id,
            )
            return row["title"] if row and row["title"] else None
    except Exception:
        return None


async def _load_pages_for_version(tenant_id: str, version_id: str) -> list[dict]:
    """Fetch ocr_results rows in page order so the chunker can map
    char offsets back to page numbers + ride along the
    "\n\n".join(pages) shape OCR emits to NER. Returns [] when no
    OCR data exists yet — the chunker treats that as "no page
    info" and emits page_number=None on every chunk."""
    try:
        from app.persist import get_pool
    except ImportError:
        return []
    try:
        pool = await get_pool()
    except Exception:
        return []
    try:
        async with pool.acquire() as conn:
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
            )
            rows = await conn.fetch(
                """SELECT page_number, text_content
                     FROM ocr_results
                    WHERE tenant_id = $1 AND version_id = $2
                    ORDER BY page_number ASC""",
                tenant_id, version_id,
            )
            return [
                {"page_number": r["page_number"], "text_content": r["text_content"]}
                for r in rows
            ]
    except Exception:
        return []


async def _persist_chunks(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    chunks: list[dict],
    embed_model: str,
) -> None:
    """Idempotent upsert into document_chunks. DELETE-then-INSERT
    inside one tx so a re-embed cleanly replaces the prior rows for
    this version. Mirrors the persist pattern in ocr / ner workers."""
    from app.persist import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
            )
            await conn.execute(
                """DELETE FROM document_chunks
                    WHERE tenant_id = $1 AND version_id = $2""",
                tenant_id, version_id,
            )
            for c in chunks:
                page_no = c.get("page_number")
                page_array = [int(page_no)] if page_no is not None else []
                await conn.execute(
                    """INSERT INTO document_chunks (
                           tenant_id, id, document_id, version_id,
                           chunk_index, text_content, token_count,
                           embedding_model, page_numbers,
                           section_path, char_offset_start, char_offset_end,
                           page_number, created_at
                       ) VALUES (
                           $1, gen_random_uuid(), $2, $3,
                           $4, $5, $6,
                           $7, $8::int[],
                           $9, $10, $11,
                           $12, now()
                       )""",
                    tenant_id, document_id, version_id,
                    int(c["chunk_index"]), c["text"], int(c.get("token_count") or 0),
                    embed_model, page_array,
                    c.get("section_path"),
                    int(c.get("start_char") or 0), int(c.get("end_char") or 0),
                    page_no,
                )


def _build_completed_envelope(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    chunk_count: int,
    model_version: str,
    correlation_id: str,
) -> dict:
    return {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "dms.intelligence.embed",
        "type": EMBED_COMPLETED_SUBJECT,
        "subject": f"version/{version_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "chunk_count": chunk_count,
            "model_version": model_version,
            "collection": settings.qdrant_collection,
        },
    }


@celery_app.task(
    name="app.tasks.embed.generate_embeddings",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=600,
    time_limit=660,
)
def generate_embeddings(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    text: str,
    readable_by: list[str] | None = None,
    event_id: str = "",
    correlation_id: str = "",
):
    start = time.monotonic()

    try:
        asyncio.run(mark_enqueued(
            tenant_id=tenant_id,
            consumer=CONSUMER,
            event_id=event_id,
            document_id=document_id,
            version_id=version_id,
        ))

        # ADR 0080 §"Chunking" — pull per-page text so the chunker can
        # resolve section_path + page_number for each emitted chunk.
        # Falls through with pages=[] for callers that pass plain
        # text (e.g. the unit tests); chunker handles None/[] safely.
        pages = asyncio.run(_load_pages_for_version(tenant_id, version_id))
        workspace_id = asyncio.run(_load_workspace_id(tenant_id, document_id))
        document_title = asyncio.run(_load_document_title(tenant_id, document_id))

        chunks = chunk_text(
            text,
            chunk_size_tokens=settings.chunk_size_tokens,
            chunk_overlap_tokens=settings.chunk_overlap_tokens,
            pages=pages,
        )
        if not chunks:
            embed_documents_total.labels(status="skipped").inc()
            asyncio.run(mark_completed(
                tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
            ))
            return {"status": "skipped", "reason": "no text to embed"}

        texts = [c["text"] for c in chunks]
        vectors = _embed_in_batches(texts)

        embed_model = getattr(settings, "embed_model", "all-MiniLM-L6-v2")

        points = []
        for chunk, vec in zip(chunks, vectors):
            points.append(PointStruct(
                id=_point_id(tenant_id, document_id, version_id, chunk["chunk_index"]),
                vector=vec,
                payload=_build_payload(
                    tenant_id=tenant_id,
                    document_id=document_id,
                    version_id=version_id,
                    workspace_id=workspace_id,
                    document_title=document_title,
                    chunk_index=chunk["chunk_index"],
                    start_char=chunk["start_char"],
                    end_char=chunk["end_char"],
                    token_count=chunk["token_count"],
                    text_snippet=chunk["text"],
                    readable_by=readable_by,
                    section_path=chunk.get("section_path"),
                    page_number=chunk.get("page_number"),
                ),
            ))

        client = _qdrant()
        _ensure_collection(client)
        for i in range(0, len(points), QDRANT_UPSERT_BATCH):
            client.upsert(
                collection_name=settings.qdrant_collection,
                points=points[i:i + QDRANT_UPSERT_BATCH],
            )

        # Persist to document_chunks so non-Qdrant queries (citation
        # renderer, audit, /chunks endpoint) can hit Postgres without
        # a vector roundtrip. Idempotent via DELETE-then-INSERT.
        try:
            asyncio.run(_persist_chunks(
                tenant_id=tenant_id,
                document_id=document_id,
                version_id=version_id,
                chunks=chunks,
                embed_model=embed_model,
            ))
        except Exception:
            # Persistence is non-critical for retrieval (Qdrant is the
            # source of truth for vector search). Log + continue so
            # an upstream DB hiccup doesn't undo a successful embed.
            log.exception("embed: document_chunks persist failed")

        envelope = _build_completed_envelope(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            chunk_count=len(chunks),
            model_version=getattr(settings, "embed_model", "all-MiniLM-L6-v2"),
            correlation_id=correlation_id,
        )
        try:
            asyncio.run(publish_cloudevent(
                EMBED_COMPLETED_SUBJECT, envelope, correlation_id=correlation_id
            ))
        except Exception:
            log.exception("embed_completed publish failed")

        asyncio.run(mark_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ))
        embed_documents_total.labels(status="completed").inc()
        embed_chunks_total.inc(len(chunks))
        embed_duration_seconds.observe(time.monotonic() - start)

        log.info(
            "embed.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "chunk_count": len(chunks),
                "attempt": self.request.retries + 1,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "chunk_count": len(chunks),
            "processing_time_ms": int((time.monotonic() - start) * 1000),
        }
    except Exception as exc:
        is_terminal = self.request.retries >= settings.ocr_max_retries
        if is_terminal:
            reason = classify_error_reason(exc)
            embed_documents_total.labels(status="failed").inc()
            embed_dlq_total.labels(reason=reason).inc()
            try:
                asyncio.run(publish_dlq(
                    consumer=CONSUMER,
                    reason=reason,
                    tenant_id=tenant_id,
                    document_id=document_id,
                    version_id=version_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=self.request.retries + 1,
                    correlation_id=correlation_id,
                ))
                asyncio.run(mark_failed(
                    tenant_id=tenant_id,
                    consumer=CONSUMER,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                ))
            except Exception:
                log.exception("embed terminal DLQ publish failed")
        raise
