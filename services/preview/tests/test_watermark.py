"""Watermark rendering tests (§5). Verifies the DoD-critical property that two
different viewers produce different stamped bytes (per-viewer identity), plus
that image + PDF stamping run and preserve structure."""
from __future__ import annotations

import io

import fitz  # PyMuPDF
from PIL import Image

from app.watermark import WatermarkSpec, render_overlay, stamp_image_bytes, stamp_pdf_bytes


def _blank_png(w: int = 400, h: int = 300, color=(255, 255, 255)) -> bytes:
    buf = io.BytesIO()
    Image.new("RGB", (w, h), color).save(buf, format="PNG")
    return buf.getvalue()


def _blank_pdf(pages: int = 2) -> bytes:
    doc = fitz.open()
    for _ in range(pages):
        doc.new_page(width=595, height=842)  # A4 points
    out = doc.tobytes()
    doc.close()
    return out


def test_overlay_has_ink_when_text_present():
    ov = render_overlay(400, 300, WatermarkSpec(text="alice@acme.com", opacity=50))
    assert ov.mode == "RGBA"
    # some pixels must be non-transparent (alpha > 0)
    alpha = ov.getchannel("A")
    assert alpha.getextrema()[1] > 0


def test_overlay_empty_text_is_fully_transparent():
    ov = render_overlay(400, 300, WatermarkSpec(text=""))
    assert ov.getchannel("A").getextrema() == (0, 0)


def test_overlay_zero_opacity_is_transparent():
    ov = render_overlay(400, 300, WatermarkSpec(text="x@y.com", opacity=0))
    assert ov.getchannel("A").getextrema() == (0, 0)


def test_stamp_image_changes_bytes_and_stays_png():
    base = _blank_png()
    out = stamp_image_bytes(base, WatermarkSpec(text="alice@acme.com", opacity=40))
    assert out != base
    img = Image.open(io.BytesIO(out))
    assert img.format == "PNG"
    assert img.size == (400, 300)


def test_two_viewers_get_different_stamped_pages():
    # DoD: two users viewing the same page see their own identity.
    base = _blank_png()
    a = stamp_image_bytes(base, WatermarkSpec(text="alice@acme.com · 2026-07-01 · 203.0.113.7", opacity=40))
    b = stamp_image_bytes(base, WatermarkSpec(text="bob@acme.com · 2026-07-01 · 198.51.100.9", opacity=40))
    assert a != b


def test_stamp_pdf_preserves_page_count_and_changes_bytes():
    src = _blank_pdf(pages=3)
    out = stamp_pdf_bytes(src, WatermarkSpec(text="alice@acme.com", opacity=30))
    assert out != src
    doc = fitz.open(stream=out, filetype="pdf")
    try:
        assert doc.page_count == 3
    finally:
        doc.close()


def test_two_viewers_get_different_stamped_pdfs():
    src = _blank_pdf(pages=1)
    a = stamp_pdf_bytes(src, WatermarkSpec(text="alice@acme.com"))
    b = stamp_pdf_bytes(src, WatermarkSpec(text="bob@acme.com"))
    assert a != b
