"""Tests for regex extraction patterns."""
from app.tasks.extract import _regex_extract_invoice


def test_invoice_number_extracted():
    text = "Invoice #INV-2025-0042\nAmount Due: $1,234.56\nDate: 01/15/2025\nPO Number: PO-9876"
    fields, conf = _regex_extract_invoice(text)
    assert fields.get("invoice_number") == "INV-2025-0042"
    assert "1,234.56" in fields.get("total_amount", "")
    assert fields.get("po_number") == "PO-9876"
    assert conf == 1.0  # all 4 patterns matched


def test_partial_match():
    text = "Invoice Number: ABC123. No other fields."
    fields, conf = _regex_extract_invoice(text)
    assert fields.get("invoice_number") == "ABC123"
    assert conf == 0.25  # 1/4


def test_no_match():
    text = "This is a generic document with no invoice fields."
    fields, conf = _regex_extract_invoice(text)
    assert conf == 0.0
