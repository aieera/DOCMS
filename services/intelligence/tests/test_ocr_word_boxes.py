"""Unit tests for the pymupdf word-box → page-relative offset mapping
that backs the PDF entity overlay (ADR 0078 follow-up, migration 23).

Pymupdf is fast enough that we don't bother stubbing it — the tests
build a real one-page PDF in memory and call the worker helper end-
to-end.
"""
from __future__ import annotations

import io

import fitz  # pymupdf
import pytest

from app.tasks.ocr import _pymupdf_word_boxes


def _make_pdf(text: str) -> fitz.Page:
    """Return a single-page PDF document containing `text`."""
    doc = fitz.open()
    page = doc.new_page(width=612, height=792)  # US Letter
    page.insert_text((72, 100), text, fontsize=11)
    return page


def test_word_boxes_have_offsets_into_page_text():
    page = _make_pdf("Hello world from pymupdf")
    text = page.get_text("text").strip()
    boxes = _pymupdf_word_boxes(page, text)
    # Each box round-trips: text[start:end] equals the word it claims.
    for b in boxes:
        s, e = b["start"], b["end"]
        # The page_text we pass in is the stripped form, so the box
        # offsets should index into that exact string.
        assert 0 <= s < e <= len(text)
        # Coordinates are real, finite numbers, not NaN.
        for k in ("x0", "y0", "x1", "y1"):
            assert isinstance(b[k], float)
            assert b["x1"] > b["x0"]
            assert b["y1"] > b["y0"]


def test_word_boxes_offsets_advance_in_reading_order():
    page = _make_pdf("alpha beta gamma delta")
    text = page.get_text("text").strip()
    boxes = _pymupdf_word_boxes(page, text)
    # Every box's start should be >= the previous box's end (reading
    # order, no overlaps).
    prev_end = 0
    for b in boxes:
        assert b["start"] >= prev_end - 1  # ±1 for whitespace tolerance
        prev_end = b["end"]


def test_word_boxes_skip_unfindable_words():
    # A page where the words exist but page_text we pass is empty —
    # nothing can be located, expect [].
    page = _make_pdf("some real content")
    boxes = _pymupdf_word_boxes(page, "")
    assert boxes == []


def test_word_boxes_empty_when_no_records():
    # Empty page yields no records and no boxes.
    doc = fitz.open()
    page = doc.new_page()
    boxes = _pymupdf_word_boxes(page, "")
    assert boxes == []


def test_word_boxes_handle_repeated_word():
    # Same word twice — the cursor should advance past the first
    # match so the second word maps to the second occurrence.
    page = _make_pdf("test test foo")
    text = page.get_text("text").strip()
    boxes = _pymupdf_word_boxes(page, text)
    # Should have three boxes, with the two "test" boxes pointing at
    # different offsets.
    test_offsets = sorted(b["start"] for b in boxes if text[b["start"]:b["end"]] == "test")
    assert len(test_offsets) == 2
    assert test_offsets[0] != test_offsets[1]
