"""Auto-tag — pure-logic + orchestration tests.

DB and NATS are stubbed; the integration test suite exercises the
real schema via testcontainers.
"""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.auto_tag import (
    DEFAULT_CONFIG,
    _dedupe,
    _run_async,
    _tag_from_entity,
    _tags_from_classification,
)


# ---- pure rules -----------------------------------------------------------

def test_tag_from_entity_org_lowercased():
    out = _tag_from_entity(
        {"entity_type": "ORG", "entity_value": "Raabyt", "confidence": 0.92},
        DEFAULT_CONFIG["source_weights"],
    )
    assert out["tag_name"] == "raabyt"
    assert out["source"] == "ner"
    assert out["confidence"] == pytest.approx(0.92)


def test_tag_from_entity_person_below_floor_dropped():
    out = _tag_from_entity(
        {"entity_type": "PERSON", "entity_value": "Jane Doe", "confidence": 0.7},
        DEFAULT_CONFIG["source_weights"],
    )
    assert out is None


def test_tag_from_entity_person_above_floor_prefixed():
    out = _tag_from_entity(
        {"entity_type": "PERSON", "entity_value": "Jane Doe", "confidence": 0.95},
        DEFAULT_CONFIG["source_weights"],
    )
    assert out["tag_name"] == "person:jane doe"


def test_tag_from_entity_skipped_types():
    for t in ("DATE", "MONEY", "CARDINAL", "ORDINAL", "PERCENT"):
        out = _tag_from_entity(
            {"entity_type": t, "entity_value": "anything", "confidence": 0.99},
            DEFAULT_CONFIG["source_weights"],
        )
        assert out is None, f"type {t} should be skipped"


def test_tag_from_entity_source_weight_applied():
    weights = {"ner": 0.5}
    out = _tag_from_entity(
        {"entity_type": "ORG", "entity_value": "Acme", "confidence": 0.8},
        weights,
    )
    assert out["confidence"] == pytest.approx(0.4)


def test_classification_emits_primary_plus_top3():
    cls = {
        "category_key": "invoice",
        "confidence": 0.95,
        "method": "ml",
        "model_version": "v1",
        "top3": [
            {"category": "invoice", "confidence": 0.95},  # dup of primary, dropped
            {"category": "receipt", "confidence": 0.30},
            {"category": "statement", "confidence": 0.10},
        ],
    }
    out = _tags_from_classification(cls, {"classification": 1.0})
    names = sorted(c["tag_name"] for c in out)
    assert names == ["invoice", "receipt", "statement"]


def test_dedupe_keeps_highest_confidence():
    candidates = [
        {"tag_name": "raabyt", "source": "ner", "confidence": 0.7,
         "source_detail": {}},
        {"tag_name": "raabyt", "source": "classification", "confidence": 0.9,
         "source_detail": {}},
    ]
    out = _dedupe(candidates)
    assert len(out) == 1
    assert out[0]["confidence"] == 0.9
    assert out[0]["source"] == "classification"


# ---- orchestration --------------------------------------------------------

@pytest.mark.asyncio
async def test_disabled_tenant_short_circuits():
    cfg = {**DEFAULT_CONFIG, "enabled": False}
    with mock.patch("app.tasks.auto_tag.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.auto_tag.mark_enqueued",
                    new=mock.AsyncMock(return_value=None)), \
         mock.patch("app.tasks.auto_tag.mark_completed",
                    new=mock.AsyncMock(return_value=None)) as mc, \
         mock.patch("app.tasks.auto_tag._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.auto_tag._fetch_classifications",
                    new=mock.AsyncMock(return_value=[])) as fc, \
         mock.patch("app.tasks.auto_tag._fetch_entities",
                    new=mock.AsyncMock(return_value=[])) as fe, \
         mock.patch("app.tasks.auto_tag._persist",
                    new=mock.AsyncMock(return_value=None)) as p:
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            source_event="classify", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "disabled"
    fc.assert_not_awaited()
    fe.assert_not_awaited()
    p.assert_not_awaited()
    mc.assert_awaited_once()


@pytest.mark.asyncio
async def test_duplicate_event_short_circuits():
    with mock.patch("app.tasks.auto_tag.already_completed",
                    new=mock.AsyncMock(return_value=True)), \
         mock.patch("app.tasks.auto_tag.mark_enqueued",
                    new=mock.AsyncMock()) as me, \
         mock.patch("app.tasks.auto_tag._load_config",
                    new=mock.AsyncMock()):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            source_event="ner", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "duplicate"
    me.assert_not_awaited()


@pytest.mark.asyncio
async def test_blocked_and_max_tags_filters_applied():
    cfg = {
        **DEFAULT_CONFIG,
        "blocked_tags": ["secret"],
        "max_tags_per_document": 2,
        "auto_apply_threshold": 0.99,  # nothing auto-applies
        "suggest_threshold": 0.10,
    }
    classifications = [
        {"category_key": "invoice", "confidence": 0.9, "method": "ml",
         "model_version": "", "top3": [
             {"category": "secret", "confidence": 0.85},
             {"category": "receipt", "confidence": 0.80},
             {"category": "statement", "confidence": 0.75},
         ]},
    ]
    persisted: dict = {}

    async def fake_persist(*, tenant_id, document_id, version_id,
                            auto_applied, suggestions, correlation_id):
        persisted["auto_applied"] = auto_applied
        persisted["suggestions"] = suggestions

    with mock.patch("app.tasks.auto_tag.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.auto_tag.mark_enqueued",
                    new=mock.AsyncMock()), \
         mock.patch("app.tasks.auto_tag.mark_completed",
                    new=mock.AsyncMock()), \
         mock.patch("app.tasks.auto_tag._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.auto_tag._fetch_classifications",
                    new=mock.AsyncMock(return_value=classifications)), \
         mock.patch("app.tasks.auto_tag._fetch_entities",
                    new=mock.AsyncMock(return_value=[])), \
         mock.patch("app.tasks.auto_tag._persist", new=fake_persist):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            source_event="classify", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )

    assert out["status"] == "completed"
    assert persisted["auto_applied"] == []
    names = [s["tag_name"] for s in persisted["suggestions"]]
    assert "secret" not in names, "blocked tag must be filtered"
    assert len(persisted["suggestions"]) == 2, "max_tags_per_document trim"


@pytest.mark.asyncio
async def test_auto_apply_split_at_threshold():
    cfg = {**DEFAULT_CONFIG, "auto_apply_threshold": 0.90,
           "suggest_threshold": 0.50}
    entities = [
        {"entity_type": "ORG", "entity_value": "BigCorp", "confidence": 0.95},
        {"entity_type": "ORG", "entity_value": "SmallCo", "confidence": 0.60},
    ]
    persisted: dict = {}

    async def fake_persist(*, auto_applied, suggestions, **_):
        persisted["auto_applied"] = [c["tag_name"] for c in auto_applied]
        persisted["suggestions"] = [c["tag_name"] for c in suggestions]

    with mock.patch("app.tasks.auto_tag.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.auto_tag.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.auto_tag.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.auto_tag._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.auto_tag._fetch_classifications",
                    new=mock.AsyncMock(return_value=[])), \
         mock.patch("app.tasks.auto_tag._fetch_entities",
                    new=mock.AsyncMock(return_value=entities)), \
         mock.patch("app.tasks.auto_tag._persist", new=fake_persist):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            source_event="ner", event_id="e1",
            correlation_id="c1", attempt=1, is_terminal=False,
        )

    assert out["auto_applied_count"] == 1
    assert persisted["auto_applied"] == ["bigcorp"]
    assert persisted["suggestions"] == ["smallco"]
