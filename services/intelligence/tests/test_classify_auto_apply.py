"""BUG-30 — a confident classification must land on the document.

Before this, `classify_document` wrote `document_classifications` and
stopped. Nothing ever propagated the result to `documents.document_class`,
so every document read "Unclassified" and grouped under "(uncategorized)"
no matter how confident the classifier was — the auto-apply step did not
exist at all rather than being set too high.

The human-in-the-loop half is deliberate and is pinned here too: below
`classify_auto_apply_threshold` the row stays a suggestion and the
document is left unclassified.
"""
from __future__ import annotations

from unittest import mock

import pytest

from app.config import settings
from app.tasks.classify import _is_untrained_label, _tier2_ml, classify_document


class _Recorder:
    """Captures the persist calls the task makes."""

    def __init__(self, applied: bool = True):
        self.upserts: list[dict] = []
        self.applies: list[dict] = []
        self._applied = applied

    async def upsert(self, **kw):
        self.upserts.append(kw)

    async def apply(self, **kw):
        self.applies.append(kw)
        return self._applied


def _run(rec: _Recorder, *, rules_conf: float, category: str = "invoice"):
    with mock.patch("app.tasks.classify.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.classify.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.classify.publish_cloudevent", new=mock.AsyncMock()), \
         mock.patch("app.tasks.classify._tier1_rules",
                    return_value=(category, rules_conf, [])), \
         mock.patch("app.tasks.classify._tier2_ml", return_value=None), \
         mock.patch("app.tasks.classify._tier3_llm",
                    side_effect=RuntimeError("no LLM in unit tests")), \
         mock.patch("app.tasks.classify.upsert_classification", new=rec.upsert), \
         mock.patch("app.tasks.classify.apply_document_class", new=rec.apply):
        return classify_document.apply(
            args=["t1", "doc-1", "ver-1", "Invoice Number: 1. Amount Due: $5."],
        ).get()


# ---- auto-apply gate ----------------------------------------------------

def test_high_confidence_writes_the_class_onto_the_document():
    rec = _Recorder()
    out = _run(rec, rules_conf=0.95)
    assert out["auto_applied"] is True
    assert len(rec.applies) == 1
    assert rec.applies[0]["category_key"] == "invoice"
    assert rec.applies[0]["document_id"] == "doc-1"


def test_low_confidence_stays_a_suggestion_and_leaves_the_doc_unclassified():
    rec = _Recorder()
    out = _run(rec, rules_conf=0.40)
    assert out["auto_applied"] is False
    assert rec.applies == [], "below-threshold results must not touch the document"
    assert rec.upserts, "…but the suggestion is still recorded for review"


def test_threshold_is_the_documented_one_and_is_reachable():
    assert settings.classify_auto_apply_threshold == pytest.approx(0.85)
    # A tier-1 rules hit alone can reach it (5 keyword hits => 0.5, LLM
    # tier routinely returns 0.9+), so the gate is not unreachable by
    # construction.
    assert settings.classify_auto_apply_threshold < 0.99


def test_apply_failure_does_not_fail_the_classification():
    rec = _Recorder()

    async def _boom(**_kw):
        raise RuntimeError("db down")

    with mock.patch("app.tasks.classify.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.classify.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.classify.publish_cloudevent", new=mock.AsyncMock()), \
         mock.patch("app.tasks.classify._tier1_rules", return_value=("invoice", 0.95, [])), \
         mock.patch("app.tasks.classify._tier2_ml", return_value=None), \
         mock.patch("app.tasks.classify.upsert_classification", new=rec.upsert), \
         mock.patch("app.tasks.classify.apply_document_class", new=_boom):
        out = classify_document.apply(args=["t1", "doc-1", "ver-1", "text"]).get()
    assert out["status"] == "completed"
    assert out["auto_applied"] is False


# ---- tier-2 is an untrained base checkpoint ----------------------------

def test_placeholder_labels_are_recognised():
    assert _is_untrained_label("LABEL_0")
    assert _is_untrained_label("label_12")
    assert not _is_untrained_label("invoice")
    assert not _is_untrained_label("")


def test_tier2_skipped_when_the_model_emits_placeholder_labels():
    """`distilbert-base-uncased` is a BASE checkpoint — its classification
    head is randomly initialised, so it returns LABEL_n at ~0.5. Adopting
    that both produced garbage categories and out-scored tier 1."""
    with mock.patch("app.models.classifier.classify", return_value=("LABEL_0", 0.62)):
        assert _tier2_ml("some text") is None


def test_tier2_used_when_a_real_label_comes_back():
    with mock.patch("app.models.classifier.classify", return_value=("invoice", 0.91)):
        out = _tier2_ml("some text")
    assert out is not None
    assert out[0] == "invoice"
    assert out[1] == pytest.approx(0.91)
