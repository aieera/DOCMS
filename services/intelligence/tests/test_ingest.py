"""WS3 pre-commit ingestion — external-key selection.

Guarded with importorskip because app.tasks.ingest pulls the OCR engine
(fitz/PIL/surya); in a minimal env without those the test skips rather than
erroring, same as the other OCR-touching suites.
"""
import asyncio

import pytest

ingest = pytest.importorskip("app.tasks.ingest")


def test_best_doc_number_picks_highest_confidence_doc_field():
    # Only is_doc_number fields are eligible; among them the highest confidence
    # wins. Non-doc-number fields (even at higher confidence) are ignored.
    fields = [
        {"field_key": "total", "value": "100.00", "confidence": 0.99, "is_doc_number": False},
        {"field_key": "invoice_number", "value": "INV-1", "confidence": 0.72, "is_doc_number": True},
        {"field_key": "po_number", "value": "PO-9", "confidence": 0.86, "is_doc_number": True},
    ]
    key, conf = ingest._best_doc_number(fields)
    assert key == "PO-9"
    assert conf == 0.86


def test_best_doc_number_none_when_no_doc_field():
    fields = [{"field_key": "total", "value": "100", "confidence": 0.9, "is_doc_number": False}]
    key, conf = ingest._best_doc_number(fields)
    assert key == ""
    assert conf == 0.0


def test_extract_external_key_no_hint_sweeps_profiles():
    # No class hint → regex-only sweep across the default profiles (no DB / LLM).
    # A clear invoice should still surface its document number.
    text = "Invoice #INV-2025-0042\nAmount Due: $1,234.56\nDate: 01/15/2025"
    key, conf = asyncio.run(ingest._extract_external_key("tenant-x", "", text))
    assert "INV" in key
    assert conf > 0.0


def test_extract_external_key_empty_text():
    key, conf = asyncio.run(ingest._extract_external_key("tenant-x", "invoice", ""))
    assert key == ""
    assert conf == 0.0
