"""ADR 0063 — workspace_query unit tests.

Focus on the pieces that are pure logic + the cross-tenant denial
invariant that the permission pre-filter guarantees. Qdrant + LLM are
stubbed out (same shape as test_doc_qa.py for /qa).

The cross-tenant test is the load-bearing one: even if a chunk in
Qdrant is misindexed (wrong tenant_id on the payload), the
allowed_doc_ids gate must still keep retrieval from surfacing it.
"""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.rag import workspace_query, _vector_search


# ---- _vector_search permission filter ---------------------------------

def test_vector_search_short_circuits_on_empty_allowed_doc_ids():
    """Empty list ≠ None — empty means "user has access to nothing"
    and we must not even hit Qdrant. Critical privacy gate: a buggy
    callsite that forgets to populate the allowed list shouldn't
    accidentally return tenant-wide results."""
    with mock.patch("app.tasks.rag._qdrant") as qdrant:
        out = _vector_search(
            q_embedding=[0.0] * 8,
            tenant_id="t1",
            user_groups=["g1"],
            allowed_doc_ids=[],  # explicit empty
        )
    assert out == []
    qdrant.assert_not_called()


def test_vector_search_appends_doc_id_filter_when_allowed_doc_ids_set():
    captured: dict = {}

    class _FakeClient:
        def search(self, **kwargs):
            captured.update(kwargs)
            return []

    with mock.patch("app.tasks.rag._qdrant", return_value=_FakeClient()):
        _vector_search(
            q_embedding=[0.0] * 8,
            tenant_id="t1",
            user_groups=["g1"],
            allowed_doc_ids=["doc-A", "doc-B"],
        )

    must = captured["query_filter"].must
    keys = [c.key for c in must]
    assert "tenant_id" in keys
    assert "readable_by" in keys
    assert "document_id" in keys


def test_vector_search_omits_doc_id_filter_when_allowed_is_none():
    """None (the default) means "no caller-provided allow-list" —
    fall back to the existing tenant + readable_by filters only.
    Used by the /qa path which has its own per-doc auth."""
    captured: dict = {}

    class _FakeClient:
        def search(self, **kwargs):
            captured.update(kwargs)
            return []

    with mock.patch("app.tasks.rag._qdrant", return_value=_FakeClient()):
        _vector_search(
            q_embedding=[0.0] * 8,
            tenant_id="t1",
            user_groups=["g1"],
            allowed_doc_ids=None,
        )

    keys = [c.key for c in captured["query_filter"].must]
    assert "document_id" not in keys


# ---- workspace_query orchestration -----------------------------------

def _stub_llm_completion(**_kwargs):
    return {
        "content": "Renewal is annual on the anniversary date. [doc-A:page_3]",
        "model": "stub-llm",
        "input_tokens": 200,
        "output_tokens": 30,
        "cost_usd": 0.002,
    }


def _fake_chunk(*, doc_id="doc-A", page=3, idx=7, text="Lorem ipsum.",
                workspace_id="ws-1", section_path="Article 5") -> dict:
    return {
        "id": f"q-{idx}",
        "score": 0.85,
        "text": text,
        "document_id": doc_id,
        "workspace_id": workspace_id,
        "version_id": "ver-1",
        "chunk_index": idx,
        "page": page,
        "section_path": section_path,
    }


def test_workspace_query_returns_i_dont_know_when_no_chunks():
    """Empty retrieval -> sentinel response. Caller (UI) can detect
    this by exact string match and hide thumbs-feedback in that
    case (an "I don't know." isn't a quality signal)."""
    with mock.patch("app.tasks.rag.embed_single", return_value=[0.0] * 8), \
         mock.patch("app.tasks.rag._vector_search", return_value=[]):
        out = workspace_query(
            tenant_id="t1", user_id="u1", user_groups=[],
            question="anything",
            workspace_id="ws-1",
            allowed_doc_ids=["doc-A"],
        )
    assert out["answer"] == "I don't know."
    assert out["citations"] == []
    assert out["model"] == ""
    assert out["input_tokens"] == 0


def test_workspace_query_emits_spec_shaped_citations():
    chunks = [_fake_chunk()]
    with mock.patch("app.tasks.rag.embed_single", return_value=[0.0] * 8), \
         mock.patch("app.tasks.rag._vector_search", return_value=chunks), \
         mock.patch("app.tasks.rag.rerank", return_value=[0.9]), \
         mock.patch("app.llm_gateway.completion", side_effect=_stub_llm_completion):
        out = workspace_query(
            tenant_id="t1", user_id="u1", user_groups=[],
            question="when does it renew?",
            workspace_id="ws-1",
            allowed_doc_ids=["doc-A"],
        )
    assert out["answer"].startswith("Renewal is annual")
    assert len(out["citations"]) == 1
    cite = out["citations"][0]
    # Spec §6.8: citations carry doc_id + page + chunk_id + section_path
    # + snippet + score so the UI can render clickable links.
    assert cite["doc_id"] == "doc-A"
    assert cite["page"] == 3
    assert cite["chunk_id"] == 7
    assert cite["section_path"] == "Article 5"
    assert cite["workspace_id"] == "ws-1"
    assert cite["snippet"]
    assert cite["score"] > 0


def test_workspace_query_cross_tenant_denial_with_empty_allowed():
    """The privacy invariant: even if Qdrant has matching chunks for
    the question, an allowed_doc_ids=[] (e.g. user not a member of
    any workspace, or asked about another tenant's workspace) must
    short-circuit to "I don't know." with zero LLM calls.

    Equivalent to the §6.8 deliverable "permission-filtered retrieval"
    failing closed when the caller hasn't proven access."""
    llm_mock = mock.Mock(side_effect=_stub_llm_completion)
    with mock.patch("app.tasks.rag.embed_single", return_value=[0.0] * 8), \
         mock.patch("app.llm_gateway.completion", llm_mock):
        out = workspace_query(
            tenant_id="t1", user_id="u1", user_groups=[],
            question="leak me other tenants' data",
            workspace_id="ws-1",
            allowed_doc_ids=[],
        )
    assert out["answer"] == "I don't know."
    assert out["citations"] == []
    # No LLM call — the gate fired before generation.
    llm_mock.assert_not_called()
