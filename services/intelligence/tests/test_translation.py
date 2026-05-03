"""Translation pipeline — pure logic + chunking + cost-guard tests."""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.lang_detect import _detect, MIN_CHARS_FOR_CONFIDENCE
from app.tasks.translate import (
    DEFAULT_AVAILABLE,
    DEFAULT_CONFIG,
    DEFAULT_MAX_CHARS,
    _run_async as translate_run,
    _split_chunks,
)


# ---- chunking -----------------------------------------------------------

def test_split_chunks_packs_paragraphs_under_limit():
    text = ("Paragraph one. " * 20).strip() + "\n\n" + ("Paragraph two. " * 20).strip()
    chunks = _split_chunks(text, max_chars=10000)
    assert len(chunks) == 1


def test_split_chunks_breaks_on_paragraph_boundary():
    p1 = "Para 1 content. " * 200          # ~3200 chars
    p2 = "Para 2 different content. " * 200  # ~5200 chars
    text = p1 + "\n\n" + p2
    chunks = _split_chunks(text, max_chars=4000)
    assert len(chunks) == 2
    assert "Para 1" in chunks[0]
    assert "Para 2" in chunks[1]
    assert "Para 1" not in chunks[1]


def test_split_chunks_keeps_oversize_paragraph_whole():
    """A single paragraph bigger than max_chars must NOT be split mid-text
    — translation quality wins over strict size limits per ADR 0056."""
    huge = "a" * 20_000
    chunks = _split_chunks(huge, max_chars=4000)
    assert len(chunks) == 1
    assert len(chunks[0]) == 20_000


def test_split_chunks_empty_returns_original():
    chunks = _split_chunks("", max_chars=100)
    assert chunks == [""]


# ---- lang detect --------------------------------------------------------

def test_detect_returns_iso_code_and_confidence():
    text = "The quick brown fox jumps over the lazy dog. " * 5
    primary, conf, secondary = _detect(text)
    assert primary == "en"
    assert 0.5 < conf <= 1.0
    assert isinstance(secondary, list)


def test_detect_low_confidence_capped_for_short_text():
    """Below MIN_CHARS_FOR_CONFIDENCE the confidence is clamped to ≤ 0.5
    so callers don't over-trust detection on tiny scraps."""
    short = "the cat"
    assert len(short) < MIN_CHARS_FOR_CONFIDENCE
    primary, conf, _ = _detect(short)
    assert conf <= 0.5
    # primary may still be 'en' but the gate is the confidence cap
    _ = primary


# ---- translate orchestration ---------------------------------------------

@pytest.mark.asyncio
async def test_translate_disabled_marks_failed():
    cfg = {**DEFAULT_CONFIG, "enabled": False}
    failures: list[tuple] = []

    async def fake_mark_failed(tenant, tid, error):
        failures.append((tenant, tid, error))

    with mock.patch("app.tasks.translate.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.translate.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.translate._mark_translation_failed", new=fake_mark_failed):
        out = await translate_run(
            tenant_id="t1", translation_id="tr1", document_id="d1", version_id="v1",
            target_language="ar", requested_by="u1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "disabled"
    assert failures and failures[0][1] == "tr1"


@pytest.mark.asyncio
async def test_translate_unavailable_language_rejected():
    cfg = {**DEFAULT_CONFIG, "available_languages": ["en", "fr"]}
    failures: list[tuple] = []

    async def fake_mark_failed(tenant, tid, error):
        failures.append((tenant, tid, error))

    with mock.patch("app.tasks.translate.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.translate.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.translate._mark_translation_failed", new=fake_mark_failed):
        out = await translate_run(
            tenant_id="t1", translation_id="tr1", document_id="d1", version_id="v1",
            target_language="zh", requested_by="u1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "rejected"
    assert "not in tenant" in failures[0][2]


@pytest.mark.asyncio
async def test_translate_oversize_text_rejected():
    """max_chars_per_doc cost guard rejects before any LLM call."""
    cfg = {**DEFAULT_CONFIG, "max_chars_per_doc": 100}
    failures: list[tuple] = []

    async def fake_mark_failed(tenant, tid, error):
        failures.append((tenant, tid, error))

    with mock.patch("app.tasks.translate.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.translate.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.translate._fetch_ocr_text",
                    new=mock.AsyncMock(return_value="x" * 500)), \
         mock.patch("app.tasks.translate._mark_translation_failed", new=fake_mark_failed):
        out = await translate_run(
            tenant_id="t1", translation_id="tr1", document_id="d1", version_id="v1",
            target_language="ar", requested_by="u1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "rejected"
    assert out["reason"] == "too long"
    assert "exceeds max_chars" in failures[0][2]


@pytest.mark.asyncio
async def test_translate_short_text_chunks_once_and_completes():
    cfg = {**DEFAULT_CONFIG}
    persisted: dict = {}

    async def fake_persist(**kwargs):
        persisted.update(kwargs)

    fake_completion = mock.MagicMock(return_value={
        "content": "translated", "model": "stub-llm",
        "input_tokens": 10, "output_tokens": 20, "cost_usd": 0.001,
    })
    with mock.patch("app.tasks.translate.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.translate.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.translate._fetch_ocr_text",
                    new=mock.AsyncMock(return_value="Hello world.")), \
         mock.patch("app.tasks.translate._fetch_source_language",
                    new=mock.AsyncMock(return_value="en")), \
         mock.patch("app.tasks.translate._set_translation_status", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate._persist_completion", new=fake_persist), \
         mock.patch("app.llm_gateway.completion", fake_completion):
        out = await translate_run(
            tenant_id="t1", translation_id="tr1", document_id="d1", version_id="v1",
            target_language="ar", requested_by="u1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "completed"
    assert out["chunks"] == 1
    assert persisted["target_language"] == "ar"
    assert persisted["source_language"] == "en"
    assert persisted["translated_text"] == "translated"
    fake_completion.assert_called_once()


@pytest.mark.asyncio
async def test_translate_long_text_calls_llm_per_chunk():
    cfg = {**DEFAULT_CONFIG}
    p1 = "Para 1 content. " * 1000
    p2 = "Para 2 content. " * 1000
    text = p1 + "\n\n" + p2  # ~32k chars, 2 chunks at 12k limit

    persisted: dict = {}

    async def fake_persist(**kwargs):
        persisted.update(kwargs)

    fake_completion = mock.MagicMock(return_value={
        "content": "TRANSLATED", "model": "stub-llm",
        "input_tokens": 100, "output_tokens": 200, "cost_usd": 0.01,
    })
    with mock.patch("app.tasks.translate.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.translate.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.translate._fetch_ocr_text",
                    new=mock.AsyncMock(return_value=text)), \
         mock.patch("app.tasks.translate._fetch_source_language",
                    new=mock.AsyncMock(return_value="en")), \
         mock.patch("app.tasks.translate._set_translation_status", new=mock.AsyncMock()), \
         mock.patch("app.tasks.translate._persist_completion", new=fake_persist), \
         mock.patch("app.llm_gateway.completion", fake_completion):
        out = await translate_run(
            tenant_id="t1", translation_id="tr1", document_id="d1", version_id="v1",
            target_language="ar", requested_by="u1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "completed"
    assert out["chunks"] >= 2
    assert fake_completion.call_count == out["chunks"]
    # Concatenated translation joined with \n\n between chunks.
    assert "TRANSLATED" in persisted["translated_text"]


@pytest.mark.asyncio
async def test_translate_duplicate_event_short_circuits():
    with mock.patch("app.tasks.translate.already_completed",
                    new=mock.AsyncMock(return_value=True)), \
         mock.patch("app.tasks.translate.mark_enqueued",
                    new=mock.AsyncMock()) as me:
        out = await translate_run(
            tenant_id="t1", translation_id="tr1", document_id="d1", version_id="v1",
            target_language="ar", requested_by="u1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "duplicate"
    me.assert_not_awaited()


def test_default_available_languages_includes_common_set():
    for code in ("en", "ar", "fr", "es", "de"):
        assert code in DEFAULT_AVAILABLE
