"""BUG-16 — a page's char confidence must not be poisoned to 0.0.

Surya's per-line score is `sum(scores)/count(scores != 0)`, which is NaN
for a detected region that decoded to no tokens (blank line, rule,
stamp). The page average was a plain `sum(confs)/len(confs)`, so ONE NaN
line made the whole page persist with confidence 0.0 — while its text and
composite quality score were fine. That is how the header chip read
"OCR: Good" next to a page row reading "0.0 % char conf".
"""
from __future__ import annotations

import math

from app.models.ocr_model import mean_confidence
from app.tasks.ocr import _merge_ocr_results
from app.tasks.ocr_quality import DEFAULT_CONFIG, _line_regularity_score, _score_page


# ---- mean_confidence ----------------------------------------------------

def test_mean_confidence_ignores_nan_lines():
    assert mean_confidence([0.99, float("nan"), 0.97]) == 0.98


def test_mean_confidence_ignores_infinities():
    assert mean_confidence([0.9, float("inf"), float("-inf")]) == 0.9


def test_mean_confidence_all_missing_is_zero():
    assert mean_confidence([float("nan")]) == 0.0
    assert mean_confidence([]) == 0.0


def test_mean_confidence_accepts_a_generator():
    assert mean_confidence(c for c in [1.0, 0.0]) == 0.5


def test_merge_ocr_results_survives_a_nan_box():
    merged = _merge_ocr_results(
        {"text": "a", "boxes": [
            {"x1": 0, "y1": 0, "x2": 10, "y2": 10, "text": "a", "confidence": 0.95},
            {"x1": 0, "y1": 20, "x2": 10, "y2": 30, "text": "", "confidence": float("nan")},
        ]},
        {"text": "", "boxes": []},
    )
    assert math.isfinite(merged["confidence"])
    assert merged["confidence"] == 0.95


# ---- ocr_quality: missing vs. genuinely-bad confidence ------------------

def _page(**over) -> dict:
    page = {
        "page_number": 1,
        "text_content": "The quick brown fox jumps over the lazy dog. " * 5,
        "confidence": 0.93,
        "bounding_boxes": [],
    }
    page.update(over)
    return page


def test_missing_confidence_is_null_not_zero():
    """ocr_results.confidence is NOT NULL DEFAULT 0.0, so 0.0 is the only
    way the row can say "the engine reported nothing". It must never be
    surfaced as a real 0.0 % reading."""
    scored = _score_page(_page(confidence=0.0), DEFAULT_CONFIG)
    assert scored["char_confidence"] is None
    assert "confidence_unavailable" in scored["issues"]
    assert "low_confidence" not in scored["issues"]


def test_non_finite_confidence_is_treated_as_missing():
    scored = _score_page(_page(confidence=float("nan")), DEFAULT_CONFIG)
    assert scored["char_confidence"] is None
    assert "confidence_unavailable" in scored["issues"]


def test_real_confidence_is_reported_and_not_flagged_unavailable():
    scored = _score_page(_page(confidence=0.93), DEFAULT_CONFIG)
    assert scored["char_confidence"] == 0.93
    assert "confidence_unavailable" not in scored["issues"]


def test_low_but_present_confidence_still_flags_low_confidence():
    scored = _score_page(_page(confidence=0.2), DEFAULT_CONFIG)
    assert scored["char_confidence"] == 0.2
    assert "low_confidence" in scored["issues"]
    assert "confidence_unavailable" not in scored["issues"]


# ---- line_regularity against the box shape actually persisted ----------

def test_line_regularity_reads_the_x1y1x2y2_shape_engines_emit():
    """app/models/ocr_model.py emits {x1,y1,x2,y2} and app/tasks/ocr.py
    persists exactly that into bounding_boxes. The scorer only understood
    `bbox`/`height`, so line_regularity was always None in production and
    15% of the composite weight silently vanished."""
    boxes = [
        {"x1": 0, "y1": 0, "x2": 100, "y2": 20, "text": "a"},
        {"x1": 0, "y1": 30, "x2": 100, "y2": 50, "text": "b"},
        {"x1": 0, "y1": 60, "x2": 100, "y2": 80, "text": "c"},
    ]
    assert _line_regularity_score(boxes) == 1.0


def test_line_regularity_still_reads_the_bbox_shape():
    boxes = [{"bbox": [0, 0, 100, 20]}, {"bbox": [0, 30, 100, 50]},
             {"bbox": [0, 60, 100, 80]}]
    assert _line_regularity_score(boxes) == 1.0


def test_score_page_uses_line_regularity_from_engine_boxes():
    scored = _score_page(
        _page(bounding_boxes=[
            {"x1": 0, "y1": 0, "x2": 100, "y2": 20},
            {"x1": 0, "y1": 30, "x2": 100, "y2": 50},
            {"x1": 0, "y1": 60, "x2": 100, "y2": 80},
        ]),
        DEFAULT_CONFIG,
    )
    assert scored["line_regularity"] is not None
