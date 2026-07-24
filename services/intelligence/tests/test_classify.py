"""Tests for the 3-tier classifier — only tier 1 (rules) runs without GPU/LLM.

_tier1_rules returns (class, confidence, evidence) — the third element is
the per-class keyword-hit evidence the review queue displays.
"""
from app.tasks.classify import _tier1_rules


def test_invoice_keywords():
    text = "Invoice Number: 12345. Amount Due: $500. Bill To: Acme Corp. Invoice Date: 2025-01-15."
    cls, conf, _ = _tier1_rules(text)
    assert cls == "invoice"
    assert conf > 0.3


def test_contract_keywords():
    text = "This Agreement is entered into by the parties. The governing law shall be whereas hereby."
    cls, conf, _ = _tier1_rules(text)
    assert cls == "contract"


def test_no_match_returns_low_confidence():
    text = "Random words that don't match any class at all."
    cls, conf, _ = _tier1_rules(text)
    assert conf < 0.3


def test_resume_keywords():
    text = "Experience: 5 years. Education: BS Computer Science. Skills: Python, Go. Objective: Senior role. References available."
    cls, conf, _ = _tier1_rules(text)
    assert cls == "resume"
    assert conf >= 0.4


def test_evidence_lists_matched_keywords():
    text = "Invoice Number: 12345. Amount Due: $500."
    _, _, evidence = _tier1_rules(text)
    assert isinstance(evidence, list)
    assert evidence, "tier-1 match should carry keyword evidence"
