"""Regression test for the rerank sort crash (Ask page 500).

sorted(zip(scores, chunks)) compares the chunk DICTS whenever two
scores tie — TypeError: '<' not supported between 'dict' and 'dict'.
Tie scores are the NORM when the reranker falls back to uniform
scoring, so /intelligence/ask crashed on real questions. All rank-by-
score call sites must go through _rank_by_score (key=score only).
"""
from app.tasks.rag import _rank_by_score


def test_tie_scores_with_dict_chunks_do_not_crash():
    chunks = [
        {"document_id": "d1", "chunk_index": 0, "text": "alpha"},
        {"document_id": "d2", "chunk_index": 1, "text": "beta"},
        {"document_id": "d3", "chunk_index": 2, "text": "gamma"},
    ]
    scores = [0.5, 0.5, 0.5]  # uniform fallback scores — the crash case
    out = _rank_by_score(scores, chunks, top_n=2)
    assert len(out) == 2
    assert all(isinstance(c, dict) for c in out)


def test_orders_by_score_desc_and_truncates():
    chunks = [{"text": "low"}, {"text": "high"}, {"text": "mid"}]
    scores = [0.1, 0.9, 0.5]
    out = _rank_by_score(scores, chunks, top_n=2)
    assert [c["text"] for c in out] == ["high", "mid"]


def test_empty_is_safe():
    assert _rank_by_score([], [], top_n=5) == []
