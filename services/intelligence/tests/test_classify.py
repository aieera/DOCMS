"""Tests for the 3-tier classifier — only tier 1 (rules) runs without GPU/LLM."""
from app.tasks.classify import _tier1_rules


def test_invoice_keywords():
    text = "Invoice Number: 12345. Amount Due: $500. Bill To: Acme Corp. Invoice Date: 2025-01-15."
    cls, conf = _tier1_rules(text)
    assert cls == "invoice"
    assert conf > 0.3


def test_contract_keywords():
    text = "This Agreement is entered into by the parties. The governing law shall be whereas hereby."
    cls, conf = _tier1_rules(text)
    assert cls == "contract"


def test_no_match_returns_low_confidence():
    text = "Random words that don't match any class at all."
    cls, conf = _tier1_rules(text)
    assert conf < 0.3


def test_resume_keywords():
    text = "Experience: 5 years. Education: BS Computer Science. Skills: Python, Go. Objective: Senior role. References available."
    cls, conf = _tier1_rules(text)
    assert cls == "resume"
    assert conf >= 0.4
