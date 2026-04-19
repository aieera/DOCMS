"""RAG Q&A pipeline — hybrid retrieval + RRF fusion + re-rank + LLM."""
from __future__ import annotations

import json
import logging
import time
from typing import Any

from qdrant_client import QdrantClient
from qdrant_client.models import FieldCondition, Filter, MatchAny, MatchValue

from app.config import settings
from app.models.embedder import embed_single
from app.models.reranker import rerank
from app.worker import celery_app

log = logging.getLogger(__name__)

SYSTEM_PROMPT = (
    "You are a document assistant for VaultDMS. Answer based ONLY on the provided documents. "
    "If the answer isn't in the documents, say 'I couldn't find this in your documents.' "
    "Always cite sources as [Document: title, Page: X]."
)


def _qdrant():
    return QdrantClient(url=settings.qdrant_url)


def _vector_search(q_embedding: list[float], tenant_id: str, user_groups: list[str],
                   scope_filter: dict | None = None, limit: int = 50) -> list[dict]:
    must = [
        FieldCondition(key="tenant_id", match=MatchValue(value=tenant_id)),
        FieldCondition(key="readable_by", match=MatchAny(any=user_groups + ["everyone"])),
    ]
    if scope_filter:
        for k, v in scope_filter.items():
            must.append(FieldCondition(key=k, match=MatchValue(value=v)))

    client = _qdrant()
    results = client.search(
        collection_name=settings.qdrant_collection,
        query_vector=q_embedding,
        query_filter=Filter(must=must),
        limit=limit,
    )
    return [
        {
            "id": str(r.id),
            "score": r.score,
            "text": r.payload.get("text", ""),
            "document_id": r.payload.get("document_id", ""),
            "version_id": r.payload.get("version_id", ""),
            "chunk_index": r.payload.get("chunk_index", 0),
        }
        for r in results
    ]


def _rrf_fuse(lists: list[list[dict]], k: int = 60) -> list[dict]:
    scores: dict[str, float] = {}
    items: dict[str, dict] = {}
    for ranked in lists:
        for rank, item in enumerate(ranked):
            key = item["id"]
            scores[key] = scores.get(key, 0) + 1.0 / (k + rank + 1)
            items[key] = item
    sorted_ids = sorted(scores, key=scores.get, reverse=True)
    return [items[i] for i in sorted_ids]


def _build_context(chunks: list[dict], max_tokens: int = 4000) -> str:
    import tiktoken
    enc = tiktoken.get_encoding("cl100k_base")
    parts = []
    total = 0
    for c in chunks:
        tokens = len(enc.encode(c["text"]))
        if total + tokens > max_tokens:
            break
        parts.append(f'[Document: {c["document_id"]}, Chunk: {c["chunk_index"]}]\n{c["text"]}')
        total += tokens
    return "\n\n---\n\n".join(parts)


def ask(
    tenant_id: str,
    user_id: str,
    user_groups: list[str],
    question: str,
    scope: str = "tenant",
    scope_id: str | None = None,
    conversation_history: list[dict] | None = None,
) -> dict[str, Any]:
    start = time.monotonic()

    q_embedding = embed_single(question)

    scope_filter = None
    if scope == "document" and scope_id:
        scope_filter = {"document_id": scope_id}
    elif scope == "workspace" and scope_id:
        scope_filter = {"workspace_id": scope_id}

    vector_results = _vector_search(q_embedding, tenant_id, user_groups, scope_filter)
    fused = _rrf_fuse([vector_results])

    top_20_texts = [c["text"] for c in fused[:20]]
    if top_20_texts:
        scores = rerank(question, top_20_texts)
        ranked_pairs = sorted(zip(scores, fused[:20]), reverse=True)
        top_chunks = [c for _, c in ranked_pairs[:5]]
    else:
        top_chunks = fused[:5]

    context = _build_context(top_chunks)
    if not context:
        return {
            "answer": "I couldn't find relevant documents to answer your question.",
            "sources": [],
            "elapsed_ms": int((time.monotonic() - start) * 1000),
        }

    messages = [{"role": "system", "content": SYSTEM_PROMPT}]
    if conversation_history:
        messages.extend(conversation_history[-6:])
    messages.append({"role": "user", "content": f"Documents:\n{context}\n\nQuestion: {question}"})

    from app import llm_gateway
    resp = llm_gateway.completion(tenant_id=tenant_id, messages=messages, max_tokens=2000)

    sources = [{"document_id": c["document_id"], "chunk_index": c["chunk_index"]} for c in top_chunks]

    return {
        "answer": resp["content"],
        "sources": sources,
        "model": resp["model"],
        "input_tokens": resp["input_tokens"],
        "output_tokens": resp["output_tokens"],
        "cost_usd": resp["cost_usd"],
        "elapsed_ms": int((time.monotonic() - start) * 1000),
    }
