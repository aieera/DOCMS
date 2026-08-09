"""RAG Q&A pipeline — hybrid retrieval + RRF fusion + re-rank + LLM."""
from __future__ import annotations

import json
import logging
import re
import time
from typing import Any

from qdrant_client import QdrantClient
from qdrant_client.models import FieldCondition, Filter, MatchAny, MatchValue

from app.config import settings
from app.models.embedder import embed_single
from app.models.reranker import rerank
from app.worker import celery_app

log = logging.getLogger(__name__)

# The exact phrase the model is told to emit when the retrieved excerpts
# don't answer the question. Callers detect it by string match, and
# `reconcile_citations` uses it to guarantee a "found nothing" answer is
# never rendered next to a citation list (BUG-11).
NO_ANSWER_SENTINEL = "I couldn't find this in your documents."

# Numbered-citation prompt (BUG-12). The old wording asked for
# "[Document: title, Page: X]" while `_build_context` headed each block
# with "[Document: <uuid>, Chunk: N]" — so the model dutifully copied the
# raw UUID into user-facing prose. The context blocks now carry ONLY the
# ordinal marker + human-readable title, and the prompt forbids ids
# outright; the structured `citations` list is what the UI renders.
SYSTEM_PROMPT = (
    "You are a document assistant for SeDoc. Answer using ONLY the numbered "
    "source excerpts supplied in the Sources section. "
    "Cite the excerpts you relied on with their bracketed number placed "
    "immediately after the claim they support, e.g. [1] or [2][3]. "
    "NEVER write out a document id, version id, chunk number, UUID, storage "
    "key or any other internal identifier — the bracketed numbers are the "
    "only citation format allowed, and the user interface resolves them to "
    "document links. "
    f'If the excerpts do not contain the answer, reply exactly "{NO_ANSWER_SENTINEL}" '
    "and cite nothing."
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
            # `text` is the FULL chunk body — that is what the model must
            # see. `text_snippet` is the 500-char rendering snippet and is
            # only a fallback for points embedded before the split (those
            # older points stored the truncated body under `text`, which is
            # what starved the prompt down to ~300 input tokens — BUG-11).
            "text": (r.payload.get("text")
                     or r.payload.get("text_snippet") or ""),
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


def _source_header(marker: int, chunk: dict) -> str:
    """Human-readable block header for the LLM context.

    Deliberately carries NO document_id / version_id / chunk_index: the
    model echoes whatever it sees in the header, and the old
    "[Document: <uuid>, Chunk: N]" header is exactly how a raw UUID ended
    up in user-facing prose (BUG-12). The ordinal marker is the model's
    only handle on a source, and it maps 1:1 onto `citations[marker - 1]`.
    """
    title = (chunk.get("document_title") or "").strip() or "Untitled document"
    bits = [title]
    page = chunk.get("page")
    if page is not None:
        bits.append(f"page {page}")
    section = (chunk.get("section_path") or "").strip()
    if section:
        bits.append(section)
    return f"[{marker}] {' — '.join(bits)}"


def _build_context(chunks: list[dict], max_tokens: int | None = None) -> str:
    """Render the retrieved chunks as a numbered Sources block.

    Two BUG-11 guarantees:
      * the first chunk is ALWAYS included, token-truncated to the budget
        if it alone overflows — the old code `break`-ed on overflow and
        could hand the model an empty context while citations existed;
      * an oversized chunk skips itself rather than terminating the loop,
        so smaller later chunks still make it in.
    """
    import tiktoken
    if max_tokens is None:
        max_tokens = settings.rag_context_max_tokens
    enc = tiktoken.get_encoding("cl100k_base")
    parts: list[str] = []
    total = 0
    for c in chunks:
        text = c.get("text") or ""
        tokens = enc.encode(text)
        if total + len(tokens) > max_tokens:
            remaining = max_tokens - total
            if not parts and remaining > 0:
                # First (highest-ranked) chunk overflows on its own —
                # keep as much of it as fits rather than sending nothing.
                text = enc.decode(tokens[:remaining])
                tokens = tokens[:remaining]
            else:
                continue
        if not text:
            continue
        parts.append(f"{_source_header(len(parts) + 1, c)}\n{text}")
        total += len(tokens)
        if total >= max_tokens:
            break
    return "\n\n---\n\n".join(parts)


def build_citations(chunks: list[dict]) -> list[dict]:
    """Structured citations for the /qa + /qa/sync responses.

    `marker` is the 1-based ordinal the answer text cites as [N]; it is the
    contract the frontend uses to turn "[2]" into a chip linking to
    citations[1]. Every field the UI needs to build that link
    (workspace_id + document_id + page) rides along so no second fetch is
    needed.
    """
    return [
        {
            "marker": i,
            "document_id": c.get("document_id", ""),
            # document_title lets the chat render readable citations
            # ([N] Title) instead of raw ids.
            "document_title": c.get("document_title", ""),
            "version_id": c.get("version_id", ""),
            "workspace_id": c.get("workspace_id") or None,
            "page": c.get("page"),
            "chunk_index": c.get("chunk_index", 0),
            "section_path": c.get("section_path"),
            "start_char": c.get("start_char"),
            "end_char": c.get("end_char"),
            "text": (c.get("text") or "")[:400],
            "similarity_score": float(c.get("score", 0.0)),
        }
        for i, c in enumerate(chunks, start=1)
    ]


# ---- answer / citation reconciliation (BUG-11, BUG-12) -------------------

_UUID_RE = re.compile(
    r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
)
# Any bracketed run, plus the whitespace in front of it — that is where a
# leaked id lands, in every shape the old prompts produced:
# "[Document: <uuid>, Chunk: 0]", "[<uuid>:page_3]", "[Source: <uuid>]".
# The leading whitespace is captured so deleting a marker doesn't leave a
# double space behind.
_BRACKET_RE = re.compile(r"([ \t]*)(\[[^\[\]]{0,400}\])")
_BARE_UUID_RE = re.compile(
    r"([ \t]*)("
    r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
    r")"
)
_SPACE_BEFORE_PUNCT_RE = re.compile(r"[ \t]+([.,;:!?])")

# Phrases that mean "the corpus does not answer this". Compared against the
# whole (short) answer, never a substring of a long one — a real answer that
# merely mentions the phrase must keep its citations.
_NO_ANSWER_PREFIXES = (
    "i couldn't find this in your documents",
    "i could not find this in your documents",
    "i couldn't find relevant content",
    "i could not find relevant content",
    "i couldn't find relevant documents",
    "i could not find relevant documents",
    "i don't know",
    "i do not know",
)
_NO_ANSWER_MAX_CHARS = 240


def is_no_answer(text: str) -> bool:
    """True when `text` is the model's "not in the corpus" response.

    Short + starts with a known sentinel. The length guard keeps a real
    answer that happens to open with a hedge from losing its citations.
    """
    stripped = re.sub(r"\[\d+\]", "", text or "")
    stripped = stripped.strip().strip('"').strip("*").strip().rstrip(".").lower()
    if not stripped:
        return True
    if len(stripped) > _NO_ANSWER_MAX_CHARS:
        return False
    return any(stripped.startswith(p) for p in _NO_ANSWER_PREFIXES)


def reconcile_citations(answer: str, citations: list[dict]) -> list[dict]:
    """BUG-11 invariant: an answer and its citation list can never
    contradict each other.

    If the answer says nothing was found, the citation list is emptied —
    the UI must not show "I couldn't find this" above a quote from page 1
    of the very document that was searched. The other direction is
    enforced upstream: with no citations we never call the LLM at all.
    """
    if not citations:
        return []
    return [] if is_no_answer(answer) else citations


def _marker_map(citations: list[dict]) -> dict[str, int]:
    """document_id → marker, for rewriting a leaked id into its ordinal."""
    out: dict[str, int] = {}
    for c in citations:
        doc_id = str(c.get("document_id") or c.get("doc_id") or "")
        marker = c.get("marker")
        if doc_id and marker and doc_id not in out:
            out[doc_id] = int(marker)
    return out


def sanitize_answer(text: str, citations: list[dict]) -> str:
    """Strip internal identifiers the model may still have echoed.

    Defence in depth behind the prompt change: a bracketed run that
    contains a UUID becomes that source's [N] marker when we can resolve
    it, and is dropped otherwise. Bare UUIDs get the same treatment.

    An answer with no identifier in it is returned byte-for-byte — the
    whitespace tidy-up only runs when something was actually removed, so
    markdown indentation and code blocks survive untouched.
    """
    if not text:
        return text
    markers = _marker_map(citations)
    changed = False

    def _rewrite(ws: str, body: str, found: list[str]) -> str:
        nonlocal changed
        changed = True
        resolved = sorted({markers[u] for u in found if u in markers})
        if resolved:
            return ws + "".join(f"[{n}]" for n in resolved)
        return ""

    def _replace_bracket(m: re.Match) -> str:
        ws, body = m.group(1), m.group(2)
        found = _UUID_RE.findall(body)
        if not found:
            return m.group(0)
        return _rewrite(ws, body, found)

    def _replace_bare(m: re.Match) -> str:
        return _rewrite(m.group(1), m.group(2), [m.group(2)])

    out = _BRACKET_RE.sub(_replace_bracket, text)
    out = _BARE_UUID_RE.sub(_replace_bare, out)
    return _SPACE_BEFORE_PUNCT_RE.sub(r"\1", out) if changed else out


# A trailing run that could still grow into a bracketed marker or a UUID.
# Held back from the stream until the next chunk resolves it, so the raw
# id never flashes in the UI mid-generation.
_PARTIAL_UUID_TAIL_RE = re.compile(r"[0-9a-fA-F]{8}[0-9a-fA-F-]*$")
# Longest bracketed run we are willing to buffer. Past this the "[" was
# prose, not a marker, and holding the rest of the answer back would stall
# the stream.
_MAX_HELD_BACK_CHARS = 300


class AnswerSanitizer:
    """Incremental `sanitize_answer` for the SSE token stream.

    `feed` returns the portion that is safe to emit now; anything that
    could still turn into an identifier is buffered until `feed` sees more
    text or `flush` closes the stream.
    """

    def __init__(self, citations: list[dict]):
        self._citations = citations
        self._pending = ""

    def feed(self, text: str) -> str:
        self._pending += text or ""
        safe, self._pending = self._split_pending(self._pending)
        return sanitize_answer(safe, self._citations) if safe else ""

    def flush(self) -> str:
        tail, self._pending = self._pending, ""
        return sanitize_answer(tail, self._citations) if tail else ""

    @staticmethod
    def _split_pending(buf: str) -> tuple[str, str]:
        open_at = buf.rfind("[")
        if (open_at != -1 and "]" not in buf[open_at:]
                and len(buf) - open_at <= _MAX_HELD_BACK_CHARS):
            return buf[:open_at], buf[open_at:]
        m = _PARTIAL_UUID_TAIL_RE.search(buf)
        if m and len(m.group(0)) < 36:
            return buf[:m.start()], buf[m.start():]
        return buf, ""


def _rank_by_score(scores: list[float], chunks: list[dict], top_n: int) -> list[dict]:
    """Order chunks by rerank score (desc) and keep the top_n.

    MUST key on the score alone: bare sorted(zip(scores, chunks))
    falls through to comparing the chunk DICTS whenever two scores tie
    (TypeError: '<' not supported between 'dict' and 'dict'), and tie
    scores are the norm when the reranker falls back to uniform
    scoring — this 500ed every /ask with tied candidates."""
    ranked = sorted(zip(scores, chunks), key=lambda p: p[0], reverse=True)
    return [c for _, c in ranked[:top_n]]


def _retrieve(
    *,
    tenant_id: str,
    user_groups: list[str],
    question: str,
    scope: str,
    scope_id: str | None,
    allowed_doc_ids: list[str] | None = None,
    top_k: int | None = None,
) -> list[dict]:
    """Shared retrieval pipeline used by both ask() and stream_ask().
    Returns the top-k chunks with full payload metadata for citation
    rendering.

    `allowed_doc_ids`, when supplied, restricts retrieval to that set on top
    of the readable_by group filter (defense-in-depth) — required for
    workspace/global scope where there is no single-document scope filter."""
    if top_k is None:
        top_k = settings.rag_top_k
    q_embedding = embed_single(question)
    scope_filter = None
    if scope == "document" and scope_id:
        scope_filter = {"document_id": scope_id}
    elif scope == "workspace" and scope_id:
        scope_filter = {"workspace_id": scope_id}
    vector_results = _vector_search(
        q_embedding, tenant_id, user_groups, scope_filter,
        allowed_doc_ids=allowed_doc_ids,
    )
    fused = _rrf_fuse([vector_results])
    rerank_pool = fused[:settings.rag_rerank_candidates]
    pool_texts = [c["text"] for c in rerank_pool]
    if pool_texts:
        scores = rerank(question, pool_texts)
        return _rank_by_score(scores, rerank_pool, top_k)
    return fused[:top_k]


def _build_messages(*, system: str, history: list[dict] | None,
                    context: str, question: str) -> list[dict]:
    msgs = [{"role": "system", "content": system}]
    if history:
        msgs.extend(history[-6:])
    msgs.append({"role": "user", "content": f"Sources:\n{context}\n\nQuestion: {question}"})
    return msgs


def stream_ask(
    *,
    tenant_id: str,
    user_id: str,
    user_groups: list[str],
    question: str,
    scope: str = "document",
    scope_id: str | None = None,
    allowed_doc_ids: list[str] | None = None,
    conversation_history: list[dict] | None = None,
    model: str | None = None,
):
    """Generator yielding events for the SSE /qa endpoint.

    Yields tuples of (event_type, payload_dict). Event types:
      'citations'  — emitted once before LLM streaming begins
      'chunk'      — text token from the LLM
      'done'       — final event with model + token totals + full_text
    Caller is responsible for SSE-formatting and persistence.

    `scope` (document | workspace | global) + `scope_id` (document_id or
    workspace_id) set retrieval breadth. For workspace/global scope the caller
    MUST pass `allowed_doc_ids` (the user's readable docs) so retrieval is
    permission-filtered. Multi-turn memory is unchanged — `conversation_history`
    flows into the prompt for every scope.
    """
    from app import llm_gateway

    top_chunks = _retrieve(
        tenant_id=tenant_id, user_groups=user_groups,
        question=question, scope=scope, scope_id=scope_id,
        allowed_doc_ids=allowed_doc_ids,
    )
    citations = build_citations(top_chunks)
    yield "citations", {"citations": citations}

    if not top_chunks:
        where = "this document" if scope == "document" else "your documents"
        msg = f"I couldn't find relevant content in {where} to answer your question."
        yield "chunk", {"text": msg}
        yield "done", {
            "full_text": msg, "citations": [],
            "model": "", "input_tokens": 0, "output_tokens": 0, "cost_usd": 0.0,
        }
        return

    context = _build_context(top_chunks)
    messages = _build_messages(
        system=SYSTEM_PROMPT, history=conversation_history,
        context=context, question=question,
    )

    sanitizer = AnswerSanitizer(citations)
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
            safe = sanitizer.feed(text)
            if safe:
                yield "chunk", {"text": safe}
    tail = sanitizer.flush()
    if tail:
        yield "chunk", {"text": tail}

    raw_text = (final_meta or {}).get("full_text") or "".join(full_text_parts)
    full_text = sanitize_answer(raw_text, citations)
    # BUG-11: "couldn't find it" and a citation list can never ship
    # together. When the model reports nothing was found we drop the
    # citations and re-emit the (now empty) list so the UI clears its
    # chips — clients MUST treat the last `citations` event / the `done`
    # payload as authoritative.
    final_citations = reconcile_citations(full_text, citations)
    if final_citations != citations:
        yield "citations", {"citations": final_citations}
    yield "done", {
        "full_text": full_text,
        "citations": final_citations,
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
# Each context block is headed by an ordinal marker + the source
# document's title. The prompt explicitly allows two things the old
# wording forbade and that 500'd simple questions into "I don't know":
# (1) answering meta-questions about which documents/sources the context
# contains (e.g. "what documents are available?") from those headers, and
# (2) citing by the human-readable title. The sentinel is now reserved
# for genuinely unanswerable *content* questions.
#
# BUG-12: the header used to be "[<doc_id>:page_X]" and the prompt told
# the model to copy it verbatim, which put raw UUIDs in the answer text.
# The marker is now the ordinal into the structured `citations` list.
WORKSPACE_SYSTEM_PROMPT = (
    "You are a workspace assistant for SeDoc. Answer using only the "
    "information in the provided context. Each context block is headed "
    "by a bracketed number followed by its source document's title. "
    "Cite sources inline next to the claims they support by copying that "
    "bracketed number, e.g. [1] or [2][3]. "
    "NEVER write out a document id, version id, chunk number, UUID or any "
    "other internal identifier — the bracketed numbers are the only "
    "citation format allowed. "
    "When the user asks which documents or sources are "
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

    top_k = settings.rag_top_k
    rerank_pool = fused[:settings.rag_rerank_candidates]
    pool_texts = [c["text"] for c in rerank_pool]
    if pool_texts:
        scores = rerank(question, pool_texts)
        # Sort by score only. Without an explicit key, tuple comparison falls
        # back to the second element (the chunk dict) whenever two scores tie,
        # raising "TypeError: '<' not supported between instances of 'dict' and
        # 'dict'" and 500-ing the whole /rag/query request.
        ranked_pairs = sorted(zip(scores, rerank_pool), key=lambda p: p[0], reverse=True)
        top_pairs = ranked_pairs[:top_k]
    else:
        top_pairs = [(0.0, c) for c in fused[:top_k]]

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

    # Build context with an ordinal marker + title header the prompt asks
    # the LLM to cite back. Surfacing the title (resolved at query time,
    # falling back to a title baked into the chunk payload) is what lets
    # the model answer "what documents are available?" and cite by name.
    # The document_id is deliberately absent from the header — see BUG-12.
    titles = doc_titles or {}
    blocks: list[str] = []
    citations: list[dict] = []
    for marker, (score, c) in enumerate(top_pairs, start=1):
        doc_id = c.get("document_id", "")
        title = titles.get(doc_id) or c.get("document_title") or ""
        page = c.get("page")
        enriched = dict(c)
        enriched["document_title"] = title
        blocks.append(f"{_source_header(marker, enriched)}\n{c.get('text', '')}")
        citations.append({
            "marker": marker,
            "doc_id": doc_id,
            # `document_id` alias so a single frontend helper can read
            # either citation shape (/qa vs /rag/query).
            "document_id": doc_id,
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

    answer = sanitize_answer(resp["content"], citations)
    return {
        "answer": answer,
        "citations": reconcile_citations(answer, citations),
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

    top_k = settings.rag_top_k
    rerank_pool = fused[:settings.rag_rerank_candidates]
    pool_texts = [c["text"] for c in rerank_pool]
    if pool_texts:
        scores = rerank(question, pool_texts)
        top_chunks = _rank_by_score(scores, rerank_pool, top_k)
    else:
        top_chunks = fused[:top_k]

    context = _build_context(top_chunks)
    if not context:
        return {
            "answer": "I couldn't find relevant documents to answer your question.",
            "sources": [],
            "citations": [],
            "elapsed_ms": int((time.monotonic() - start) * 1000),
        }

    citations = build_citations(top_chunks)
    messages = [{"role": "system", "content": SYSTEM_PROMPT}]
    if conversation_history:
        messages.extend(conversation_history[-6:])
    messages.append({"role": "user", "content": f"Sources:\n{context}\n\nQuestion: {question}"})

    from app import llm_gateway
    resp = llm_gateway.completion(tenant_id=tenant_id, messages=messages, max_tokens=2000)

    answer = sanitize_answer(resp["content"], citations)
    final_citations = reconcile_citations(answer, citations)
    sources = [
        {"document_id": c["document_id"], "chunk_index": c["chunk_index"]}
        for c in final_citations
    ]

    return {
        "answer": answer,
        "sources": sources,
        "citations": final_citations,
        "model": resp["model"],
        "input_tokens": resp["input_tokens"],
        "output_tokens": resp["output_tokens"],
        "cost_usd": resp["cost_usd"],
        "elapsed_ms": int((time.monotonic() - start) * 1000),
    }
