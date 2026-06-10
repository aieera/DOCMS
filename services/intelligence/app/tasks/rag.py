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
    "You are a document assistant for SeDoc. Answer based ONLY on the provided documents. "
    "If the answer isn't in the documents, say 'I couldn't find this in your documents.' "
    "Always cite sources as [Document: title, Page: X]."
)


def _qdrant():
    return QdrantClient(url=settings.qdrant_url)


def _vector_search(q_embedding: list[float], tenant_id: str, user_groups: list[str],
                   scope_filter: dict | None = None, limit: int = 50,
                   allowed_doc_ids: list[str] | None = None) -> list[dict]:  # noqa: D401
    must = [
        FieldCondition(key="tenant_id", match=MatchValue(value=tenant_id)),
        FieldCondition(key="readable_by", match=MatchAny(any=user_groups + ["everyone"])),
    ]
    if scope_filter:
        for k, v in scope_filter.items():
            must.append(FieldCondition(key=k, match=MatchValue(value=v)))
    # ADR 0080 §"Permission-filtered retrieval" — when the caller has
    # already done a BatchCheckPermission and built an allowed doc_ids
    # set, restrict retrieval to that set. Layered on top of the
    # readable_by group filter as defense-in-depth.
    if allowed_doc_ids is not None:
        if not allowed_doc_ids:
            # Empty set means caller has access to nothing; short-circuit.
            return []
        must.append(FieldCondition(
            key="document_id", match=MatchAny(any=list(allowed_doc_ids)),
        ))

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
            # Embed task writes the snippet as `text_snippet`; older payloads
            # used `text`. Read both so this works against both eras.
            "text": (r.payload.get("text_snippet")
                     or r.payload.get("text") or ""),
            "document_id": r.payload.get("document_id", ""),
            "document_title": r.payload.get("document_title", ""),
            "workspace_id": r.payload.get("workspace_id", ""),
            "version_id": r.payload.get("version_id", ""),
            "chunk_index": r.payload.get("chunk_index", 0),
            "page": r.payload.get("page_number"),
            "section_path": r.payload.get("section_path"),
            "start_char": r.payload.get("start_char"),
            "end_char": r.payload.get("end_char"),
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


def _retrieve(
    *,
    tenant_id: str,
    user_groups: list[str],
    question: str,
    scope: str,
    scope_id: str | None,
    top_k: int = 5,
) -> list[dict]:
    """Shared retrieval pipeline used by both ask() and stream_ask().
    Returns the top-k chunks with full payload metadata for citation
    rendering."""
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
        return [c for _, c in ranked_pairs[:top_k]]
    return fused[:top_k]


def _build_messages(*, system: str, history: list[dict] | None,
                    context: str, question: str) -> list[dict]:
    msgs = [{"role": "system", "content": system}]
    if history:
        msgs.extend(history[-6:])
    msgs.append({"role": "user", "content": f"Documents:\n{context}\n\nQuestion: {question}"})
    return msgs


def stream_ask(
    *,
    tenant_id: str,
    user_id: str,
    user_groups: list[str],
    question: str,
    document_id: str,
    conversation_history: list[dict] | None = None,
    model: str | None = None,
):
    """Generator yielding events for the SSE /qa endpoint.

    Yields tuples of (event_type, payload_dict). Event types:
      'citations'  — emitted once before LLM streaming begins
      'chunk'      — text token from the LLM
      'done'       — final event with model + token totals + full_text
    Caller is responsible for SSE-formatting and persistence.
    """
    from app import llm_gateway

    top_chunks = _retrieve(
        tenant_id=tenant_id, user_groups=user_groups,
        question=question, scope="document", scope_id=document_id,
    )
    citations = [
        {
            "chunk_index": c.get("chunk_index", 0),
            "text": c.get("text", "")[:400],
            "page": c.get("page"),
            "start_char": c.get("start_char"),
            "end_char": c.get("end_char"),
            "similarity_score": float(c.get("score", 0.0)),
            "document_id": c.get("document_id", ""),
            "version_id": c.get("version_id", ""),
        }
        for c in top_chunks
    ]
    yield "citations", {"citations": citations}

    if not top_chunks:
        msg = "I couldn't find relevant content in this document to answer your question."
        yield "chunk", {"text": msg}
        yield "done", {
            "full_text": msg, "citations": citations,
            "model": "", "input_tokens": 0, "output_tokens": 0, "cost_usd": 0.0,
        }
        return

    context = _build_context(top_chunks)
    messages = _build_messages(
        system=SYSTEM_PROMPT, history=conversation_history,
        context=context, question=question,
    )

    full_text_parts: list[str] = []
    final_meta: dict | None = None
    for text, is_final, meta in llm_gateway.stream_completion(
        tenant_id=tenant_id, messages=messages, model=model,
    ):
        if is_final:
            final_meta = meta or {}
            break
        if text:
            full_text_parts.append(text)
            yield "chunk", {"text": text}
    full_text = (final_meta or {}).get("full_text") or "".join(full_text_parts)
    yield "done", {
        "full_text": full_text,
        "citations": citations,
        "model": (final_meta or {}).get("model", ""),
        "input_tokens": (final_meta or {}).get("input_tokens", 0),
        "output_tokens": (final_meta or {}).get("output_tokens", 0),
        "cost_usd": (final_meta or {}).get("cost_usd", 0.0),
        "elapsed_ms": (final_meta or {}).get("elapsed_ms", 0),
    }


# ADR 0080 system prompt — "answer only from context" + the fixed
# "I don't know" sentinel the spec asks for so callers can detect
# not-in-corpus responses by string match.
#
# Each context block is headed by the source document's title and a
# [doc_id:page_X] marker. The prompt explicitly allows two things the
# old wording forbade and that 500'd simple questions into "I don't
# know": (1) answering meta-questions about which documents/sources the
# context contains (e.g. "what documents are available?") from those
# headers, and (2) citing by the human-readable title. The sentinel is
# now reserved for genuinely unanswerable *content* questions.
WORKSPACE_SYSTEM_PROMPT = (
    "You are a workspace assistant for SeDoc. Answer using only the "
    "information in the provided context. Each context block is headed "
    "by its source document's title followed by a [doc_id:page_X] "
    "marker. Cite sources inline next to the claims they support by "
    "copying the exact [doc_id:page_X] marker from the relevant block "
    "header. When the user asks which documents or sources are "
    "available, list the document titles shown in the context. "
    "Only if the context contains nothing relevant to the question, "
    "respond with the exact phrase \"I don't know.\" and nothing else."
)


def workspace_query(
    *,
    tenant_id: str,
    user_id: str,
    user_groups: list[str],
    question: str,
    workspace_id: str | None = None,
    allowed_doc_ids: list[str] | None = None,
    doc_titles: dict[str, str] | None = None,
    model: str | None = None,
) -> dict[str, Any]:
    """ADR 0080 — workspace-scoped RAG for the /rag/query endpoint.

    Different from `ask()` in three ways:
    - Permission-filtered retrieval: caller passes the BatchCheckPermission
      result as `allowed_doc_ids` so retrieval can't surface chunks from
      docs the user isn't allowed to read (defense-in-depth on top of the
      readable_by group filter that's already there).
    - Fixed "I don't know." sentinel for not-in-corpus questions so the
      caller can flag unanswerables without parsing free-form responses.
    - Citation shape matches §6.8 spec: each cite carries
      {doc_id, page, chunk_id, snippet, score} so the UI can render
      clickable links straight to the source page.

    Stateless per query — no conversation_history. Multi-turn workspace
    RAG is a follow-up tracked in the ADR.
    """
    start = time.monotonic()

    q_embedding = embed_single(question)
    scope_filter = None
    if workspace_id:
        scope_filter = {"workspace_id": workspace_id}

    vector_results = _vector_search(
        q_embedding, tenant_id, user_groups,
        scope_filter=scope_filter, allowed_doc_ids=allowed_doc_ids,
    )
    fused = _rrf_fuse([vector_results])

    top_20_texts = [c["text"] for c in fused[:20]]
    if top_20_texts:
        scores = rerank(question, top_20_texts)
        # Sort by score only. Without an explicit key, tuple comparison falls
        # back to the second element (the chunk dict) whenever two scores tie,
        # raising "TypeError: '<' not supported between instances of 'dict' and
        # 'dict'" and 500-ing the whole /rag/query request.
        ranked_pairs = sorted(zip(scores, fused[:20]), key=lambda p: p[0], reverse=True)
        top_pairs = ranked_pairs[:5]
    else:
        top_pairs = [(0.0, c) for c in fused[:5]]

    if not top_pairs:
        return {
            "answer": "I don't know.",
            "citations": [],
            "model": "",
            "input_tokens": 0,
            "output_tokens": 0,
            "cost_usd": 0.0,
            "elapsed_ms": int((time.monotonic() - start) * 1000),
        }

    # Build context with a title header + explicit doc_id marker the
    # prompt asks the LLM to cite back. Surfacing the title (resolved
    # at query time, falling back to a title baked into the chunk
    # payload, then the bare id) is what lets the model answer
    # "what documents are available?" and cite by name instead of UUID.
    titles = doc_titles or {}
    blocks: list[str] = []
    citations: list[dict] = []
    for score, c in top_pairs:
        doc_id = c.get("document_id", "")
        title = titles.get(doc_id) or c.get("document_title") or ""
        page = c.get("page")
        marker = f"[{doc_id}:page_{page}]" if page is not None else f"[{doc_id}]"
        header = f"{title} {marker}" if title else marker
        blocks.append(f"{header}\n{c.get('text', '')}")
        citations.append({
            "doc_id": doc_id,
            "document_title": title or None,
            "workspace_id": c.get("workspace_id") or None,
            "page": page,
            "chunk_id": c.get("chunk_index"),
            "section_path": c.get("section_path"),
            "snippet": (c.get("text") or "")[:240],
            "score": float(score),
        })
    context = "\n\n---\n\n".join(blocks)

    messages = [
        {"role": "system", "content": WORKSPACE_SYSTEM_PROMPT},
        {"role": "user", "content": f"Context:\n{context}\n\nQuestion: {question}"},
    ]

    from app import llm_gateway
    resp = llm_gateway.completion(
        tenant_id=tenant_id, messages=messages, model=model, max_tokens=2000,
    )

    return {
        "answer": resp["content"],
        "citations": citations,
        "model": resp["model"],
        "input_tokens": resp["input_tokens"],
        "output_tokens": resp["output_tokens"],
        "cost_usd": resp["cost_usd"],
        "elapsed_ms": int((time.monotonic() - start) * 1000),
    }


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
