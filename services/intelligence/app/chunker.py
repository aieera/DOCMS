"""Token-aware chunker shared by embed + RAG paths.

Kept separate from `tasks/embed.py` so we can unit-test chunk boundaries
without loading tiktoken+embedder+qdrant clients at import time.

Strategy: sentence-split, pack sentences into a budget of
`chunk_size_tokens` (default 512), overlap the tail
`chunk_overlap_tokens` (default 64) into the next chunk so semantic
continuity across boundaries is preserved.
"""
from __future__ import annotations

import re
from typing import Any

# Lazy tokenizer — one per process.
_enc = None


def _tokenizer():
    global _enc
    if _enc is None:
        import tiktoken
        _enc = tiktoken.get_encoding("cl100k_base")
    return _enc


def chunk_text(
    text: str, *, chunk_size_tokens: int = 512, chunk_overlap_tokens: int = 64
) -> list[dict[str, Any]]:
    """Return a list of chunk dicts:

        {"chunk_index", "text", "token_count", "start_char", "end_char"}

    `start_char` / `end_char` are offsets into the ORIGINAL input
    text — what the embed payload stores so the search service can
    highlight the originating region without a second fetch.
    """
    if not text:
        return []
    enc = _tokenizer()

    # Split on sentence boundaries but keep character spans.
    spans: list[tuple[int, int, str]] = []
    last = 0
    for m in re.finditer(r'(?<=[.!?])\s+', text):
        end = m.start()
        if end > last:
            spans.append((last, end, text[last:end]))
        last = m.end()
    if last < len(text):
        spans.append((last, len(text), text[last:]))
    if not spans:
        spans = [(0, len(text), text)]

    chunks: list[dict[str, Any]] = []
    cur_tokens: list[int] = []
    cur_spans: list[tuple[int, int]] = []

    def _flush():
        if not cur_spans:
            return
        start = cur_spans[0][0]
        end = cur_spans[-1][1]
        chunks.append({
            "chunk_index": len(chunks),
            "text": text[start:end],
            "token_count": len(cur_tokens),
            "start_char": start,
            "end_char": end,
        })

    for span_start, span_end, sent in spans:
        sent_tokens = enc.encode(sent)
        if (
            len(cur_tokens) + len(sent_tokens) > chunk_size_tokens
            and cur_tokens
        ):
            _flush()
            # Overlap: carry the last `chunk_overlap_tokens` of the
            # flushed chunk into the new one. We approximate the
            # char boundary by re-decoding those tokens' text length
            # and walking back from end.
            overlap_count = min(chunk_overlap_tokens, len(cur_tokens))
            if overlap_count > 0 and cur_spans:
                # Keep a single synthetic overlap span based on the
                # tail of the just-flushed chunk.
                tail_tokens = cur_tokens[-overlap_count:]
                tail_text = enc.decode(tail_tokens)
                new_start = max(0, cur_spans[-1][1] - len(tail_text))
                cur_tokens = list(tail_tokens)
                cur_spans = [(new_start, cur_spans[-1][1])]
            else:
                cur_tokens = []
                cur_spans = []
        cur_tokens.extend(sent_tokens)
        cur_spans.append((span_start, span_end))

    _flush()
    return chunks


# ---- Embed point / payload helpers (kept here, zero-dep) ---------------


def point_id(tenant_id: str, document_id: str, version_id: str, chunk_index: int) -> str:
    """Deterministic UUIDv5 for a chunk. Tenant-scoped by design so
    two tenants uploading identical (document_id, version_id) still
    produce distinct Qdrant point ids; cross-tenant overwrites are
    prevented at the id layer even before payload filters."""
    import uuid
    return str(uuid.uuid5(
        uuid.NAMESPACE_URL,
        f"{tenant_id}/{document_id}/{version_id}/{chunk_index}",
    ))


def build_payload(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    chunk_index: int,
    start_char: int,
    end_char: int,
    token_count: int,
    text_snippet: str,
    readable_by: list | None = None,
) -> dict:
    """Spec-shaped Qdrant payload. tenant_id is mandatory and every
    search query MUST filter on it to enforce isolation."""
    if not tenant_id:
        raise ValueError("tenant_id is required for embed payload")
    return {
        "tenant_id": tenant_id,
        "document_id": document_id,
        "version_id": version_id,
        "chunk_index": chunk_index,
        "start_char": start_char,
        "end_char": end_char,
        "token_count": token_count,
        "text": text_snippet[:500],
        "readable_by": readable_by or ["everyone"],
    }
