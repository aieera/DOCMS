"""Active-learning pipeline — pure logic + orchestration tests.
DB, MinIO, transformers/torch are all stubbed."""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.training_collector import (
    DEFAULT_CONFIG,
    _assign_split,
    _run_async as collector_run,
    _should_retrain,
)
from app.tasks.model_retrain import _bump_patch
from app.tasks.model_evaluate import _decide


# ---- training_collector pure helpers ------------------------------------

def test_assign_split_deterministic_by_correction_id():
    cfg = {**DEFAULT_CONFIG, "train_validation_split": 0.10, "train_test_split": 0.10}
    s1 = _assign_split("11111111-1111-1111-1111-111111111111", cfg)
    s2 = _assign_split("11111111-1111-1111-1111-111111111111", cfg)
    assert s1 == s2, "same correction_id must map to same split"


def test_assign_split_distribution_roughly_matches_ratios():
    cfg = {**DEFAULT_CONFIG, "train_validation_split": 0.10, "train_test_split": 0.10}
    counts = {"train": 0, "validation": 0, "test": 0}
    for i in range(1000):
        cid = f"{i:08x}-{i:04x}-{i:04x}-{i:04x}-{i:012x}"
        counts[_assign_split(cid, cfg)] += 1
    # 10/10/80 split — allow ±2.5% on each.
    assert 75 <= counts["validation"] <= 125, counts
    assert 75 <= counts["test"]       <= 125, counts
    assert 750 <= counts["train"]    <= 850, counts


def test_should_retrain_threshold():
    cfg = {**DEFAULT_CONFIG, "min_examples_for_retrain": 50, "retrain_increment": 25}
    # Below first floor: never retrain.
    assert not _should_retrain(0, cfg)
    assert not _should_retrain(49, cfg)
    # First trigger on hitting the floor exactly.
    assert _should_retrain(50, cfg)
    # In-between values don't retrigger.
    assert not _should_retrain(51, cfg)
    assert not _should_retrain(74, cfg)
    # Increment hits.
    assert _should_retrain(75, cfg)
    assert _should_retrain(100, cfg)
    assert not _should_retrain(99, cfg)


# ---- model_retrain pure helpers -----------------------------------------

def test_bump_patch_basic():
    assert _bump_patch("v1.0.0") == "v1.0.1"
    assert _bump_patch("v1.0.99") == "v1.0.100"
    assert _bump_patch("v2.5.13") == "v2.5.14"


def test_bump_patch_falls_back_when_unparseable():
    out = _bump_patch("garbage-v")
    assert out.startswith("v"), out


# ---- model_evaluate decision logic --------------------------------------

def test_decide_cold_start_accepts_above_half():
    cfg = {"auto_promote_if_better": False, "min_accuracy_improvement": 0.02}
    assert _decide({"accuracy": 0.75}, None, cfg) == "candidate"
    assert _decide({"accuracy": 0.49}, None, cfg) == "retired"
    assert _decide({"accuracy": 0.50}, None, cfg) == "candidate"


def test_decide_below_threshold_retires():
    cfg = {"auto_promote_if_better": False, "min_accuracy_improvement": 0.02}
    assert _decide({"accuracy": 0.91}, {"accuracy": 0.90}, cfg) == "retired"


def test_decide_above_threshold_becomes_candidate_by_default():
    cfg = {"auto_promote_if_better": False, "min_accuracy_improvement": 0.02}
    assert _decide({"accuracy": 0.93}, {"accuracy": 0.90}, cfg) == "candidate"


def test_decide_auto_promote_when_enabled_and_above_threshold():
    cfg = {"auto_promote_if_better": True, "min_accuracy_improvement": 0.02}
    assert _decide({"accuracy": 0.93}, {"accuracy": 0.90}, cfg) == "promote"
    # Even with auto-promote, below threshold still retires.
    assert _decide({"accuracy": 0.91}, {"accuracy": 0.90}, cfg) == "retired"


# ---- collector orchestration --------------------------------------------

@pytest.mark.asyncio
async def test_collector_disabled_short_circuits():
    cfg = {**DEFAULT_CONFIG, "enabled": False}
    with mock.patch("app.tasks.training_collector.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.training_collector.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector.mark_completed",
                    new=mock.AsyncMock()) as mc, \
         mock.patch("app.tasks.training_collector._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.training_collector._fetch_ocr_text",
                    new=mock.AsyncMock()) as fo, \
         mock.patch("app.tasks.training_collector._insert_example",
                    new=mock.AsyncMock()) as ie:
        out = await collector_run(
            tenant_id="t1", correction_id="c1",
            document_id="d1", version_id="v1",
            original_category="invoice", corrected_category="receipt",
            event_id="e1", correlation_id="cor",
            attempt=1, is_terminal=False,
        )
    assert out["status"] == "disabled"
    fo.assert_not_awaited()
    ie.assert_not_awaited()
    mc.assert_awaited_once()


@pytest.mark.asyncio
async def test_collector_no_text_short_circuits():
    cfg = {**DEFAULT_CONFIG, "enabled": True}
    with mock.patch("app.tasks.training_collector.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.training_collector.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.training_collector._fetch_ocr_text",
                    new=mock.AsyncMock(return_value="x")), \
         mock.patch("app.tasks.training_collector._insert_example",
                    new=mock.AsyncMock()) as ie:
        out = await collector_run(
            tenant_id="t1", correction_id="c1",
            document_id="d1", version_id="v1",
            original_category="", corrected_category="invoice",
            event_id="e1", correlation_id="cor",
            attempt=1, is_terminal=False,
        )
    assert out["status"] == "skipped"
    ie.assert_not_awaited()


@pytest.mark.asyncio
async def test_collector_dispatches_retrain_at_threshold():
    cfg = {**DEFAULT_CONFIG, "enabled": True,
           "min_examples_for_retrain": 50, "retrain_increment": 25}
    fake_retrain = mock.MagicMock()

    with mock.patch("app.tasks.training_collector.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.training_collector.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.training_collector._fetch_ocr_text",
                    new=mock.AsyncMock(return_value="x" * 100)), \
         mock.patch("app.tasks.training_collector._insert_example",
                    new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector._count_unused",
                    new=mock.AsyncMock(return_value=50)), \
         mock.patch("app.tasks.model_retrain.retrain", fake_retrain):
        out = await collector_run(
            tenant_id="t1", correction_id="c1",
            document_id="d1", version_id="v1",
            original_category="", corrected_category="invoice",
            event_id="e1", correlation_id="cor",
            attempt=1, is_terminal=False,
        )
    assert out["retrain_dispatched"] is True
    fake_retrain.apply_async.assert_called_once()


@pytest.mark.asyncio
async def test_collector_no_dispatch_below_threshold():
    cfg = {**DEFAULT_CONFIG, "enabled": True,
           "min_examples_for_retrain": 50, "retrain_increment": 25}
    fake_retrain = mock.MagicMock()
    with mock.patch("app.tasks.training_collector.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.training_collector.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.training_collector._fetch_ocr_text",
                    new=mock.AsyncMock(return_value="x" * 100)), \
         mock.patch("app.tasks.training_collector._insert_example",
                    new=mock.AsyncMock()), \
         mock.patch("app.tasks.training_collector._count_unused",
                    new=mock.AsyncMock(return_value=49)), \
         mock.patch("app.tasks.model_retrain.retrain", fake_retrain):
        out = await collector_run(
            tenant_id="t1", correction_id="c1",
            document_id="d1", version_id="v1",
            original_category="", corrected_category="invoice",
            event_id="e1", correlation_id="cor",
            attempt=1, is_terminal=False,
        )
    assert out["retrain_dispatched"] is False
    fake_retrain.apply_async.assert_not_called()
