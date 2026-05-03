"""Smart-route — pure logic + orchestration tests."""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.smart_route import (
    DEFAULT_CONFIG,
    RULE_CONFIDENCE_MULTIPLIER,
    _dedupe,
    _from_history,
    _from_rules,
    _mean_vector,
    _run_async,
)


# ---- pure ----------------------------------------------------------------

def test_mean_vector():
    assert _mean_vector([[1.0, 0.0], [0.0, 1.0]]) == [0.5, 0.5]


def test_dedupe_keeps_highest_confidence_per_folder():
    out = _dedupe([
        {"suggested_folder_id": "f1", "match_source": "rule", "confidence": 0.7},
        {"suggested_folder_id": "f1", "match_source": "history", "confidence": 0.9},
        {"suggested_folder_id": "f2", "match_source": "similarity", "confidence": 0.5},
    ])
    by_folder = {c["suggested_folder_id"]: c for c in out}
    assert by_folder["f1"]["confidence"] == 0.9
    assert by_folder["f1"]["match_source"] == "history"
    assert by_folder["f2"]["confidence"] == 0.5


# ---- strategy: rules -----------------------------------------------------

@pytest.mark.asyncio
async def test_from_rules_uses_classification_confidence_with_multiplier():
    fake_pool = _PoolStub(rows=[
        {"id": "r1", "name": "Invoices → Finance", "target_folder_id": "f-fin",
         "target_workspace_id": None, "priority": 10},
    ])
    with mock.patch("app.tasks.smart_route.get_pool",
                    new=mock.AsyncMock(return_value=fake_pool)):
        out = await _from_rules("t1", {"category_key": "invoice", "confidence": 0.9})
    assert len(out) == 1
    expected = round(0.9 * RULE_CONFIDENCE_MULTIPLIER, 6)
    assert round(out[0]["confidence"], 6) == expected
    assert out[0]["match_source"] == "rule"
    assert out[0]["suggested_folder_id"] == "f-fin"


# ---- strategy: history ---------------------------------------------------

@pytest.mark.asyncio
async def test_from_history_share_of_recent():
    fake_pool = _PoolStub(rows=[
        {"filed_folder_id": "f-a", "filed_workspace_id": None, "freq": 8},
        {"filed_folder_id": "f-b", "filed_workspace_id": None, "freq": 2},
    ])
    with mock.patch("app.tasks.smart_route.get_pool",
                    new=mock.AsyncMock(return_value=fake_pool)):
        out = await _from_history("t1", {"category_key": "invoice", "confidence": 1.0})
    by_folder = {c["suggested_folder_id"]: c for c in out}
    assert by_folder["f-a"]["confidence"] == pytest.approx(0.8)
    assert by_folder["f-b"]["confidence"] == pytest.approx(0.2)


@pytest.mark.asyncio
async def test_from_history_empty_returns_empty():
    fake_pool = _PoolStub(rows=[])
    with mock.patch("app.tasks.smart_route.get_pool",
                    new=mock.AsyncMock(return_value=fake_pool)):
        out = await _from_history("t1", {"category_key": "rare", "confidence": 0.5})
    assert out == []


# ---- orchestration -------------------------------------------------------

@pytest.mark.asyncio
async def test_disabled_short_circuits():
    cfg = {**DEFAULT_CONFIG, "enabled": False}
    with mock.patch("app.tasks.smart_route.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.smart_route.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.smart_route.mark_completed",
                    new=mock.AsyncMock()) as mc, \
         mock.patch("app.tasks.smart_route._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.smart_route._fetch_primary_classification",
                    new=mock.AsyncMock()) as fc:
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "disabled"
    fc.assert_not_awaited()
    mc.assert_awaited_once()


@pytest.mark.asyncio
async def test_no_classification_short_circuits():
    with mock.patch("app.tasks.smart_route.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.smart_route.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.smart_route.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.smart_route._load_config",
                    new=mock.AsyncMock(return_value=DEFAULT_CONFIG)), \
         mock.patch("app.tasks.smart_route._fetch_primary_classification",
                    new=mock.AsyncMock(return_value=None)):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "skipped"


@pytest.mark.asyncio
async def test_duplicate_event_short_circuits():
    with mock.patch("app.tasks.smart_route.already_completed",
                    new=mock.AsyncMock(return_value=True)), \
         mock.patch("app.tasks.smart_route.mark_enqueued",
                    new=mock.AsyncMock()) as me:
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "duplicate"
    me.assert_not_awaited()


@pytest.mark.asyncio
async def test_merge_dedupe_picks_best_per_folder():
    """Both a rule and history point at the same folder; the merged
    output should emit one row carrying the higher-confidence source."""
    cfg = {**DEFAULT_CONFIG, "use_similarity": False}
    persisted: dict = {}

    async def fake_persist(*, candidates, **_):
        persisted["candidates"] = candidates

    with mock.patch("app.tasks.smart_route.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.smart_route.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.smart_route.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.smart_route._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.smart_route._fetch_primary_classification",
                    new=mock.AsyncMock(return_value={
                        "category_key": "invoice", "confidence": 0.8,
                        "method": "ml", "model_version": "v1",
                    })), \
         mock.patch("app.tasks.smart_route._from_rules",
                    new=mock.AsyncMock(return_value=[{
                        "suggested_folder_id": "f-fin",
                        "suggested_workspace_id": None,
                        "match_source": "rule",
                        "match_detail": {},
                        "confidence": 0.5,
                    }])), \
         mock.patch("app.tasks.smart_route._from_history",
                    new=mock.AsyncMock(return_value=[{
                        "suggested_folder_id": "f-fin",
                        "suggested_workspace_id": None,
                        "match_source": "history",
                        "match_detail": {},
                        "confidence": 0.85,
                    }])), \
         mock.patch("app.tasks.smart_route._resolve_folder_paths",
                    new=mock.AsyncMock()), \
         mock.patch("app.tasks.smart_route._persist", new=fake_persist):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )

    assert out["suggestion_count"] == 1
    cands = persisted["candidates"]
    assert len(cands) == 1
    assert cands[0]["suggested_folder_id"] == "f-fin"
    assert cands[0]["match_source"] == "history"
    assert cands[0]["confidence"] == pytest.approx(0.85)


# ---- helpers -------------------------------------------------------------

class _PoolStub:
    """Stand-in for asyncpg.Pool that returns canned rows."""

    def __init__(self, rows):
        self._rows = rows

    def acquire(self):
        return _AcqStub(self._rows)


class _AcqStub:
    def __init__(self, rows):
        self._rows = rows

    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc):
        return False

    def transaction(self):
        return _TxStub()

    async def execute(self, *_a, **_k):
        return None

    async def fetch(self, *_a, **_k):
        return self._rows

    async def fetchrow(self, *_a, **_k):
        return self._rows[0] if self._rows else None


class _TxStub:
    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc):
        return False
