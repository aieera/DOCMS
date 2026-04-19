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

log = logging.getLogger(__name__)

EMBED_COMPLETED_SUBJECT = "dms.embed.completed.v1"
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

        chunks = chunk_text(
            text,
            chunk_size_tokens=settings.chunk_size_tokens,
            chunk_overlap_tokens=settings.chunk_overlap_tokens,
        )
        if not chunks:
            embed_documents_total.labels(status="skipped").inc()
            asyncio.run(mark_completed(
                tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
            ))
            return {"status": "skipped", "reason": "no text to embed"}

        texts = [c["text"] for c in chunks]
        vectors = _embed_in_batches(texts)

        points = []
        for chunk, vec in zip(chunks, vectors):
            points.append(PointStruct(
                id=_point_id(tenant_id, document_id, version_id, chunk["chunk_index"]),
                vector=vec,
                payload=_build_payload(
                    tenant_id=tenant_id,
                    document_id=document_id,
                    version_id=version_id,
                    chunk_index=chunk["chunk_index"],
                    start_char=chunk["start_char"],
                    end_char=chunk["end_char"],
                    token_count=chunk["token_count"],
                    text_snippet=chunk["text"],
                    readable_by=readable_by,
                ),
            ))

        client = _qdrant()
        _ensure_collection(client)
        for i in range(0, len(points), QDRANT_UPSERT_BATCH):
            client.upsert(
                collection_name=settings.qdrant_collection,
                points=points[i:i + QDRANT_UPSERT_BATCH],
            )

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
