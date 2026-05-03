"""Compliance scan — pure rules + orchestration. DB stubbed."""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks.compliance_scan import (
    DEFAULT_CONFIG,
    PII_RISK_MAP,
    REGEX_PATTERNS,
    _build_summary,
    _dedup_against,
    _findings_from_ner,
    _findings_from_regex,
    _redact_match,
    _resolve_risk,
    _run_async,
    _validate_custom_patterns,
)
import re


# ---- risk classification ------------------------------------------------

def test_resolve_risk_pii_default():
    assert _resolve_risk("SSN", DEFAULT_CONFIG) == ("critical", "pii")
    assert _resolve_risk("EMAIL", DEFAULT_CONFIG) == ("medium", "pii")
    assert _resolve_risk("UNKNOWN_TYPE", DEFAULT_CONFIG) is None


def test_resolve_risk_phi_default():
    assert _resolve_risk("MEDICAL_RECORD", DEFAULT_CONFIG) == ("critical", "phi")
    assert _resolve_risk("MEDICATION", DEFAULT_CONFIG) == ("high", "phi")


def test_resolve_risk_tenant_override_wins():
    cfg = {**DEFAULT_CONFIG, "pii_entity_risk_overrides": {"EMAIL": "high"}}
    risk, _ = _resolve_risk("EMAIL", cfg)
    assert risk == "high"


# ---- ner findings -------------------------------------------------------

def test_findings_from_ner_groups_by_type():
    entities = [
        {"entity_type": "EMAIL", "entity_value": "a@b.com", "confidence": 0.9, "page_number": 1},
        {"entity_type": "EMAIL", "entity_value": "c@d.com", "confidence": 0.8, "page_number": 2},
        {"entity_type": "SSN", "entity_value": "123-45-6789", "confidence": 0.99, "page_number": 1},
        {"entity_type": "ORG", "entity_value": "Acme", "confidence": 0.9, "page_number": 1},  # not PII/PHI
    ]
    out = _findings_from_ner(entities, DEFAULT_CONFIG)
    by_type = {f["entity_type"]: f for f in out}
    assert "ORG" not in by_type, "non-PII entities are dropped"
    assert by_type["EMAIL"]["occurrence_count"] == 2
    assert by_type["EMAIL"]["page_numbers"] == [1, 2]
    assert by_type["EMAIL"]["entity_category"] == "pii"
    assert by_type["EMAIL"]["risk_level"] == "medium"
    assert by_type["SSN"]["risk_level"] == "critical"


# ---- regex findings -----------------------------------------------------

def test_regex_finds_ssn_in_text():
    text = "Employee SSN 123-45-6789 was filed on 2026-01-01."
    out = _findings_from_regex(text, REGEX_PATTERNS, DEFAULT_CONFIG, "pattern")
    by_type = {f["entity_type"]: f for f in out}
    assert "SSN" in by_type
    assert by_type["SSN"]["occurrence_count"] == 1
    assert by_type["SSN"]["risk_level"] == "critical"
    # Sample is redacted — value must NOT appear.
    assert "123-45-6789" not in by_type["SSN"]["sample_context"]


def test_regex_empty_text_returns_empty():
    assert _findings_from_regex("", REGEX_PATTERNS, DEFAULT_CONFIG, "pattern") == []


def test_dedup_pattern_against_ner():
    ner = [{
        "entity_type": "EMAIL", "entity_category": "pii",
        "occurrence_count": 1, "_values": ["a@b.com"],
    }]
    patterns = [{
        "entity_type": "EMAIL", "entity_category": "pii",
        "occurrence_count": 2, "_values": ["a@b.com", "x@y.com"],
        "page_numbers": [], "confidence": 0.99, "risk_level": "medium",
        "sample_context": "", "detection_source": "pattern",
    }]
    out = _dedup_against(patterns, ner)
    assert len(out) == 1
    assert out[0]["_values"] == ["x@y.com"]
    assert out[0]["occurrence_count"] == 1


def test_dedup_drops_finding_when_all_values_already_in_ner():
    ner = [{
        "entity_type": "EMAIL", "entity_category": "pii",
        "occurrence_count": 1, "_values": ["a@b.com"],
    }]
    patterns = [{
        "entity_type": "EMAIL", "entity_category": "pii",
        "occurrence_count": 1, "_values": ["a@b.com"],
        "page_numbers": [], "confidence": 0.99, "risk_level": "medium",
        "sample_context": "", "detection_source": "pattern",
    }]
    assert _dedup_against(patterns, ner) == []


# ---- custom patterns ----------------------------------------------------

def test_validate_custom_patterns_skips_malformed():
    raw = [
        {"type": "TICKET_ID", "regex": r"TICK-\d{5}"},      # ok
        {"type": "BAD", "regex": r"["},                     # invalid regex
        {"type": "", "regex": r"x"},                        # empty type
        {"regex": r"y"},                                     # missing type
        "not a dict",                                       # totally wrong shape
    ]
    out = _validate_custom_patterns(raw)
    assert out == {"TICKET_ID": r"TICK-\d{5}"}


def test_validate_custom_patterns_caps_at_max():
    many = [{"type": f"T{i}", "regex": r"\d"} for i in range(40)]
    out = _validate_custom_patterns(many)
    assert len(out) == 32


# ---- redaction ----------------------------------------------------------

def test_redact_match_replaces_value_with_blocks():
    text = "secret 123-45-6789 here"
    m = re.search(r"\d{3}-\d{2}-\d{4}", text)
    sample = _redact_match(text, m)
    assert "123-45-6789" not in sample
    assert "▓" in sample


# ---- summary ------------------------------------------------------------

def test_build_summary_overall_risk_is_max():
    findings = [
        {"entity_type": "EMAIL", "entity_category": "pii", "risk_level": "medium"},
        {"entity_type": "SSN", "entity_category": "pii", "risk_level": "critical"},
        {"entity_type": "PHONE", "entity_category": "pii", "risk_level": "medium"},
    ]
    s = _build_summary(findings, DEFAULT_CONFIG, "doc", "ver")
    assert s["overall_risk"] == "critical"
    assert s["pii_count"] == 3
    assert s["phi_count"] == 0
    assert s["critical_count"] == 1
    assert s["medium_count"] == 2
    assert s["needs_review"] is True
    assert s["auto_held"] is False  # default config has auto_hold off


def test_build_summary_auto_held_when_config_enabled_and_critical_present():
    cfg = {**DEFAULT_CONFIG, "auto_hold_on_critical": True}
    findings = [{"entity_type": "SSN", "entity_category": "pii", "risk_level": "critical"}]
    s = _build_summary(findings, cfg, "doc", "ver")
    assert s["auto_held"] is True


def test_build_summary_empty_is_clean():
    s = _build_summary([], DEFAULT_CONFIG, "doc", "ver")
    assert s["overall_risk"] == "none"
    assert s["needs_review"] is False
    assert s["pii_count"] == 0
    assert s["entity_types_found"] == []


# ---- orchestration ------------------------------------------------------

@pytest.mark.asyncio
async def test_disabled_short_circuits():
    cfg = {**DEFAULT_CONFIG, "enabled": False}
    with mock.patch("app.tasks.compliance_scan.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.compliance_scan.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.compliance_scan.mark_completed",
                    new=mock.AsyncMock()) as mc, \
         mock.patch("app.tasks.compliance_scan._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.compliance_scan._fetch_ner_entities",
                    new=mock.AsyncMock()) as fe, \
         mock.patch("app.tasks.compliance_scan._fetch_ocr_text",
                    new=mock.AsyncMock()) as ft, \
         mock.patch("app.tasks.compliance_scan._persist", new=mock.AsyncMock()) as p:
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "disabled"
    fe.assert_not_awaited()
    ft.assert_not_awaited()
    p.assert_not_awaited()
    mc.assert_awaited_once()


@pytest.mark.asyncio
async def test_phi_dropped_when_disabled():
    """PHI entities surfaced by NER must be dropped when phi_enabled=false."""
    cfg = {**DEFAULT_CONFIG, "phi_enabled": False}
    persisted: dict = {}

    async def fake_persist(*, findings, summary, **_):
        persisted["findings"] = findings
        persisted["summary"] = summary

    with mock.patch("app.tasks.compliance_scan.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.compliance_scan.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.compliance_scan.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.compliance_scan._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.compliance_scan._fetch_ner_entities",
                    new=mock.AsyncMock(return_value=[
                        {"entity_type": "MEDICAL_RECORD", "entity_value": "MRN-001",
                         "confidence": 0.95, "page_number": 1},
                        {"entity_type": "EMAIL", "entity_value": "a@b.com",
                         "confidence": 0.9, "page_number": 1},
                    ])), \
         mock.patch("app.tasks.compliance_scan._fetch_ocr_text",
                    new=mock.AsyncMock(return_value="")), \
         mock.patch("app.tasks.compliance_scan._persist", new=fake_persist):
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )

    types = {f["entity_type"] for f in persisted["findings"]}
    assert "MEDICAL_RECORD" not in types
    assert "EMAIL" in types
    assert persisted["summary"]["phi_count"] == 0


@pytest.mark.asyncio
async def test_phi_kept_when_enabled():
    cfg = {**DEFAULT_CONFIG, "phi_enabled": True}
    persisted: dict = {}

    async def fake_persist(*, findings, summary, **_):
        persisted["findings"] = findings
        persisted["summary"] = summary

    with mock.patch("app.tasks.compliance_scan.already_completed",
                    new=mock.AsyncMock(return_value=False)), \
         mock.patch("app.tasks.compliance_scan.mark_enqueued", new=mock.AsyncMock()), \
         mock.patch("app.tasks.compliance_scan.mark_completed", new=mock.AsyncMock()), \
         mock.patch("app.tasks.compliance_scan._load_config",
                    new=mock.AsyncMock(return_value=cfg)), \
         mock.patch("app.tasks.compliance_scan._fetch_ner_entities",
                    new=mock.AsyncMock(return_value=[
                        {"entity_type": "MEDICAL_RECORD", "entity_value": "MRN-001",
                         "confidence": 0.95, "page_number": 1},
                    ])), \
         mock.patch("app.tasks.compliance_scan._fetch_ocr_text",
                    new=mock.AsyncMock(return_value="")), \
         mock.patch("app.tasks.compliance_scan._persist", new=fake_persist):
        await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert persisted["summary"]["phi_count"] == 1
    assert persisted["summary"]["overall_risk"] == "critical"


@pytest.mark.asyncio
async def test_duplicate_event_short_circuits():
    with mock.patch("app.tasks.compliance_scan.already_completed",
                    new=mock.AsyncMock(return_value=True)), \
         mock.patch("app.tasks.compliance_scan.mark_enqueued",
                    new=mock.AsyncMock()) as me:
        out = await _run_async(
            tenant_id="t1", document_id="d1", version_id="v1",
            event_id="e1", correlation_id="c1", attempt=1, is_terminal=False,
        )
    assert out["status"] == "duplicate"
    me.assert_not_awaited()
