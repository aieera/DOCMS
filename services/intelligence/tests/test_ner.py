"""Tests for regex PII detection (no SpaCy needed)."""
from app.tasks.ner import _regex_pii, _luhn_valid


def test_ssn_detected():
    text = "SSN: 123-45-6789 is on file."
    ents = _regex_pii(text)
    ssns = [e for e in ents if e["entity_type"] == "SSN"]
    assert len(ssns) == 1
    assert ssns[0]["entity_value"] == "123-45-6789"
    assert ssns[0]["is_pii"] is True


def test_credit_card_luhn():
    assert _luhn_valid("4111111111111111") is True
    assert _luhn_valid("1234567890123456") is False


def test_credit_card_detected():
    text = "Card: 4111-1111-1111-1111 please charge."
    ents = _regex_pii(text)
    ccs = [e for e in ents if e["entity_type"] == "CREDIT_CARD"]
    assert len(ccs) == 1


def test_email_detected():
    text = "Contact alice@example.com for details."
    ents = _regex_pii(text)
    emails = [e for e in ents if e["entity_type"] == "EMAIL"]
    assert len(emails) == 1
    assert emails[0]["entity_value"] == "alice@example.com"


def test_phone_detected():
    text = "Call (555) 123-4567 now."
    ents = _regex_pii(text)
    phones = [e for e in ents if e["entity_type"] == "PHONE"]
    assert len(phones) == 1


def test_dob_detected():
    text = "Date of Birth: 01/15/1990 is in the record."
    ents = _regex_pii(text)
    dobs = [e for e in ents if e["entity_type"] == "DATE_OF_BIRTH"]
    assert len(dobs) == 1


def test_no_false_positives():
    text = "The quick brown fox jumps over the lazy dog."
    ents = _regex_pii(text)
    pii = [e for e in ents if e["is_pii"]]
    assert len(pii) == 0
