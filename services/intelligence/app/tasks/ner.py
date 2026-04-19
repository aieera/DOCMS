"""Named Entity Recognition — SpaCy + regex PII detection.

Wave 5 Prompt 5.4 hardening mirrors classify: acks_late, 3 jittered
retries, dedupe, DB persistence, NATS emit on success, DLQ on terminal
failure.
"""
from __future__ import annotations

import asyncio
import logging
import re
import time
import uuid
from datetime import datetime, timezone

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
from app.worker import celery_app

log = logging.getLogger(__name__)

NER_COMPLETED_SUBJECT = "dms.ner.completed.v1"
CONSUMER = "ner"

SSN_RE = re.compile(r'\b\d{3}-\d{2}-\d{4}\b')
CC_RE = re.compile(r'\b(?:\d{4}[\s-]?){3}\d{4}\b')
EMAIL_RE = re.compile(r'\b[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b')
PHONE_RE = re.compile(r'\b(?:\+\d{1,3}[\s-]?)?\(?\d{3}\)?[\s.-]?\d{3}[\s.-]?\d{4}\b')
DOB_RE = re.compile(
    r'(?:DOB|Date of Birth|Born)[\s:]*(\d{1,2}[/-]\d{1,2}[/-]\d{2,4})',
    re.IGNORECASE,
)


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


def _regex_pii(text: str) -> list[dict]:
    entities: list[dict] = []
    for m in SSN_RE.finditer(text):
        entities.append({"entity_type": "SSN", "entity_value": m.group(), "start_offset": m.start(),
                         "end_offset": m.end(), "confidence": 0.95, "is_pii": True})
    for m in CC_RE.finditer(text):
        if _luhn_valid(m.group()):
            entities.append({"entity_type": "CREDIT_CARD", "entity_value": m.group(),
                             "start_offset": m.start(), "end_offset": m.end(),
                             "confidence": 0.95, "is_pii": True})
    for m in EMAIL_RE.finditer(text):
        entities.append({"entity_type": "EMAIL", "entity_value": m.group(), "start_offset": m.start(),
                         "end_offset": m.end(), "confidence": 0.9, "is_pii": True})
    for m in PHONE_RE.finditer(text):
        entities.append({"entity_type": "PHONE", "entity_value": m.group(), "start_offset": m.start(),
                         "end_offset": m.end(), "confidence": 0.85, "is_pii": True})
    for m in DOB_RE.finditer(text):
        entities.append({"entity_type": "DATE_OF_BIRTH", "entity_value": m.group(1),
                         "start_offset": m.start(1), "end_offset": m.end(1),
                         "confidence": 0.9, "is_pii": True})
    return entities


def _build_completed_envelope(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    entities: list[dict],
    pii_count: int,
    entity_types: list[str],
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
            "model_version": "spacy-en_core_web_sm+regex-v1",
            # entities themselves can be large; consumers fetch from
            # document_entities table keyed by version_id.
        },
    }


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

        entities = _regex_pii(text)
        try:
            from app.models.ner_model import extract_entities as spacy_extract
            entities.extend(spacy_extract(text))
        except Exception as e:
            log.warning("SpaCy NER failed: %s", e)

        pii_count = sum(1 for e in entities if e.get("is_pii"))
        entity_types = sorted({e["entity_type"] for e in entities})

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
