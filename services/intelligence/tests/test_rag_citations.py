"""BUG-12 / BUG-11 — citations are structured data, never inline ids, and
an answer can never contradict its own citation list.

Two production incidents are pinned here:

  BUG-12  /ask answers ended with "[Document: c4282a2a-…, Chunk: 0]" —
          the raw Qdrant document id, pasted into user-facing prose
          because `_build_context` put it in the block header and the
          system prompt told the model to cite the header back.

  BUG-11  "I couldn't find this in your documents." was rendered directly
          above a citation quoting page 1 of the very document that was
          searched, on a ~307-token prompt (one 500-char-truncated chunk).
"""
from __future__ import annotations

import re
from unittest import mock

# `_build_context` tokenises with tiktoken, which downloads cl100k_base on
# first use. Importing litellm points TIKTOKEN_CACHE_DIR at its bundled
# copy, so this file passes on an offline runner instead of depending on
# some alphabetically-earlier test having imported litellm first.
import litellm  # noqa: F401
import pytest

from app.chunker import build_payload
from app.tasks.rag import (
    NO_ANSWER_SENTINEL,
    SYSTEM_PROMPT,
    WORKSPACE_SYSTEM_PROMPT,
    AnswerSanitizer,
    _build_context,
    _source_header,
    build_citations,
    is_no_answer,
    reconcile_citations,
    sanitize_answer,
    stream_ask,
    workspace_query,
)

DOC_A = "c4282a2a-7b74-44ce-9f35-b5ab019edc1a"
DOC_B = "11111111-2222-3333-4444-555555555555"

_UUID_IN_TEXT = re.compile(
    r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
)


def _chunk(*, doc_id=DOC_A, title="Q4 Financial Report", idx=0, page=1,
           text="Revenue grew 12% year on year.", score=0.9,
           workspace_id="ws-1", section_path=None) -> dict:
    return {
        "id": f"point-{idx}",
        "score": score,
        "text": text,
        "document_id": doc_id,
        "document_title": title,
        "version_id": "ver-1",
        "workspace_id": workspace_id,
        "chunk_index": idx,
        "page": page,
        "section_path": section_path,
        "start_char": 0,
        "end_char": len(text),
    }


# ---- BUG-12: no internal identifiers reach the model or the answer ------

def test_source_header_carries_no_identifiers():
    header = _source_header(1, _chunk())
    assert header.startswith("[1] ")
    assert "Q4 Financial Report" in header
    assert DOC_A not in header
    assert "Chunk" not in header


def test_build_context_numbers_sources_and_hides_ids():
    ctx = _build_context([_chunk(idx=0), _chunk(doc_id=DOC_B, title="Policy", idx=1)])
    assert "[1] Q4 Financial Report" in ctx
    assert "[2] Policy" in ctx
    assert not _UUID_IN_TEXT.search(ctx), "context leaked a document id to the model"
    assert "Chunk:" not in ctx


def test_system_prompts_forbid_raw_identifiers():
    for prompt in (SYSTEM_PROMPT, WORKSPACE_SYSTEM_PROMPT):
        lowered = prompt.lower()
        assert "[1]" in prompt, "prompt must show the numbered citation format"
        assert "never write out a document id" in lowered


def test_sanitize_rewrites_leaked_marker_to_its_ordinal():
    citations = build_citations([_chunk()])
    out = sanitize_answer(
        f"Revenue grew 12%. [Document: {DOC_A}, Chunk: 0]", citations,
    )
    assert out == "Revenue grew 12%. [1]"
    assert not _UUID_IN_TEXT.search(out)


def test_sanitize_drops_unresolvable_identifiers():
    citations = build_citations([_chunk()])
    out = sanitize_answer(f"See [Document: {DOC_B}] for detail.", citations)
    assert not _UUID_IN_TEXT.search(out)
    assert "Document:" not in out


def test_sanitize_keeps_ordinary_brackets_and_markers():
    citations = build_citations([_chunk()])
    text = "Revenue grew 12% [1] per the summary [see appendix]."
    assert sanitize_answer(text, citations) == text


def test_sanitize_is_byte_for_byte_when_there_is_nothing_to_strip():
    """No identifier => no reformatting. Collapsing whitespace
    unconditionally would mangle markdown lists and code blocks."""
    citations = build_citations([_chunk()])
    markdown = (
        "Key findings:\n\n"
        "1. Revenue grew 12% [1]\n"
        "   - driven by EMEA\n\n"
        "```\n"
        "    indented code   line\n"
        "```\n"
    )
    assert sanitize_answer(markdown, citations) == markdown


def test_stream_sanitizer_does_not_stall_on_an_unclosed_bracket():
    san = AnswerSanitizer(build_citations([_chunk()]))
    long_prose = "[unclosed " + "word " * 200
    emitted = san.feed(long_prose)
    assert emitted, "a long unclosed bracket must not hold the stream hostage"


def test_stream_sanitizer_never_emits_a_partial_or_whole_uuid():
    citations = build_citations([_chunk()])
    san = AnswerSanitizer(citations)
    # Token-by-token delivery, exactly how the id leaked into the UI.
    tokens = ["Revenue ", "grew ", "12%. ", "[Doc", "ument: ", DOC_A[:18],
              DOC_A[18:], ", Chunk", ": 0]"]
    emitted = "".join(san.feed(t) for t in tokens) + san.flush()
    assert not _UUID_IN_TEXT.search(emitted)
    assert emitted == "Revenue grew 12%. [1]"


def test_stream_sanitizer_is_lossless_for_clean_text():
    san = AnswerSanitizer(build_citations([_chunk()]))
    tokens = ["The ", "answer ", "is ", "42", "."]
    out = "".join(san.feed(t) for t in tokens) + san.flush()
    assert out == "The answer is 42."


# ---- BUG-12: the structured citation contract --------------------------

def test_citations_carry_every_field_the_ui_needs_to_link():
    citations = build_citations([_chunk(section_path="Article 5")])
    assert len(citations) == 1
    c = citations[0]
    # `marker` is the join key between "[1]" in the prose and the chip.
    assert c["marker"] == 1
    assert c["document_id"] == DOC_A
    assert c["document_title"] == "Q4 Financial Report"
    assert c["workspace_id"] == "ws-1"
    assert c["version_id"] == "ver-1"
    assert c["page"] == 1
    assert c["chunk_index"] == 0
    assert c["section_path"] == "Article 5"
    assert c["start_char"] == 0
    assert c["end_char"] > 0
    assert c["text"]
    assert c["similarity_score"] == pytest.approx(0.9)


def test_markers_are_sequential_and_index_the_citation_list():
    chunks = [_chunk(idx=i, doc_id=DOC_A if i % 2 else DOC_B) for i in range(3)]
    citations = build_citations(chunks)
    assert [c["marker"] for c in citations] == [1, 2, 3]
    for c in citations:
        assert citations[c["marker"] - 1] is c


# ---- BUG-11: no truncation between retrieval and the prompt ------------

def test_embed_payload_keeps_the_full_chunk_body():
    body = "x" * 4000
    p = build_payload(
        tenant_id="t", document_id="d", version_id="v", chunk_index=0,
        start_char=0, end_char=4000, token_count=1000, text_snippet=body,
        readable_by=None,
    )
    # `text` feeds the model and must be complete; `text_snippet` is the
    # render-only preview.
    assert p["text"] == body
    assert len(p["text_snippet"]) == 500


def test_build_context_always_includes_the_top_chunk():
    """An oversized top chunk used to `break` on the first iteration and
    hand the model an EMPTY context while citations existed."""
    ctx = _build_context([_chunk(text="word " * 5000)], max_tokens=50)
    assert ctx, "top chunk must never be dropped entirely"
    assert ctx.startswith("[1] ")


def test_build_context_skips_an_oversized_chunk_but_keeps_later_ones():
    chunks = [
        _chunk(idx=0, title="Small", text="short one."),
        _chunk(idx=1, title="Huge", text="word " * 5000),
        _chunk(idx=2, title="AlsoSmall", text="another short one."),
    ]
    ctx = _build_context(chunks, max_tokens=200)
    assert "[1] Small" in ctx
    assert "[2] AlsoSmall" in ctx


# ---- BUG-11: answer and citations can never contradict -----------------

def test_is_no_answer_detects_the_sentinel():
    assert is_no_answer(NO_ANSWER_SENTINEL)
    assert is_no_answer("I couldn't find this in your documents. [1]")
    assert is_no_answer("I don't know.")
    assert is_no_answer("")


def test_is_no_answer_does_not_fire_on_a_real_answer():
    assert not is_no_answer("Revenue grew 12% year on year.")
    long_answer = (
        "I couldn't find this in your documents section on pricing, but the "
        "renewal terms are covered in detail: " + "detail. " * 40
    )
    assert not is_no_answer(long_answer)


def test_reconcile_strips_citations_from_a_not_found_answer():
    citations = build_citations([_chunk()])
    assert reconcile_citations(NO_ANSWER_SENTINEL, citations) == []
    assert reconcile_citations("Revenue grew 12%. [1]", citations) == citations


def _stub_stream(text: str):
    def _gen(*_a, **_kw):
        yield text, False, None
        yield "", True, {
            "model": "stub-llm", "input_tokens": 900, "output_tokens": 9,
            "cost_usd": 0.001, "elapsed_ms": 10, "full_text": text,
        }
    return _gen


def _events(llm_text: str, chunks: list[dict]) -> list[tuple[str, dict]]:
    with mock.patch("app.tasks.rag._retrieve", return_value=chunks), \
         mock.patch("app.llm_gateway.stream_completion", new=_stub_stream(llm_text)):
        return list(stream_ask(
            tenant_id="t1", user_id="u1", user_groups=["g1"],
            question="What are the key findings?",
            scope="document", scope_id=DOC_A,
        ))


def test_stream_ask_clears_citations_when_the_model_found_nothing():
    events = _events(NO_ANSWER_SENTINEL, [_chunk()])
    done = events[-1][1]
    assert done["full_text"] == NO_ANSWER_SENTINEL
    assert done["citations"] == [], "no-answer must never ship with citations"
    # The pre-generation list is re-emitted as empty so the UI clears chips.
    citation_events = [p["citations"] for t, p in events if t == "citations"]
    assert citation_events[0], "chips are shown optimistically while generating"
    assert citation_events[-1] == []


def test_stream_ask_keeps_citations_for_a_real_answer():
    events = _events("Revenue grew 12% [1].", [_chunk()])
    done = events[-1][1]
    assert done["citations"], "a real answer must keep its sources"
    assert done["citations"][0]["marker"] == 1


def test_stream_ask_sanitizes_a_leaked_id_out_of_the_stream():
    leak = f"Revenue grew 12%. [Document: {DOC_A}, Chunk: 0]"
    events = _events(leak, [_chunk()])
    streamed = "".join(p["text"] for t, p in events if t == "chunk")
    done = events[-1][1]
    assert not _UUID_IN_TEXT.search(streamed)
    assert not _UUID_IN_TEXT.search(done["full_text"])
    assert done["full_text"].endswith("[1]")


def test_stream_ask_with_no_chunks_shows_no_citations():
    with mock.patch("app.tasks.rag._retrieve", return_value=[]):
        events = list(stream_ask(
            tenant_id="t1", user_id="u1", user_groups=[],
            question="q", scope="document", scope_id=DOC_A,
        ))
    assert all(not p.get("citations") for t, p in events if t in ("citations", "done"))
    assert "couldn't find" in events[1][1]["text"].lower()


# ---- /rag/query (workspace scope) — same guarantees --------------------

def _stub_completion(content: str):
    def _fn(**_kw):
        return {
            "content": content, "model": "stub-llm",
            "input_tokens": 900, "output_tokens": 9, "cost_usd": 0.002,
        }
    return _fn


def _workspace_out(llm_text: str):
    with mock.patch("app.tasks.rag.embed_single", return_value=[0.0] * 8), \
         mock.patch("app.tasks.rag._vector_search", return_value=[_chunk()]), \
         mock.patch("app.tasks.rag.rerank", return_value=[0.9]), \
         mock.patch("app.llm_gateway.completion", side_effect=_stub_completion(llm_text)):
        return workspace_query(
            tenant_id="t1", user_id="u1", user_groups=[], question="q",
            workspace_id="ws-1", allowed_doc_ids=[DOC_A],
        )


def test_workspace_query_never_leaks_a_doc_id_into_the_answer():
    out = _workspace_out(f"Renewal is annual. [{DOC_A}:page_1]")
    assert not _UUID_IN_TEXT.search(out["answer"])
    assert out["answer"].endswith("[1]")
    assert out["citations"][0]["marker"] == 1
    # Both id spellings so one frontend helper reads either citation shape.
    assert out["citations"][0]["doc_id"] == DOC_A
    assert out["citations"][0]["document_id"] == DOC_A


def test_workspace_query_drops_citations_on_the_sentinel():
    out = _workspace_out("I don't know.")
    assert out["citations"] == []
