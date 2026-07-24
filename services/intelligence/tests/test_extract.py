"""Tests for regex field extraction (per-class profiles).

The old invoice-shaped helper (_regex_extract_invoice → (fields, conf))
was replaced by the profile-driven _regex_extract(text, specs) that
returns per-field {value, confidence, method, is_doc_number} dicts and
takes its specs from app.extraction_profiles.DEFAULT_PROFILES.
"""
from app.config import settings
from app.extraction_profiles import DEFAULT_PROFILES, normalize_class
from app.tasks.extract import _regex_extract

INVOICE_SPECS = DEFAULT_PROFILES["invoice"]


def test_invoice_fields_extracted():
    text = (
        "Invoice #INV-2025-0042\n"
        "Amount Due: $1,234.56\n"
        "Invoice Date: 01/15/2025\n"
        "Bill To: Acme Corp\n"
    )
    out = _regex_extract(text, INVOICE_SPECS)
    assert out["invoice_number"]["value"] == "INV-2025-0042"
    assert out["invoice_number"]["is_doc_number"] is True
    assert out["invoice_number"]["method"] == "regex"
    assert out["invoice_number"]["confidence"] == settings.extraction_regex_confidence
    assert "1,234.56" in out["total"]["value"]


def test_partial_match_returns_only_matched_fields():
    text = "Invoice Number: ABC123. No other fields."
    out = _regex_extract(text, INVOICE_SPECS)
    assert out["invoice_number"]["value"] == "ABC123"
    assert "total" not in out
    assert "customer_name" not in out


def test_no_match_returns_empty():
    text = "This is a generic document with no invoice fields."
    out = _regex_extract(text, INVOICE_SPECS)
    assert out == {}


def test_class_aliases_normalize_to_invoice():
    # "bill" is an alias — routing must land on the invoice profile.
    assert normalize_class("bill") == "invoice"
    assert normalize_class("invoice") == "invoice"
