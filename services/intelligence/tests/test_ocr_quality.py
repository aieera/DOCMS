"""OCR quality — pure scorers + summary + orchestration."""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.ocr_quality import (
    DEFAULT_CONFIG,
    QUALITY_WEIGHTS,
    _build_summary,
    _grade_for,
    _language_score,
    _line_regularity_score,
    _noise_ratio_score,
    _run_async,
    _score_page,
    _word_density_score,
)


# ---- pure sub-scorers ---------------------------------------------------

def test_word_density_blank_is_zero():
    assert _word_density_score(0) == 0.0


def test_word_density_caps_at_one():
    assert _word_density_score(50) == 0.5
    assert _word_density_score(100) == 1.0
    assert _word_density_score(500) == 1.0


def test_noise_ratio_clean_text_high():
    """Mostly alphanumeric → high score."""
    assert _noise_ratio_score("The quick brown fox") > 0.9


def test_noise_ratio_garbled_text_low():
    """Mostly symbols → low score."""
    assert _noise_ratio_score("!@#$%^&*()_+`~[]{}|") == 0.0


def test_language_score_real_english():
    s = _language_score("The quick brown fox jumps over the lazy dog.")
    assert s > 0.9


def test_language_score_garbage():
    """Random short tokens like '&^x' should drop the score."""
    s = _language_score("a 1 ! ? . , ; ' x y")
    assert s < 0.5


def test_language_score_blank_is_zero():
    assert _language_score("") == 0.0
    assert _language_score("   ") == 0.0


def test_language_score_arabic_treated_as_words():
    """Language-agnostic per ADR 0057 — Arabic tokens must score
    high, not low."""
    arabic = "مرحبا كيف حالك اليوم نحن نعمل بجد على هذا المشروع"
    s = _language_score(arabic)
    assert s > 0.7, f"Arabic should score high, got {s}"


# ---- line regularity ----------------------------------------------------

def test_line_regularity_returns_none_when_too_few_boxes():
    assert _line_regularity_score([]) is None
    assert _line_regularity_score([{"bbox": [0, 0, 100, 20]}]) is None


def test_line_regularity_uniform_heights_high_score():
    boxes = [{"bbox": [0, i * 25, 100, i * 25 + 20]} for i in range(10)]
    s = _line_regularity_score(boxes)
    assert s is not None and s > 0.95


def test_line_regularity_chaotic_heights_low_score():
    """Heights all over the place → low regularity."""
    heights = [10, 50, 8, 60, 12, 80, 5, 75, 9, 70]
    boxes = [{"bbox": [0, 0, 100, h]} for h in heights]
    s = _line_regularity_score(boxes)
    assert s is not None and s < 0.5


# ---- per-page composite -------------------------------------------------

def test_score_page_clean_high_quality():
    page = {
        "page_number": 1,
        "text_content": ("The quick brown fox jumps over the lazy dog. " * 25),
        "confidence": 0.95,
        "bounding_boxes": [{"bbox": [0, i * 25, 100, i * 25 + 20]} for i in range(15)],
    }
    s = _score_page(page, DEFAULT_CONFIG)
    assert s["overall_score"] > 0.85
    assert "low_confidence" not in s["issues"]
    assert "sparse_text" not in s["issues"]
    assert s["needs_review"] is False
    assert s["page_number"] == 1


def test_score_page_blank():
    page = {"page_number": 1, "text_content": "", "confidence": 0.0,
            "bounding_boxes": []}
    s = _score_page(page, DEFAULT_CONFIG)
    assert s["overall_score"] < 0.3
    assert "sparse_text" in s["issues"]
    assert s["needs_review"] is True


def test_score_page_garbled():
    page = {
        "page_number": 1,
        "text_content": "&^%$ @!# *( )_+ {}|: <>?? ~`'\\ //--",
        "confidence": 0.85,  # engine is confident but the content is junk
        "bounding_boxes": [],
    }
    s = _score_page(page, DEFAULT_CONFIG)
    assert s["overall_score"] < 0.6
    assert "noisy" in s["issues"] or "garbled" in s["issues"]


def test_score_page_missing_confidence_renormalises():
    """ADR 0057: when char_confidence is None, weights re-normalise
    across the remaining four sub-scores. A clean page must still
    score high even without engine confidence."""
    page = {
        "page_number": 1,
        "text_content": ("The quick brown fox jumps over the lazy dog. " * 25),
        "confidence": 0.0,  # treated as missing
        "bounding_boxes": [{"bbox": [0, i * 25, 100, i * 25 + 20]} for i in range(15)],
    }
    s = _score_page(page, DEFAULT_CONFIG)
    assert s["char_confidence"] is None
    assert s["overall_score"] > 0.85


# ---- summary + grading --------------------------------------------------

def test_grade_thresholds():
    cfg = DEFAULT_CONFIG
    assert _grade_for(0.95, cfg) == "excellent"
    assert _grade_for(0.80, cfg) == "good"
    assert _grade_for(0.65, cfg) == "fair"
    assert _grade_for(0.30, cfg) == "poor"


def test_build_summary_aggregates_correctly():
    scored = [
        {"overall_score": 0.95, "needs_review": False},
        {"overall_score": 0.50, "needs_review": True},
        {"overall_score": 0.85, "needs_review": False},
    ]
    s = _build_summary(scored, DEFAULT_CONFIG)
    assert s["total_pages"] == 3
    assert s["pages_needing_review"] == 1
    assert s["min_score"] == pytest.approx(0.50)
    assert s["max_score"] == pytest.approx(0.95)
    # _build_summary rounds avg_score to 4dp before returning.
    assert s["avg_score"] == pytest.approx(round((0.95 + 0.50 + 0.85) / 3, 4))
    assert s["quality_grade"] == "good"  # 0.766 between good and excellent


# ---- orchestration ------------------------------------------------------

@pytest.mark.asyncio
async def test_disabled_short_circuits():
    cfg = {**DEFAULT_CONFIG, "enabled": False}
    with mock.patch("app.tasks.ocr_quality.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.ocr_quality.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality.mark_completed",
                    new=mock.AsyncMock()) as mc, \
         mock.patch("app.tasks.ocr_quality._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.ocr_quality._fetch_ocr_pages",
                    new=mock.AsyncMock()) as fp, \
         mock.patch("app.tasks.ocr_quality._persist", new=mock.AsyncMock()) as p:
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "disabled"
    fp.assert_not_awaited()
    p.assert_not_awaited()
    mc.assert_awaited_once()


@pytest.mark.asyncio
async def test_no_ocr_pages_short_circuits():
    with mock.patch("app.tasks.ocr_quality.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.ocr_quality.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality._load_config",
                    new=mock.AsyncMock(return_value=DEFAULT_CONFIG)), \
         mock.patch("app.tasks.ocr_quality._fetch_ocr_pages",
                    new=mock.AsyncMock(return_value=[])):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "skipped"


@pytest.mark.asyncio
async def test_auto_retry_emitted_on_low_tesseract_score():
    """Below auto_retry_below + Tesseract engine + not previously
    retried → emits dms.version.ocr_retry_requested.v1."""
    cfg = {**DEFAULT_CONFIG, "auto_retry_below": 0.50}
    pages = [{"page_number": 1, "text_content": "", "confidence": 0.1,
              "bounding_boxes": [], "engine": "tesseract"}]

    persisted: dict = {}
    retry_calls: list = []
    set_calls: list = []

    async def fake_persist(**kwargs):
        persisted.update(kwargs)

    async def fake_emit(**kwargs):
        retry_calls.append(kwargs)

    async def fake_set(t, d):
        set_calls.append((t, d))

    with mock.patch("app.tasks.ocr_quality.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.ocr_quality.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.ocr_quality._fetch_ocr_pages",
                    new=mock.AsyncMock(return_value=pages)), \
         mock.patch("app.tasks.ocr_quality._prior_auto_retried",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.ocr_quality._persist", new=fake_persist), \
         mock.patch("app.tasks.ocr_quality._emit_retry", new=fake_emit), \
         mock.patch("app.tasks.ocr_quality._set_auto_retried", new=fake_set):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["auto_retry_emitted"] is True
    assert len(retry_calls) == 1
    assert len(set_calls) == 1


@pytest.mark.asyncio
async def test_auto_retry_suppressed_when_already_retried():
    cfg = {**DEFAULT_CONFIG, "auto_retry_below": 0.50}
    pages = [{"page_number": 1, "text_content": "", "confidence": 0.1,
              "bounding_boxes": [], "engine": "tesseract"}]

    retry_calls: list = []

    async def fake_emit(**kwargs):
        retry_calls.append(kwargs)

    with mock.patch("app.tasks.ocr_quality.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.ocr_quality.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.ocr_quality._fetch_ocr_pages",
                    new=mock.AsyncMock(return_value=pages)), \
         mock.patch("app.tasks.ocr_quality._prior_auto_retried",
                    new=mock.AsyncMock(return_value=True)), \
         mock.patch("app.tasks.ocr_quality._persist", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality._emit_retry", new=fake_emit):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["auto_retry_emitted"] is False
    assert retry_calls == []


@pytest.mark.asyncio
async def test_auto_retry_suppressed_when_engine_is_already_surya():
    """Surya is the best engine — no fallback target."""
    cfg = {**DEFAULT_CONFIG, "auto_retry_below": 0.50}
    pages = [{"page_number": 1, "text_content": "", "confidence": 0.1,
              "bounding_boxes": [], "engine": "surya"}]

    retry_calls: list = []

    async def fake_emit(**kwargs):
        retry_calls.append(kwargs)

    with mock.patch("app.tasks.ocr_quality.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.ocr_quality.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.ocr_quality._fetch_ocr_pages",
                    new=mock.AsyncMock(return_value=pages)), \
         mock.patch("app.tasks.ocr_quality._prior_auto_retried",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.ocr_quality._persist", new=mock.AsyncMock()), \
         mock.patch("app.tasks.ocr_quality._emit_retry", new=fake_emit):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["auto_retry_emitted"] is False


@pytest.mark.asyncio
async def test_duplicate_event_short_circuits():
    with mock.patch("app.tasks.ocr_quality.already_completed",
                    new=mock.AsyncMock(return_value=True)), \
         mock.patch("app.tasks.ocr_quality.mark_enqueued",
                    new=mock.AsyncMock()) as me:
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "duplicate"
    me.assert_not_awaited()


def test_quality_weights_sum_to_one():
    """Sanity: composite math assumes weights sum to 1 (or close)
    when all sub-scores are present."""
    assert abs(sum(QUALITY_WEIGHTS.values()) - 1.0) < 1e-6
