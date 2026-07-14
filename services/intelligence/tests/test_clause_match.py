"""detect_clauses — pure helpers + orchestration (mocked I/O)."""
from unittest.mock import MagicMock

from app.tasks.clause_match import (
    _body_sha,
    _normalize_text,
    _matches_for_clauses,
)


def test_body_sha_stable_and_content_sensitive():
    a = _body_sha("Governing law shall be England.")
    assert a == _body_sha("Governing law shall be England.")
    assert a != _body_sha("Governing law shall be Wales.")


def test_normalize_collapses_case_and_whitespace():
    assert _normalize_text("  Force\n\nMAJEURE   event ") == "force majeure event"
    assert _normalize_text("\n\tForce  Majeure\t\n") == "force majeure"


def _mk_hit(score, chunk_index=3, text="matched snippet"):
    h = MagicMock()
    h.score = score
    h.payload = {"chunk_index": chunk_index, "text_snippet": text}
    return h


def test_matches_respect_threshold_boundary():
    client = MagicMock()
    # clause A scores above, clause B exactly at threshold, C below.
    client.search.side_effect = [[_mk_hit(0.91)], [_mk_hit(0.80)], [_mk_hit(0.79)]]
    clauses = [
        {"id": "a", "vector": [0.1]},
        {"id": "b", "vector": [0.2]},
        {"id": "c", "vector": [0.3]},
    ]
    out = _matches_for_clauses(
        client, tenant_id="t1", document_id="d1", version_id="v1",
        clauses=clauses, threshold=0.80,
    )
    assert [m["clause_id"] for m in out] == ["a", "b"]
    assert out[0]["similarity"] == 0.91
    assert out[0]["chunk_index"] == 3
    assert out[0]["matched_text"] == "matched snippet"


def test_no_hits_yield_no_matches():
    client = MagicMock()
    client.search.return_value = []
    out = _matches_for_clauses(
        client, tenant_id="t1", document_id="d1", version_id="v1",
        clauses=[{"id": "a", "vector": [0.1]}], threshold=0.80,
    )
    assert out == []


def test_matched_text_falls_back_to_text_payload_key():
    client = MagicMock()
    h = MagicMock()
    h.score = 0.9
    h.payload = {"chunk_index": 1, "text": "real payload key"}
    client.search.return_value = [h]
    out = _matches_for_clauses(
        client, tenant_id="t1", document_id="d1", version_id="v1",
        clauses=[{"id": "a", "vector": [0.1]}], threshold=0.80,
    )
    assert out[0]["matched_text"] == "real payload key"
