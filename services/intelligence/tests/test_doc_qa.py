"""Document Q&A — pure logic + stream_ask orchestration. Qdrant + LLM stubbed."""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.rag import _build_messages, stream_ask


def test_build_messages_caps_history_at_six():
    history = [{"role": "user", "content": str(i)} for i in range(20)]
    msgs = _build_messages(system="sys", history=history,
                           context="docs", question="q")
    # 1 system + 6 history + 1 user = 8
    assert len(msgs) == 8
    assert msgs[0]["role"] == "system"
    # Last entries of history kept (truncate front).
    assert msgs[1]["content"] == "14"
    assert msgs[6]["content"] == "19"
    assert msgs[-1]["content"].endswith("Question: q")


def test_build_messages_handles_no_history():
    msgs = _build_messages(system="sys", history=None,
                           context="docs", question="q")
    assert len(msgs) == 2
    assert msgs[0]["role"] == "system"
    assert msgs[1]["role"] == "user"


# ---- stream_ask -----------------------------------------------------------

def _stub_llm_stream(*_args, **_kwargs):
    """Yield (text, is_final, meta). Mirrors llm_gateway.stream_completion."""
    for text in ["The ", "answer ", "is ", "42."]:
        yield text, False, None
    yield "", True, {
        "model": "stub-llm",
        "input_tokens": 100,
        "output_tokens": 4,
        "cost_usd": 0.001,
        "elapsed_ms": 50,
        "full_text": "The answer is 42.",
    }


def test_stream_ask_emits_citations_then_chunks_then_done():
    chunk_payload = {
        "text_snippet": "Lorem ipsum about the answer.",
        "page_number": 3,
        "start_char": 100,
        "end_char": 200,
        "document_id": "doc-1",
        "version_id": "ver-1",
        "chunk_index": 7,
    }
    fake_top = [{
        "id": "1", "score": 0.9, "text": chunk_payload["text_snippet"],
        "document_id": "doc-1", "version_id": "ver-1",
        "chunk_index": 7, "page": 3,
        "start_char": 100, "end_char": 200,
    }]
    with mock.patch("app.tasks.rag._retrieve", return_value=fake_top), \
         mock.patch("app.tasks.rag._build_context", return_value="ctx"), \
         mock.patch("app.llm_gateway.stream_completion", new=_stub_llm_stream):
        events = list(stream_ask(
            tenant_id="t1", user_id="u1", user_groups=["g1"],
            question="What is the answer?", document_id="doc-1",
        ))

    types = [e[0] for e in events]
    assert types[0] == "citations"
    assert "chunk" in types
    assert types[-1] == "done"

    citations_evt = events[0][1]["citations"]
    assert len(citations_evt) == 1
    c = citations_evt[0]
    assert c["chunk_index"] == 7
    assert c["page"] == 3
    assert c["start_char"] == 100
    assert c["end_char"] == 200
    assert c["similarity_score"] == pytest.approx(0.9)

    text_pieces = [e[1]["text"] for e in events if e[0] == "chunk"]
    assert "".join(text_pieces) == "The answer is 42."

    done = events[-1][1]
    assert done["full_text"] == "The answer is 42."
    assert done["model"] == "stub-llm"
    assert done["output_tokens"] == 4


def test_stream_ask_no_results_returns_friendly_message():
    with mock.patch("app.tasks.rag._retrieve", return_value=[]):
        events = list(stream_ask(
            tenant_id="t1", user_id="u1", user_groups=["g1"],
            question="Q?", document_id="doc-1",
        ))
    types = [e[0] for e in events]
    assert types == ["citations", "chunk", "done"]
    chunk_text = events[1][1]["text"]
    assert "couldn't find" in chunk_text.lower()
    # No LLM was invoked (no tokens charged).
    assert events[-1][1]["output_tokens"] == 0


def test_stream_ask_passes_document_scope_to_retrieve():
    captured: dict = {}

    def capture(**kwargs):
        captured.update(kwargs)
        return []

    with mock.patch("app.tasks.rag._retrieve", side_effect=capture):
        list(stream_ask(
            tenant_id="t1", user_id="u1", user_groups=[],
            question="q", document_id="doc-xyz",
        ))
    assert captured["scope"] == "document"
    assert captured["scope_id"] == "doc-xyz"
    assert captured["tenant_id"] == "t1"
