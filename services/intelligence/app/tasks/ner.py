"""Named Entity Recognition — three-tier ensemble (regex + SpaCy + LLM).

ADR 0061: SpaCy / regex / LLM all write to document_entities with a
`source` column for provenance. Regex wins ties (cheaper, deterministic);
LLM only runs for the types ner_config.llm_entity_types asks for, and
only when the tenant has opted in.

Hardening (Wave 5 Prompt 5.4) is unchanged: acks_late, 3 jittered
retries, dedupe via intel_processed_events, NATS emit on success, DLQ
on terminal failure.
"""
from __future__ import annotations

import asyncio
import logging
import re
import time
import uuid
from datetime import datetime, timezone
from typing import Any

from app.config import settings
from app.events.publisher import publish_cloudevent
from app.error_classifier import classify_error_reason as _classify_error
from app.intel_dedupe import (
    mark_completed,
    mark_enqueued,
    mark_failed,
    publish_dlq,
)
from app.metrics import (
    ner_documents_total,
    ner_dlq_total,
    ner_duration_seconds,
    ner_entities_total,
)
from app.persist import replace_entities
from app.tasks.ner_llm import extract_via_llm, load_ner_config
from app.worker import celery_app

log = logging.getLogger(__name__)

NER_COMPLETED_SUBJECT = "dms.ner.completed.v1"
CONSUMER = "ner"

# ---- Regex matchers (deterministic, fast) --------------------------------

SSN_RE = re.compile(r'\b\d{3}-\d{2}-\d{4}\b')
CC_RE = re.compile(r'\b(?:\d{4}[\s-]?){3}\d{4}\b')
EMAIL_RE = re.compile(r'\b[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b')
PHONE_RE = re.compile(r'\b(?:\+\d{1,3}[\s-]?)?\(?\d{3}\)?[\s.-]?\d{3}[\s.-]?\d{4}\b')
DOB_RE = re.compile(
    r'(?:DOB|Date of Birth|Born)[\s:]*(\d{1,2}[/-]\d{1,2}[/-]\d{2,4})',
    re.IGNORECASE,
)
# ICD-10: letter (not U) + 2 digits + optional decimal subcode (1-4 digits).
# Common false-positive guard: require a non-alphanumeric on both sides.
ICD10_RE = re.compile(
    r'(?<![A-Za-z0-9])([A-TV-Z]\d{2}(?:\.\d{1,4})?)(?![A-Za-z0-9])',
)
# CPT: 5 digits, surrounded by non-digits. Filter out years and amounts
# in the validator below.
CPT_RE = re.compile(r'(?<!\d)(\d{5})(?!\d)')
# Currency-prefixed amount: $1,234.56  /  USD 1234  /  £99.00
AMOUNT_RE = re.compile(
    r'(?P<currency>\$|€|£|¥|USD|EUR|GBP|JPY|INR|AUD|CAD)\s?'
    r'(?P<amount>\d{1,3}(?:[,.]\d{3})*(?:\.\d{1,2})?|\d+(?:\.\d{1,2})?)',
    re.IGNORECASE,
)
# Account-number heuristic: "Account #12345678" / "Acct No: 9876543210"
ACCOUNT_RE = re.compile(
    r'\b(?:account|acct|a/c)\s*(?:#|no\.?|number)?\s*[:.]?\s*([0-9]{6,18})\b',
    re.IGNORECASE,
)
# US EIN: 12-3456789 ; UK NINO and other tax-ids vary too much for a
# generic regex — leave to the LLM.
EIN_RE = re.compile(r'\b\d{2}-\d{7}\b')


def _luhn_valid(number: str) -> bool:
    digits = [int(d) for d in number if d.isdigit()]
    if len(digits) < 13:
        return False
    total = 0
    for i, d in enumerate(reversed(digits)):
        if i % 2 == 1:
            d *= 2
            if d > 9:
                d -= 9
        total += d
    return total % 10 == 0


def _emit(entities: list[dict], **kwargs: Any) -> None:
    """Append an entity row with source defaulting to 'regex'."""
    kwargs.setdefault("source", "regex")
    entities.append(kwargs)


def _regex_pass(text: str) -> list[dict]:
    """All regex matchers in one pass. Order matters for dedupe in the
    final merge — these run first so they win offset collisions."""
    entities: list[dict] = []
    for m in SSN_RE.finditer(text):
        _emit(entities, entity_type="national_id", entity_value=m.group(),
              start_offset=m.start(), end_offset=m.end(), confidence=0.95, is_pii=True)
    for m in CC_RE.finditer(text):
        if _luhn_valid(m.group()):
            _emit(entities, entity_type="credit_card", entity_value=m.group(),
                  start_offset=m.start(), end_offset=m.end(), confidence=0.95, is_pii=True)
    for m in EMAIL_RE.finditer(text):
        _emit(entities, entity_type="email", entity_value=m.group(),
              start_offset=m.start(), end_offset=m.end(), confidence=0.9, is_pii=True)
    for m in PHONE_RE.finditer(text):
        _emit(entities, entity_type="phone", entity_value=m.group(),
              start_offset=m.start(), end_offset=m.end(), confidence=0.85, is_pii=True)
    for m in DOB_RE.finditer(text):
        _emit(entities, entity_type="dob", entity_value=m.group(1),
              start_offset=m.start(1), end_offset=m.end(1), confidence=0.9, is_pii=True)
    for m in ICD10_RE.finditer(text):
        _emit(entities, entity_type="icd_code", entity_value=m.group(1),
              start_offset=m.start(1), end_offset=m.end(1), confidence=0.85, is_pii=False)
    for m in CPT_RE.finditer(text):
        # Filter obvious false positives: years and ZIP+4 fragments. CPT
        # codes are always 5 digits; we keep them only if surrounded by
        # CPT-context words.
        v = m.group(1)
        if 1900 <= int(v) <= 2099:
            continue
        ctx_start = max(0, m.start() - 40)
        ctx = text[ctx_start:m.start()].lower()
        if not any(kw in ctx for kw in ("cpt", "procedure", "service code", "hcpcs")):
            continue
        _emit(entities, entity_type="cpt_code", entity_value=v,
              start_offset=m.start(1), end_offset=m.end(1), confidence=0.8, is_pii=False)
    for m in AMOUNT_RE.finditer(text):
        _emit(entities, entity_type="amount",
              entity_value=f"{m.group('currency')}{m.group('amount')}",
              start_offset=m.start(), end_offset=m.end(), confidence=0.85, is_pii=False)
        # Also emit currency separately so reporting can group on it.
        _emit(entities, entity_type="currency", entity_value=m.group("currency"),
              start_offset=m.start("currency"), end_offset=m.end("currency"),
              confidence=0.95, is_pii=False)
    for m in ACCOUNT_RE.finditer(text):
        _emit(entities, entity_type="account_number", entity_value=m.group(1),
              start_offset=m.start(1), end_offset=m.end(1), confidence=0.85, is_pii=True)
    for m in EIN_RE.finditer(text):
        _emit(entities, entity_type="tax_id", entity_value=m.group(),
              start_offset=m.start(), end_offset=m.end(), confidence=0.9, is_pii=True)
    return entities


# Map SpaCy's pretrained label set to our ADR 0061 taxonomy. Labels
# not listed are dropped (we only persist what the consumers can use).
_SPACY_TYPE_MAP = {
    "PERSON": "name",
    "ORG":    "party_name",
    "GPE":    "jurisdiction",
    "LOC":    "address",
    "DATE":   "effective_date",
    "MONEY":  "amount",
    "PERCENT": "percent",
}


def _spacy_pass(text: str) -> list[dict]:
    try:
        from app.models.ner_model import extract_entities as _spacy_extract
    except Exception as e:  # noqa: BLE001
        log.warning("SpaCy NER unavailable: %s", e)
        return []
    raw = _spacy_extract(text) or []
    out: list[dict] = []
    for r in raw:
        # Existing SpaCy wrapper returns {entity_type, entity_value,
        # start_offset, end_offset, confidence, is_pii}. Map the type.
        src_type = str(r.get("entity_type", "")).upper()
        mapped = _SPACY_TYPE_MAP.get(src_type)
        if not mapped:
            continue
        out.append({
            "entity_type": mapped,
            "entity_value": r.get("entity_value", ""),
            "start_offset": int(r.get("start_offset", 0)),
            "end_offset":   int(r.get("end_offset", 0)),
            "confidence":   float(r.get("confidence", 0.7)),
            "is_pii":       mapped in {"name", "address"},
            "source":       "spacy",
        })
    return out


def _dedupe(entities: list[dict]) -> list[dict]:
    """Drop entities whose (start_offset, end_offset) span is already
    covered by a higher-priority source. Priority: regex > spacy > llm.
    Preserves the first occurrence per (start, end) range."""
    priority = {"regex": 0, "spacy": 1, "llm": 2, "manual": -1}
    # Sort: cheaper sources first, then longer spans first within source.
    entities = sorted(
        entities,
        key=lambda e: (priority.get(e.get("source", "spacy"), 9),
                       -(int(e["end_offset"]) - int(e["start_offset"]))),
    )
    seen: list[tuple[int, int]] = []
    out: list[dict] = []
    for e in entities:
        s, t = int(e["start_offset"]), int(e["end_offset"])
        if any(not (t <= a or s >= b) for (a, b) in seen):
            continue
        seen.append((s, t))
        out.append(e)
    return out


def _build_completed_envelope(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    entities: list[dict],
    pii_count: int,
    entity_types: list[str],
    sources_used: list[str],
    correlation_id: str,
) -> dict:
    return {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "dms.intelligence.ner",
        "type": NER_COMPLETED_SUBJECT,
        "subject": f"version/{version_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "entity_count": len(entities),
            "pii_count": pii_count,
            "entity_types": entity_types,
            "sources_used": sources_used,
            "model_version": "ensemble-v2-regex+spacy+llm",
        },
    }


async def _run_async(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    text: str,
) -> list[dict]:
    """The actual NER pipeline, async so the LLM call can await."""
    regex_hits = _regex_pass(text)
    spacy_hits = _spacy_pass(text)
    cfg = await load_ner_config(tenant_id)
    llm_hits = await extract_via_llm(text, cfg) if cfg.enabled else []
    return _dedupe(regex_hits + spacy_hits + llm_hits)


@celery_app.task(
    name="app.tasks.ner.detect_entities",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=120,
    time_limit=150,
)
def detect_entities(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    text: str,
    event_id: str = "",
    correlation_id: str = "",
):
    start = time.monotonic()

    try:
        asyncio.run(mark_enqueued(
            tenant_id=tenant_id,
            consumer=CONSUMER,
            event_id=event_id,
            document_id=document_id,
            version_id=version_id,
        ))

        entities = asyncio.run(_run_async(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            text=text,
        ))

        pii_count = sum(1 for e in entities if e.get("is_pii"))
        entity_types = sorted({e["entity_type"] for e in entities})
        sources_used = sorted({e.get("source", "spacy") for e in entities})

        asyncio.run(replace_entities(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            entities=entities,
        ))

        for e in entities:
            ner_entities_total.labels(
                entity_type=e["entity_type"],
                is_pii=str(bool(e.get("is_pii"))).lower(),
            ).inc()

        envelope = _build_completed_envelope(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            entities=entities,
            pii_count=pii_count,
            entity_types=entity_types,
            sources_used=sources_used,
            correlation_id=correlation_id,
        )
        try:
            asyncio.run(publish_cloudevent(
                NER_COMPLETED_SUBJECT, envelope, correlation_id=correlation_id
            ))
        except Exception:
            log.exception("ner_completed publish failed")

        asyncio.run(mark_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ))
        ner_documents_total.labels(status="completed").inc()
        ner_duration_seconds.observe(time.monotonic() - start)

        log.info(
            "ner.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "entity_count": len(entities),
                "pii_count": pii_count,
                "sources": ",".join(sources_used),
                "attempt": self.request.retries + 1,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "entity_count": len(entities),
            "pii_count": pii_count,
            "entity_types_found": entity_types,
            "sources_used": sources_used,
            "processing_time_ms": int((time.monotonic() - start) * 1000),
        }
    except Exception as exc:
        is_terminal = self.request.retries >= settings.ocr_max_retries
        if is_terminal:
            reason = _classify_error(exc)
            ner_documents_total.labels(status="failed").inc()
            ner_dlq_total.labels(reason=reason).inc()
            try:
                asyncio.run(publish_dlq(
                    consumer=CONSUMER,
                    reason=reason,
                    tenant_id=tenant_id,
                    document_id=document_id,
                    version_id=version_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=self.request.retries + 1,
                    correlation_id=correlation_id,
                ))
                asyncio.run(mark_failed(
                    tenant_id=tenant_id,
                    consumer=CONSUMER,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                ))
            except Exception:
                log.exception("ner terminal DLQ publish failed")
        raise
