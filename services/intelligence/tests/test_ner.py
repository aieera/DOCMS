"""Regex + dedupe + LLM-validation tests for the NER pipeline (ADR 0078).
SpaCy + the actual LLM call are stubbed; we test the deterministic logic."""
from __future__ import annotations

from unittest import mock
import pytest

from app.tasks.ner import _regex_pass, _luhn_valid, _dedupe, _spacy_pass
from app.tasks.ner_llm import (
    NERConfig,
    _validate_and_realign,
    DEFAULT_NER_CONFIG,
)


# ---- regex matchers -----------------------------------------------------

def test_ssn_detected():
    ents = _regex_pass("SSN: 123-45-6789 is on file.")
    ssns = [e for e in ents if e["entity_type"] == "national_id"]
    assert len(ssns) == 1
    assert ssns[0]["entity_value"] == "123-45-6789"
    assert ssns[0]["is_pii"] is True
    assert ssns[0]["source"] == "regex"


def test_credit_card_luhn():
    assert _luhn_valid("4111111111111111") is True
    assert _luhn_valid("1234567890123456") is False


def test_credit_card_detected():
    ents = _regex_pass("Card: 4111-1111-1111-1111 please charge.")
    ccs = [e for e in ents if e["entity_type"] == "credit_card"]
    assert len(ccs) == 1


def test_email_detected():
    ents = _regex_pass("Contact alice@example.com for details.")
    emails = [e for e in ents if e["entity_type"] == "email"]
    assert len(emails) == 1
    assert emails[0]["entity_value"] == "alice@example.com"


def test_phone_detected():
    ents = _regex_pass("Call (555) 123-4567 now.")
    phones = [e for e in ents if e["entity_type"] == "phone"]
    assert len(phones) == 1


def test_dob_detected():
    ents = _regex_pass("Date of Birth: 01/15/1990 is in the record.")
    dobs = [e for e in ents if e["entity_type"] == "dob"]
    assert len(dobs) == 1


def test_no_false_positives_on_plain_text():
    ents = _regex_pass("The quick brown fox jumps over the lazy dog.")
    pii = [e for e in ents if e["is_pii"]]
    assert len(pii) == 0


# ---- medical NER per blueprint §6.6 deliverable #7 ---------------------

def test_synthetic_phi_medical_doc_detects_icd_cpt_and_pii():
    """The §6.6 acceptance test: a synthetic medical doc with PHI must
    surface ICD codes, CPT codes, and the standard PII set in one run."""
    doc = (
        "Patient: John Doe\n"
        "DOB: 03/14/1965\n"
        "SSN: 123-45-6789\n"
        "Email: jdoe@example.com\n"
        "Phone: (555) 867-5309\n"
        "\n"
        "Primary diagnosis: E11.9 (Type 2 diabetes mellitus without complications).\n"
        "Secondary: I10 (essential hypertension).\n"
        "Procedure performed (CPT 99213): office/outpatient visit.\n"
        "Additional service code 93000 — electrocardiogram.\n"
        "Charged: $245.00 USD to insurer.\n"
    )
    ents = _regex_pass(doc)
    by_type = {e["entity_type"]: e for e in ents}
    # PII tier
    assert "national_id" in by_type
    assert "email"       in by_type
    assert "phone"       in by_type
    assert "dob"         in by_type
    # Medical tier
    icd = [e for e in ents if e["entity_type"] == "icd_code"]
    assert {e["entity_value"] for e in icd} == {"E11.9", "I10"}
    cpt = [e for e in ents if e["entity_type"] == "cpt_code"]
    assert {e["entity_value"] for e in cpt} == {"99213", "93000"}
    # Financial tier
    amount = [e for e in ents if e["entity_type"] == "amount"]
    assert any("245" in e["entity_value"] for e in amount)


def test_cpt_filters_year_and_no_context():
    # 2024 should not be a CPT code; 12345 without context shouldn't either.
    ents = _regex_pass("In 2024 the model returned 12345 as the result.")
    cpt = [e for e in ents if e["entity_type"] == "cpt_code"]
    assert cpt == []


def test_icd10_excludes_u_prefix_and_alphanumeric_neighbors():
    # U codes are reserved; we should skip them.
    ents = _regex_pass("Code U07.1 was used previously; current is J45.909.")
    icd = {e["entity_value"] for e in ents if e["entity_type"] == "icd_code"}
    assert "U07.1" not in icd
    assert "J45.909" in icd


def test_amount_emits_currency_separately():
    ents = _regex_pass("Total: $1,234.56 due on receipt.")
    by_type = [e["entity_type"] for e in ents]
    assert "amount" in by_type
    assert "currency" in by_type


def test_account_number_requires_context():
    # Bare 12345678 should NOT match without account context.
    plain = _regex_pass("Order 12345678 shipped today.")
    assert not [e for e in plain if e["entity_type"] == "account_number"]
    # With context it should.
    with_ctx = _regex_pass("Account #: 12345678 — please reference.")
    assert [e for e in with_ctx if e["entity_type"] == "account_number"]


def test_ein_tax_id_detected():
    ents = _regex_pass("EIN 12-3456789 on file.")
    tax = [e for e in ents if e["entity_type"] == "tax_id"]
    assert len(tax) == 1


# ---- dedupe -------------------------------------------------------------

def test_dedupe_regex_wins_over_llm_on_same_span():
    overlapping = [
        {"entity_type": "name", "entity_value": "alice@x.io",
         "start_offset": 8, "end_offset": 18, "confidence": 0.7,
         "is_pii": True, "source": "llm"},
        {"entity_type": "email", "entity_value": "alice@x.io",
         "start_offset": 8, "end_offset": 18, "confidence": 0.9,
         "is_pii": True, "source": "regex"},
    ]
    out = _dedupe(overlapping)
    assert len(out) == 1
    assert out[0]["source"] == "regex"
    assert out[0]["entity_type"] == "email"


def test_dedupe_keeps_disjoint_spans():
    items = [
        {"entity_type": "email", "entity_value": "a@b.io",
         "start_offset": 0, "end_offset": 6, "confidence": 0.9,
         "is_pii": True, "source": "regex"},
        {"entity_type": "phone", "entity_value": "555-1234",
         "start_offset": 10, "end_offset": 18, "confidence": 0.85,
         "is_pii": True, "source": "regex"},
    ]
    out = _dedupe(items)
    assert len(out) == 2


def test_dedupe_longer_span_wins_within_same_source():
    # Two regex hits, one nested inside the other — keep the longer.
    items = [
        {"entity_type": "amount", "entity_value": "$1,234.56",
         "start_offset": 0, "end_offset": 9, "confidence": 0.85,
         "is_pii": False, "source": "regex"},
        {"entity_type": "currency", "entity_value": "$",
         "start_offset": 0, "end_offset": 1, "confidence": 0.95,
         "is_pii": False, "source": "regex"},
    ]
    out = _dedupe(items)
    assert len(out) == 1
    assert out[0]["entity_type"] == "amount"


# ---- LLM offset validation ---------------------------------------------

def _cfg(**overrides) -> NERConfig:
    return NERConfig.from_dict({**DEFAULT_NER_CONFIG, **overrides, "llm_enabled": True})


def test_llm_validate_keeps_correct_offsets():
    text = "Governing law: New York. Effective date: 2026-04-19."
    raw = [
        {"type": "governing_law", "value": "New York",
         "char_start": 15, "char_end": 23, "confidence": 0.92},
    ]
    out = _validate_and_realign(raw, text, _cfg())
    assert len(out) == 1
    assert out[0]["entity_value"] == "New York"
    assert out[0]["source"] == "llm"


def test_llm_validate_falls_back_to_substring_on_bad_offsets():
    text = "Governing law: Delaware."
    raw = [
        {"type": "governing_law", "value": "Delaware",
         "char_start": 0, "char_end": 8, "confidence": 0.9},  # WRONG offsets
    ]
    out = _validate_and_realign(raw, text, _cfg())
    assert len(out) == 1
    assert out[0]["start_offset"] == text.find("Delaware")


def test_llm_validate_drops_hallucinated_value():
    raw = [
        {"type": "governing_law", "value": "Atlantis",
         "char_start": 0, "char_end": 8, "confidence": 0.9},
    ]
    out = _validate_and_realign(raw, "Governing law: New York.", _cfg())
    assert out == []


def test_llm_validate_drops_below_min_confidence():
    text = "Effective date: 2026-04-19."
    raw = [{"type": "effective_date", "value": "2026-04-19",
            "char_start": 16, "char_end": 26, "confidence": 0.4}]
    out = _validate_and_realign(raw, text, _cfg(llm_min_confidence=0.6))
    assert out == []


def test_llm_validate_drops_unwanted_type():
    text = "Charged: $245.00"
    raw = [{"type": "credit_card", "value": "$245.00",
            "char_start": 9, "char_end": 16, "confidence": 0.95}]
    # credit_card isn't in the LLM target list; should be dropped even
    # if regex would have caught it elsewhere.
    out = _validate_and_realign(raw, text, _cfg())
    assert out == []


# ---- spacy pass mapping (stub the underlying extractor) ---------------

def test_spacy_pass_maps_taxonomy():
    fake = [
        {"entity_type": "PERSON", "entity_value": "Alice",
         "start_offset": 0, "end_offset": 5, "confidence": 0.9, "is_pii": True},
        {"entity_type": "ORG", "entity_value": "Acme Corp",
         "start_offset": 10, "end_offset": 19, "confidence": 0.8, "is_pii": False},
        {"entity_type": "WEIRD_LABEL", "entity_value": "ignore me",
         "start_offset": 20, "end_offset": 29, "confidence": 0.5, "is_pii": False},
    ]
    with mock.patch("app.models.ner_model.extract_entities", return_value=fake):
        out = _spacy_pass("Alice from Acme Corp ignore me")
    types = sorted(e["entity_type"] for e in out)
    assert types == ["name", "party_name"]
    assert all(e["source"] == "spacy" for e in out)


# ---- LLM call (mocked litellm) -----------------------------------------

@pytest.mark.asyncio
async def test_extract_via_llm_disabled_returns_empty():
    from app.tasks.ner_llm import extract_via_llm
    cfg = NERConfig.from_dict({**DEFAULT_NER_CONFIG, "llm_enabled": False})
    out = await extract_via_llm("Some text with stuff.", cfg)
    assert out == []


@pytest.mark.asyncio
async def test_extract_via_llm_parses_and_validates():
    text = "Governing law shall be the State of Delaware."
    fake_resp = {
        "choices": [
            {"message": {"content": (
                '{"entities": [{"type": "governing_law", "value": "Delaware",'
                ' "char_start": 36, "char_end": 44, "confidence": 0.9}]}'
            )}},
        ],
    }

    class FakeLitellm:
        @staticmethod
        async def acompletion(**_kw):
            return fake_resp

    with mock.patch.dict("sys.modules", {"litellm": FakeLitellm}):
        from app.tasks.ner_llm import extract_via_llm
        out = await extract_via_llm(text, _cfg())

    assert len(out) == 1
    assert out[0]["entity_type"] == "governing_law"
    assert out[0]["entity_value"] == "Delaware"
    assert out[0]["source"] == "llm"
