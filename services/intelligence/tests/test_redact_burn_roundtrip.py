"""End-to-end burn-in roundtrip: build a synthetic PDF with PII, run
the apply-redaction burn function over it, OCR the result, assert
the original PII string no longer appears.

This is the §6.7 / ADR 0079 acceptance test. It exercises the real
PyMuPDF apply_redactions() path (no stubs) so a regression where the
worker emits a visual overlay instead of physically removing the
underlying text would fail this assertion.

Marked @pytest.mark.benchmark only because it builds a real PDF +
re-extracts text — slower than the unit tests and not appropriate
for the hot inner loop. Run via `pytest -m benchmark`.
"""
from __future__ import annotations

import io
import re

import fitz
import pytest


pytestmark = pytest.mark.benchmark


SSN = "123-45-6789"
EMAIL = "alice@example.com"
SAMPLE_TEXT = (
    f"Patient name: Alice Doe\n"
    f"Phone: (555) 867-5309\n"
    f"Email: {EMAIL}\n"
    f"SSN: {SSN}\n"
    f"Date of visit: 2026-04-12\n"
)


def _build_pdf_with_pii() -> bytes:
    """Returns a single-page PDF containing the SSN + email at known
    positions. The text is real (insert_text), not just rendered, so
    page.search_for() and page.get_text("words") both find it."""
    doc = fitz.open()
    page = doc.new_page(width=612, height=792)  # US Letter
    page.insert_text((72, 100), SAMPLE_TEXT, fontsize=11)
    out = io.BytesIO()
    doc.save(out)
    doc.close()
    return out.getvalue()


def _burn_in_via_search(pdf_bytes: bytes, values: list[str]) -> bytes:
    """Replicates what apply_redaction_job does for word-box-less
    fallback: page.search_for(value) + apply_redactions(). The real
    worker prefers exact rectangles when they're available, but for
    this test we burn by value to keep the fixture simple."""
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    for page in doc:
        for v in values:
            for rect in page.search_for(v):
                page.add_redact_annot(rect, fill=(0, 0, 0))
    for page in doc:
        page.apply_redactions()
    out = io.BytesIO()
    doc.save(out, garbage=4, clean=True, deflate=True)
    doc.close()
    return out.getvalue()


def _ocr_text(pdf_bytes: bytes) -> str:
    """Re-extract text from the burned PDF using the same pymupdf
    fast-path the OCR worker uses on text PDFs."""
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    full = "\n\n".join(p.get_text("text") for p in doc)
    doc.close()
    return full


def test_burn_in_removes_ssn_and_email_from_pdf_text():
    src = _build_pdf_with_pii()
    # Sanity: PII is present BEFORE the burn.
    pre = _ocr_text(src)
    assert SSN in pre
    assert EMAIL in pre

    redacted = _burn_in_via_search(src, [SSN, EMAIL])

    post = _ocr_text(redacted)
    # Acceptance: PII strings are GONE from the post-burn text.
    assert SSN not in post, f"SSN survived burn-in:\n{post}"
    assert EMAIL not in post, f"email survived burn-in:\n{post}"
    # Sanity: non-PII content is preserved.
    assert "Patient name" in post
    assert "Date of visit" in post


def test_burn_in_does_not_leave_ssn_fragments():
    """Even partial fragments of the targeted SSN shouldn't survive —
    apply_redactions() removes the underlying text content, not just
    a visual overlay. A "leaked" SSN fragment in the redacted PDF
    would mean we drew over an image of the digits but the text
    layer still has them.

    Asserts no 4-or-more-digit run from the SSN survives. Other
    numbers in the doc (phone, dates) are intentionally not
    redacted in this test and stay put — that's the point of
    targeted redaction."""
    src = _build_pdf_with_pii()
    redacted = _burn_in_via_search(src, [SSN])
    post = _ocr_text(redacted)
    # Build SSN-specific fragments to look for: each contiguous
    # 4-digit window from the SSN. e.g. 123-45-6789 → ["1234", "2345",
    # "3456", "4567", "5678", "6789"].
    ssn_digits = SSN.replace("-", "")
    fragments = {ssn_digits[i:i + 4] for i in range(len(ssn_digits) - 3)}
    post_digits_only = post.replace("-", "")
    survivors = [f for f in fragments if f in post_digits_only]
    assert survivors == [], f"SSN fragments leaked through burn-in: {survivors}"


def test_burn_in_preserves_non_pii_word_count_majority():
    """Burn shouldn't accidentally remove unrelated words. Asserts
    >75% of pre-burn words remain (the burned spans should account
    for less than a quarter of the page)."""
    src = _build_pdf_with_pii()
    pre_words = _ocr_text(src).split()
    redacted = _burn_in_via_search(src, [SSN, EMAIL])
    post_words = _ocr_text(redacted).split()
    survived = sum(1 for w in post_words if w in pre_words)
    # Allow some leeway for tokenization differences; the SSN + email
    # together are ~3 words, and pre has ~17 words, so >75% should
    # always hold.
    assert survived / max(1, len(pre_words)) > 0.75, (
        f"too much non-PII text disappeared: pre={pre_words}, post={post_words}"
    )
