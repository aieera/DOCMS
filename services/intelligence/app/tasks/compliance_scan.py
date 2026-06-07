"""Compliance scanning — PII / PHI detection (ADR 0054).

Triggered by: dms.ner.completed.v1
Persists to:  compliance_findings + compliance_summary
Emits:        dms.compliance.completed.v1   (always)
              dms.notification.send.v1      (if notify_on_high + risk >= high)

Three detection sources fan in:
  ner       — entities table rows mapped via PII_RISK_MAP / PHI_RISK_MAP
  pattern   — built-in regex pass over raw OCR text for entities NER
              under-recalls (SSN, credit-card, IP, DOB markers)
  custom    — tenant-defined regex patterns from compliance_config

Per ADR 0054:
  * No PII value ever appears in a log line. Logs carry entity_type,
    count, risk_level, never the value itself.
  * sample_context redacts the matched value to ▓ characters.
  * encrypted_values column stays NULL until the storage-side
    crypto wrap API lands; values themselves aren't stored in v1.
  * auto_held in the summary is a *recommendation*. Placement of the
    legal hold is a domain action that lives in the document service.
"""
from __future__ import annotations

import asyncio
import json
import logging
import re
import time
import uuid
from collections import defaultdict
from datetime import datetime, timezone
from typing import Any

from app.error_classifier import classify_error_reason
from app.intel_dedupe import (
    already_completed,
    mark_completed,
    mark_enqueued,
    mark_failed,
    publish_dlq,
)
from app.worker import celery_app
from app.db.pool import get_pool

log = logging.getLogger(__name__)

COMPLIANCE_COMPLETED_SUBJECT = "dms.compliance.completed.v1"
NOTIFICATION_SUBJECT = "dms.notification.send.v1"
CONSUMER = "compliance_scan"

PII_RISK_MAP: dict[str, str] = {
    "SSN":             "critical",
    "CREDIT_CARD":     "critical",
    "BANK_ACCOUNT":    "critical",
    "PASSPORT":        "critical",
    "TAX_ID":          "high",
    "DRIVER_LICENSE":  "high",
    "DOB":             "high",
    "EMAIL":           "medium",
    "PHONE":           "medium",
    "ADDRESS":         "medium",
    "IP_ADDRESS":      "low",
}

PHI_RISK_MAP: dict[str, str] = {
    "MEDICAL_RECORD":  "critical",
    "DIAGNOSIS":       "critical",
    "LAB_RESULT":      "critical",
    "BIOMETRIC":       "critical",
    "MEDICATION":      "high",
    "PROCEDURE":       "high",
    "INSURANCE_ID":    "high",
    "HEALTH_PLAN":     "medium",
}

REGEX_PATTERNS: dict[str, str] = {
    "SSN":         r"\b\d{3}-\d{2}-\d{4}\b",
    "CREDIT_CARD": r"\b(?:\d{4}[-\s]?){3}\d{4}\b",
    "EMAIL":       r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b",
    "PHONE":       r"\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b",
    "IP_ADDRESS":  r"\b(?:\d{1,3}\.){3}\d{1,3}\b",
    "DOB":         r"\b(?:DOB|Date of Birth|Born)[\s:]+\d{1,2}[/\-]\d{1,2}[/\-]\d{2,4}\b",
}

RISK_ORDER = ["none", "low", "medium", "high", "critical"]
SAMPLE_RADIUS = 40
MAX_CUSTOM_PATTERNS = 32


@celery_app.task(
    name="app.tasks.compliance_scan.compliance_scan",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=180,
    time_limit=240,
)
def compliance_scan(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id,
        document_id=document_id,
        version_id=version_id,
        event_id=event_id,
        correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 3,
    ))


# ---- entry point ---------------------------------------------------------

async def _run_async(
    *, tenant_id, document_id, version_id, event_id, correlation_id,
    attempt, is_terminal,
) -> dict:
    start = time.monotonic()
    if not (tenant_id and document_id and version_id):
        return {"status": "skipped", "reason": "missing ids"}

    try:
        if event_id and await already_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ):
            return {"status": "duplicate", "event_id": event_id}

        await mark_enqueued(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
            document_id=document_id, version_id=version_id,
        )

        cfg = await _load_config(tenant_id)
        if not cfg["enabled"]:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "disabled"}

        entities = await _fetch_ner_entities(tenant_id, version_id)
        ocr_text = await _fetch_ocr_text(tenant_id, version_id)

        # Source 1: NER → mapped entity types.
        ner_findings = _findings_from_ner(entities, cfg)
        # Source 2: built-in regex pass.
        pattern_findings = _findings_from_regex(ocr_text, REGEX_PATTERNS, cfg, "pattern")
        # Source 3: tenant custom patterns.
        custom_patterns = _validate_custom_patterns(cfg.get("custom_patterns", []))
        custom_findings = _findings_from_regex(ocr_text, custom_patterns, cfg, "custom")

        # Position-overlap dedup: drop pattern findings whose value already
        # appeared in NER. (Cheap: same entity_type + same value substring.)
        pattern_findings = _dedup_against(pattern_findings, ner_findings)

        all_findings = ner_findings + pattern_findings + custom_findings
        # PHI gate — drop PHI findings entirely if the tenant hasn't opted in.
        if not cfg.get("phi_enabled", False):
            all_findings = [f for f in all_findings if f["entity_category"] != "phi"]

        summary = _build_summary(all_findings, cfg, document_id, version_id)

        await _persist(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            findings=all_findings,
            summary=summary,
            cfg=cfg,
            correlation_id=correlation_id,
        )

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)

        elapsed_ms = int((time.monotonic() - start) * 1000)
        # Logs deliberately omit any value field — only types + counts.
        log.info(
            "compliance_scan.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "overall_risk": summary["overall_risk"],
                "pii_count": summary["pii_count"],
                "phi_count": summary["phi_count"],
                "auto_held_recommended": summary["auto_held"],
                "entity_types": summary["entity_types_found"],
                "elapsed_ms": elapsed_ms,
                "correlation_id": correlation_id,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "overall_risk": summary["overall_risk"],
            "finding_count": len(all_findings),
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        if is_terminal:
            reason = classify_error_reason(exc)
            try:
                await publish_dlq(
                    consumer=CONSUMER, reason=reason, tenant_id=tenant_id,
                    document_id=document_id, version_id=version_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=attempt, correlation_id=correlation_id,
                )
                await mark_failed(
                    tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                )
            except Exception:
                log.exception("compliance_scan terminal DLQ publish failed")
        raise


# ---- config + lookup ----------------------------------------------------

DEFAULT_CONFIG = {
    "enabled": True,
    "auto_hold_on_critical": False,
    "notify_on_high": True,
    "notify_roles": ["compliance_officer", "admin"],
    "pii_entity_risk_overrides": {},
    "phi_enabled": False,
    "custom_patterns": [],
}


async def _load_config(tenant_id: str) -> dict:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT enabled, auto_hold_on_critical, notify_on_high,
                       notify_roles, pii_entity_risk_overrides, phi_enabled,
                       custom_patterns
                  FROM compliance_config WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return dict(DEFAULT_CONFIG)
    overrides = row["pii_entity_risk_overrides"]
    if isinstance(overrides, str):
        overrides = json.loads(overrides)
    custom = row["custom_patterns"]
    if isinstance(custom, str):
        custom = json.loads(custom)
    return {
        "enabled": row["enabled"],
        "auto_hold_on_critical": row["auto_hold_on_critical"],
        "notify_on_high": row["notify_on_high"],
        "notify_roles": list(row["notify_roles"] or []),
        "pii_entity_risk_overrides": overrides or {},
        "phi_enabled": row["phi_enabled"],
        "custom_patterns": custom or [],
    }


async def _fetch_ner_entities(tenant_id: str, version_id: str) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            # document_entities tracks character offsets (start_offset /
            # end_offset), NOT page numbers — selecting a non-existent
            # page_number column raised UndefinedColumnError and aborted the
            # whole scan before anything was persisted. Page resolution isn't
            # available here, so findings carry page 0 (same as the regex pass).
            rows = await conn.fetch(
                """
                SELECT entity_type, entity_value, confidence
                  FROM document_entities
                 WHERE tenant_id = $1 AND version_id = $2
                """,
                tenant_id, version_id,
            )
    return [
        {
            "entity_type": (r["entity_type"] or "").upper(),
            "entity_value": r["entity_value"] or "",
            "confidence": float(r["confidence"] or 0.0),
            "page_number": 0,
        }
        for r in rows
    ]


async def _fetch_ocr_text(tenant_id: str, version_id: str) -> str:
    """Concatenate OCR pages for this version. Empty when OCR hasn't
    completed yet — the regex pass simply finds nothing."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            # ocr_results is per-PAGE (page_number, text_content); there is no
            # full_text column. Concatenate the pages in order so the regex
            # pass sees the whole document. (Same fix already applied to
            # translate + lang_detect.)
            row = await conn.fetchrow(
                """
                SELECT COALESCE(
                         string_agg(text_content, E'\n' ORDER BY page_number),
                         ''
                       ) AS full_text
                  FROM ocr_results
                 WHERE tenant_id = $1 AND version_id = $2
                """,
                tenant_id, version_id,
            )
    return (row["full_text"] if row else "") or ""


# ---- detection ---------------------------------------------------------

def _resolve_risk(entity_type: str, cfg: dict) -> tuple[str, str] | None:
    """Return (risk_level, category) for an entity_type, or None to skip.
    Tenant override wins over the static maps."""
    overrides = cfg.get("pii_entity_risk_overrides") or {}
    if entity_type in overrides:
        return str(overrides[entity_type]), _category_for(entity_type)
    if entity_type in PII_RISK_MAP:
        return PII_RISK_MAP[entity_type], "pii"
    if entity_type in PHI_RISK_MAP:
        return PHI_RISK_MAP[entity_type], "phi"
    return None


def _category_for(entity_type: str) -> str:
    return "phi" if entity_type in PHI_RISK_MAP else "pii"


def _findings_from_ner(entities: list[dict], cfg: dict) -> list[dict]:
    """Group NER entities by type into one finding per type."""
    by_type: dict[str, dict[str, Any]] = defaultdict(lambda: {
        "values": [], "pages": set(), "confidences": [],
    })
    for e in entities:
        risk = _resolve_risk(e["entity_type"], cfg)
        if risk is None:
            continue
        rec = by_type[e["entity_type"]]
        rec["values"].append(e["entity_value"])
        if e["page_number"]:
            rec["pages"].add(e["page_number"])
        rec["confidences"].append(e["confidence"])

    out = []
    for etype, rec in by_type.items():
        risk = _resolve_risk(etype, cfg)
        if risk is None:
            continue
        risk_level, category = risk
        out.append({
            "entity_type": etype,
            "entity_category": category,
            "occurrence_count": len(rec["values"]),
            "page_numbers": sorted(rec["pages"]),
            "confidence": sum(rec["confidences"]) / max(1, len(rec["confidences"])),
            "risk_level": risk_level,
            "sample_context": _redacted_sample(rec["values"][0]),
            "detection_source": "ner",
            "_values": rec["values"],
        })
    return out


def _findings_from_regex(text: str, patterns: dict[str, str], cfg: dict,
                          source: str) -> list[dict]:
    if not text or not patterns:
        return []
    out = []
    for etype, regex in patterns.items():
        try:
            compiled = re.compile(regex)
        except re.error:
            continue
        matches = list(compiled.finditer(text))
        if not matches:
            continue
        risk = _resolve_risk(etype, cfg)
        if risk is None:
            # Custom-pattern-only types fall back to medium with category=pii.
            if source == "custom":
                risk_level, category = "medium", "pii"
            else:
                continue
        else:
            risk_level, category = risk
        values = [m.group(0) for m in matches]
        first_match = matches[0]
        sample = _redact_match(text, first_match)
        out.append({
            "entity_type": etype,
            "entity_category": category,
            "occurrence_count": len(values),
            "page_numbers": [],  # regex over full_text — no page resolution
            "confidence": 0.99,  # regex match is exact
            "risk_level": risk_level,
            "sample_context": sample,
            "detection_source": source,
            "_values": values,
        })
    return out


def _validate_custom_patterns(raw: list) -> dict[str, str]:
    """Accept tenant config items shaped as {type, regex, risk?}.
    Cap to MAX_CUSTOM_PATTERNS. Skip malformed entries silently."""
    out: dict[str, str] = {}
    if not isinstance(raw, list):
        return out
    for item in raw[:MAX_CUSTOM_PATTERNS]:
        if not isinstance(item, dict):
            continue
        t = (item.get("type") or "").strip().upper()
        regex = item.get("regex") or ""
        if not t or not regex:
            continue
        try:
            re.compile(regex)
        except re.error:
            continue
        out[t] = regex
    return out


def _dedup_against(patterns: list[dict], ner: list[dict]) -> list[dict]:
    """Drop pattern findings whose entity_type + value already appeared
    in NER. Avoids double-counting EMAIL etc."""
    ner_pairs = set()
    for f in ner:
        for v in f.get("_values", []):
            ner_pairs.add((f["entity_type"], v))
    out = []
    for f in patterns:
        kept = [v for v in f.get("_values", []) if (f["entity_type"], v) not in ner_pairs]
        if not kept:
            continue
        f = dict(f)
        f["_values"] = kept
        f["occurrence_count"] = len(kept)
        out.append(f)
    return out


def _redacted_sample(value: str) -> str:
    if not value:
        return ""
    return f"…contains {''.join('▓' for _ in value[:8])}"


def _redact_match(text: str, m: re.Match) -> str:
    start, end = m.span()
    lo = max(0, start - SAMPLE_RADIUS)
    hi = min(len(text), end + SAMPLE_RADIUS)
    masked = "▓" * (end - start)
    return text[lo:start] + masked + text[end:hi]


# ---- summary -----------------------------------------------------------

def _build_summary(findings: list[dict], cfg: dict, document_id: str,
                    version_id: str) -> dict:
    counts = {"critical": 0, "high": 0, "medium": 0, "low": 0}
    pii = 0
    phi = 0
    types: set[str] = set()
    overall = "none"
    for f in findings:
        counts[f["risk_level"]] += 1
        types.add(f["entity_type"])
        if f["entity_category"] == "phi":
            phi += 1
        else:
            pii += 1
        if RISK_ORDER.index(f["risk_level"]) > RISK_ORDER.index(overall):
            overall = f["risk_level"]
    needs_review = counts["critical"] > 0 or counts["high"] > 0
    auto_held = bool(cfg.get("auto_hold_on_critical", False)) and counts["critical"] > 0
    return {
        "document_id": document_id,
        "version_id": version_id,
        "overall_risk": overall,
        "pii_count": pii,
        "phi_count": phi,
        "critical_count": counts["critical"],
        "high_count": counts["high"],
        "medium_count": counts["medium"],
        "low_count": counts["low"],
        "entity_types_found": sorted(types),
        "needs_review": needs_review,
        "auto_held": auto_held,
    }


# ---- persistence -------------------------------------------------------

async def _persist(
    *, tenant_id: str, document_id: str, version_id: str,
    findings: list[dict], summary: dict, cfg: dict, correlation_id: str,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )

            for f in findings:
                await conn.execute(
                    """
                    INSERT INTO compliance_findings
                        (tenant_id, document_id, version_id, entity_type,
                         entity_category, occurrence_count, page_numbers,
                         confidence, risk_level, sample_context,
                         detection_source)
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
                    ON CONFLICT (tenant_id, version_id, entity_type, detection_source)
                        WHERE remediation_status = 'open'
                    DO UPDATE SET
                        occurrence_count = EXCLUDED.occurrence_count,
                        page_numbers     = EXCLUDED.page_numbers,
                        confidence       = EXCLUDED.confidence,
                        risk_level       = EXCLUDED.risk_level,
                        sample_context   = EXCLUDED.sample_context,
                        created_at       = NOW()
                    """,
                    tenant_id, document_id, version_id, f["entity_type"],
                    f["entity_category"], f["occurrence_count"], f["page_numbers"],
                    float(f["confidence"]), f["risk_level"], f.get("sample_context") or "",
                    f.get("detection_source", "ner"),
                )

            await conn.execute(
                """
                INSERT INTO compliance_summary
                    (tenant_id, document_id, version_id, overall_risk,
                     pii_count, phi_count, critical_count, high_count,
                     medium_count, low_count, entity_types_found,
                     needs_review, auto_held, scanned_at)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW())
                ON CONFLICT (tenant_id, document_id) DO UPDATE
                   SET version_id         = EXCLUDED.version_id,
                       overall_risk       = EXCLUDED.overall_risk,
                       pii_count          = EXCLUDED.pii_count,
                       phi_count          = EXCLUDED.phi_count,
                       critical_count     = EXCLUDED.critical_count,
                       high_count         = EXCLUDED.high_count,
                       medium_count       = EXCLUDED.medium_count,
                       low_count          = EXCLUDED.low_count,
                       entity_types_found = EXCLUDED.entity_types_found,
                       needs_review       = EXCLUDED.needs_review,
                       auto_held          = EXCLUDED.auto_held,
                       scanned_at         = NOW()
                """,
                tenant_id, document_id, version_id, summary["overall_risk"],
                summary["pii_count"], summary["phi_count"],
                summary["critical_count"], summary["high_count"],
                summary["medium_count"], summary["low_count"],
                summary["entity_types_found"],
                summary["needs_review"], summary["auto_held"],
            )

            payload = {
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "overall_risk": summary["overall_risk"],
                "pii_count": summary["pii_count"],
                "phi_count": summary["phi_count"],
                "auto_held_recommended": summary["auto_held"],
                "entity_types_found": summary["entity_types_found"],
                "correlation_id": correlation_id,
                "emitted_at": datetime.now(timezone.utc).isoformat(),
            }
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'document', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                COMPLIANCE_COMPLETED_SUBJECT, document_id,
                json.dumps(payload),
            )

            # Notification on high/critical when configured.
            if cfg.get("notify_on_high", True) and summary["overall_risk"] in ("high", "critical"):
                notify_payload = {
                    "tenant_id": tenant_id,
                    "document_id": document_id,
                    "subject": "Compliance scan flagged a document",
                    "body": (
                        f"Document {document_id} flagged with "
                        f"{summary['overall_risk']} compliance risk: "
                        f"{', '.join(summary['entity_types_found'][:5])}"
                    ),
                    "target_roles": cfg.get("notify_roles") or ["compliance_officer", "admin"],
                    "category": "compliance",
                    "correlation_id": correlation_id,
                }
                await conn.execute(
                    """
                    INSERT INTO outbox
                        (id, tenant_id, event_type, aggregate_type, aggregate_id,
                         payload, created_at)
                    VALUES ($1, $2, $3, 'document', $4, $5::jsonb, NOW())
                    """,
                    uuid.uuid4(), tenant_id,
                    NOTIFICATION_SUBJECT, document_id,
                    json.dumps(notify_payload),
                )
