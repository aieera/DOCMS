"""Unit tests for Wave 5 Prompt 5.5: chunker, payload shape, and
cross-tenant isolation.

Integration-level correctness (actual Qdrant round-trip; search
service reject of a cross-tenant query) lives in the Wave 13.1
integration harness. These tests pin the contracts that keep cross-
tenant leaks from being introduced by a careless payload refactor.
"""
from __future__ import annotations

import uuid

import pytest

from app.chunker import build_payload, chunk_text, point_id


# ---- chunker ------------------------------------------------------------

class TestChunker:
    def test_empty_input_returns_no_chunks(self) -> None:
        assert chunk_text("") == []

    def test_small_text_single_chunk(self) -> None:
        text = "Hello world. This is a test."
        chunks = chunk_text(text)
        assert len(chunks) == 1
        assert chunks[0]["chunk_index"] == 0
        assert chunks[0]["start_char"] == 0
        assert chunks[0]["end_char"] == len(text)
        assert chunks[0]["token_count"] > 0

    def test_chunk_offsets_are_monotonic(self) -> None:
        text = (
            "First sentence here. " * 50
            + "Second cluster now. " * 50
            + "Third tail. " * 50
        )
        chunks = chunk_text(text, chunk_size_tokens=64, chunk_overlap_tokens=8)
        assert len(chunks) >= 2
        # Char offsets must start at or before previous chunk's end
        # (overlap is allowed) and never go backwards across different
        # chunks' starts.
        prev_start = -1
        for c in chunks:
            assert c["start_char"] >= 0
            assert c["end_char"] <= len(text)
            assert c["end_char"] > c["start_char"]
            assert c["start_char"] >= prev_start
            prev_start = c["start_char"]

    def test_chunk_indices_are_sequential(self) -> None:
        text = "Sentence. " * 200
        chunks = chunk_text(text, chunk_size_tokens=64, chunk_overlap_tokens=8)
        assert [c["chunk_index"] for c in chunks] == list(range(len(chunks)))

    def test_respects_token_budget(self) -> None:
        # Use long pseudo-sentences so the sentence packer fires the
        # flush path. "word " alone never triggers sentence splits.
        text = ("This is sentence number one. " * 20) + ("Another sentence here. " * 20)
        chunks = chunk_text(text, chunk_size_tokens=64, chunk_overlap_tokens=8)
        assert len(chunks) > 1
        # Budget-generous: token count in any chunk must stay within
        # 4× the budget even with overlap + final-sentence overshoot.
        assert all(c["token_count"] <= 64 * 4 for c in chunks)


# ---- payload shape ------------------------------------------------------

class TestPayloadShape:
    """Assert the spec-required fields exist. Enforces the cross-
    tenant isolation invariant: every point MUST carry tenant_id."""

    def test_contains_required_keys(self) -> None:
        p = build_payload(
            tenant_id="tenant-a",
            document_id="doc-1",
            version_id="ver-1",
            chunk_index=0,
            start_char=0,
            end_char=100,
            token_count=25,
            text_snippet="sample text",
            readable_by=None,
        )
        for key in (
            "tenant_id",
            "document_id",
            "version_id",
            "chunk_index",
            "start_char",
            "end_char",
            "token_count",
            "text",
            "readable_by",
        ):
            assert key in p, f"payload missing {key}"

    def test_empty_tenant_id_raises(self) -> None:
        # A false-y tenant id would silently let cross-tenant queries
        # leak. Fail loudly instead.
        with pytest.raises(ValueError):
            build_payload(
                tenant_id="",
                document_id="d",
                version_id="v",
                chunk_index=0,
                start_char=0,
                end_char=10,
                token_count=1,
                text_snippet="x",
                readable_by=None,
            )

    def test_snippet_is_truncated_but_body_is_not(self) -> None:
        """`text_snippet` is the render-only preview; `text` is what RAG
        feeds the model and MUST stay complete. Capping `text` at 500
        chars truncated every chunk to ~25% of its content and starved
        Doc Q&A prompts down to a few hundred input tokens (BUG-11)."""
        body = "x" * 10000
        p = build_payload(
            tenant_id="t",
            document_id="d",
            version_id="v",
            chunk_index=0,
            start_char=0,
            end_char=10000,
            token_count=10,
            text_snippet=body,
            readable_by=None,
        )
        assert len(p["text_snippet"]) <= 500
        assert p["text"] == body

    def test_readable_by_default(self) -> None:
        p = build_payload(
            tenant_id="t",
            document_id="d",
            version_id="v",
            chunk_index=0,
            start_char=0,
            end_char=10,
            token_count=1,
            text_snippet="x",
            readable_by=None,
        )
        assert p["readable_by"] == ["everyone"]


# ---- cross-tenant isolation (unit-level) --------------------------------

class TestCrossTenantIsolation:
    """Pins the invariant that point_id is tenant-scoped — two tenants
    emitting identical (document_id, version_id, chunk_index) produce
    DIFFERENT point ids, so one tenant's upsert cannot overwrite
    another's.

    Full integration: Wave 13.1 chaos suite — 2 tenants upload
    identical docs, each tenant's search returns only their own.
    """

    def test_same_coords_different_tenants_produce_different_ids(self) -> None:
        id_a = point_id("tenant-a", "doc-1", "ver-1", 0)
        id_b = point_id("tenant-b", "doc-1", "ver-1", 0)
        assert id_a != id_b

    def test_same_coords_same_tenant_produce_stable_id(self) -> None:
        """Re-running the task MUST produce the same point ids so the
        Qdrant upsert is deterministic (otherwise retries accumulate
        duplicate vectors)."""
        assert point_id("t", "d", "v", 0) == point_id("t", "d", "v", 0)

    def test_point_id_is_valid_uuid(self) -> None:
        uuid.UUID(point_id("t", "d", "v", 0))
