"""Token-aware chunker shared by embed + RAG paths.

Kept separate from `tasks/embed.py` so we can unit-test chunk boundaries
without loading tiktoken+embedder+qdrant clients at import time.

Strategy (ADR 0055 floor + ADR 0063 enrichments):
- Sentence-split, pack sentences into a budget of
  `chunk_size_tokens` (default 512).
- Overlap the tail `chunk_overlap_tokens` (default 64) into the next
  chunk so semantic continuity across boundaries is preserved.
- Heading-aware: detect contract / outline section headers and stamp
  each emitted chunk with the cumulative section_path at that point.
  Falls through to None for unstructured prose.
- Page-aware: when callers pass per-page text, each chunk carries a
  page_number derived from the chunk's start_char.
"""
from __future__ import annotations

import re
from typing import Any, Optional

# Lazy tokenizer — one per process.
_enc = None


def _tokenizer():
    global _enc
    if _enc is None:
        import tiktoken
        _enc = tiktoken.get_encoding("cl100k_base")
    return _enc


# ---- Heading detection -------------------------------------------------

# Matches the most common contract / outline heading shapes. Each
# pattern returns a (depth, label) tuple:
#   - "ARTICLE 5"           → (1, "Article 5")
#   - "Section 5.2"         → (2, "Section 5.2")
#   - "5.2.1  Definitions"  → (3, "5.2.1 Definitions")
#   - "## Title"            → (2, "Title")          (markdown)
# Non-heading lines return None.

_PAT_ARTICLE = re.compile(r"^(?:ARTICLE|Article)\s+([IVXLCDM]+|\d+)\.?\s*(.*)$")
_PAT_SECTION = re.compile(r"^(?:SECTION|Section)\s+(\d+(?:\.\d+)*)\.?\s*(.*)$")
_PAT_CLAUSE  = re.compile(r"^(?:CLAUSE|Clause)\s+(\d+(?:\.\d+)*)\.?\s*(.*)$")
_PAT_NUMERIC = re.compile(r"^(\d+(?:\.\d+){1,4})\s+([A-Z][A-Za-z0-9 ,/'\-]{3,80})\s*$")
_PAT_MD      = re.compile(r"^(#{1,6})\s+(.+?)\s*$")
_PAT_ALLCAPS = re.compile(r"^([A-Z][A-Z0-9 ,/&'\-]{3,80})\s*$")


def _detect_heading(line: str) -> Optional[tuple[int, str]]:
    """Return (depth, label) when `line` looks like a heading; None
    otherwise. Depth follows the outline convention 1 = top, deeper
    digits = lower nesting. Used to maintain a section stack while
    walking the document."""
    s = line.strip()
    if not s or len(s) > 200:
        return None
    if (m := _PAT_ARTICLE.match(s)):
        return (1, f"Article {m.group(1)}{(' ' + m.group(2).strip()) if m.group(2).strip() else ''}".strip())
    if (m := _PAT_SECTION.match(s)):
        return (2, f"Section {m.group(1)}{(' ' + m.group(2).strip()) if m.group(2).strip() else ''}".strip())
    if (m := _PAT_CLAUSE.match(s)):
        # Depth = number of dotted segments in the clause id. So
        # "Clause 5"   → 1, "Clause 5.2" → 2, "Clause 5.2.1" → 3.
        # Matches the natural reading of legal-doc nesting.
        depth = m.group(1).count(".") + 1
        return (depth, f"Clause {m.group(1)}{(' ' + m.group(2).strip()) if m.group(2).strip() else ''}".strip())
    if (m := _PAT_NUMERIC.match(s)):
        # 1 → depth 1, 1.2 → depth 2, 1.2.3 → depth 3, etc.
        depth = m.group(1).count(".") + 1
        return (depth, f"{m.group(1)} {m.group(2).strip()}")
    if (m := _PAT_MD.match(s)):
        depth = len(m.group(1))
        return (depth, m.group(2).strip())
    if (m := _PAT_ALLCAPS.match(s)):
        # All-caps headings are common in legal docs but easy to
        # mistake for prose. Require at least one space + no trailing
        # punctuation that would suggest a sentence.
        if " " in s and not s.endswith((".", ",", ";", ":", "?", "!")):
            return (2, s.title())
    return None


def _section_path_at(heading_stack: list[tuple[int, str]]) -> Optional[str]:
    if not heading_stack:
        return None
    return " / ".join(label for _, label in heading_stack)


def _build_heading_index(text: str) -> list[tuple[int, int, str]]:
    """Walk `text` line by line, return [(line_start_offset, depth,
    label), ...] for every detected heading. Cursor used by the
    chunker to map a chunk's start_char to the active section path."""
    out: list[tuple[int, int, str]] = []
    cursor = 0
    for line in text.split("\n"):
        h = _detect_heading(line)
        if h is not None:
            out.append((cursor, h[0], h[1]))
        cursor += len(line) + 1  # +1 for the newline we split on
    return out


# ---- Page mapping ------------------------------------------------------

def _build_page_offsets(pages: list[dict] | None) -> list[tuple[int, int, int]]:
    """Returns one entry per page: (page_number, doc_start, doc_end).
    doc_start/doc_end are character offsets into the joined text the
    chunker is operating on (matches the worker's "\n\n".join shape).
    Empty list when callers don't pass per-page metadata — chunks
    will just have page_number=None."""
    if not pages:
        return []
    out: list[tuple[int, int, int]] = []
    cursor = 0
    for p in pages:
        text = (p.get("text_content") or p.get("text") or "")
        page_no = int(p.get("page_number") or len(out) + 1)
        page_start = cursor
        page_end = page_start + len(text)
        cursor = page_end + 2  # "\n\n" separator
        out.append((page_no, page_start, page_end))
    return out


def _page_for_offset(offsets: list[tuple[int, int, int]], char: int) -> Optional[int]:
    for page_no, s, e in offsets:
        if s <= char < e:
            return page_no
    return None


# ---- Chunker -----------------------------------------------------------

def chunk_text(
    text: str,
    *,
    chunk_size_tokens: int = 512,
    chunk_overlap_tokens: int = 64,
    pages: list[dict] | None = None,
) -> list[dict[str, Any]]:
    """Return a list of chunk dicts:

        {"chunk_index", "text", "token_count",
         "start_char", "end_char",
         "section_path",   # nullable; populated when text has headings
         "page_number"}    # nullable; populated when `pages` is provided

    `start_char` / `end_char` are offsets into the ORIGINAL input
    text — what the embed payload stores so the search service can
    highlight the originating region without a second fetch.

    Pass `pages` (list of {text_content, page_number}) to fill the
    page_number field — without it the chunker has no way to map a
    char offset back to a page.
    """
    if not text:
        return []
    enc = _tokenizer()
    headings = _build_heading_index(text)
    page_offsets = _build_page_offsets(pages)

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

    def _section_path_for_char(c: int) -> Optional[str]:
        # Walk the headings up to char c and maintain a depth-keyed
        # stack — pop entries whose depth is >= the current one.
        stack: list[tuple[int, str]] = []
        for h_start, depth, label in headings:
            if h_start > c:
                break
            while stack and stack[-1][0] >= depth:
                stack.pop()
            stack.append((depth, label))
        return _section_path_at(stack)

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
            "section_path": _section_path_for_char(start),
            "page_number": _page_for_offset(page_offsets, start),
        })

    for span_start, span_end, sent in spans:
        sent_tokens = enc.encode(sent)
        if (
            len(cur_tokens) + len(sent_tokens) > chunk_size_tokens
            and cur_tokens
        ):
            _flush()
            overlap_count = min(chunk_overlap_tokens, len(cur_tokens))
            if overlap_count > 0 and cur_spans:
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
    section_path: Optional[str] = None,
    page_number: Optional[int] = None,
) -> dict:
    """Spec-shaped Qdrant payload. tenant_id is mandatory and every
    search query MUST filter on it to enforce isolation. ADR 0063
    additions: section_path + page_number ride along so retrieval
    can return precise citations without a second fetch."""
    if not tenant_id:
        raise ValueError("tenant_id is required for embed payload")
    payload = {
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
    if section_path:
        payload["section_path"] = section_path
    if page_number is not None:
        payload["page_number"] = page_number
    return payload
