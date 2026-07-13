"""Structured field extraction — per-class profiles, regex first, LLM fallback.

Dispatched off dms.classify.completed.v1 (the extract needs the document_class
classify produces). For each field in the (tenant, class) profile it tries the
labelled-field regexes; when regex coverage is thin it asks the LLM for the
missing fields. Results land in `extracted_fields` with PER-FIELD confidence,
the key business fields are mirrored onto documents.custom_metadata (so routing
+ search can read them), and a dms.version.fields_extracted.v1 event lets the
search indexer fold them into OpenSearch.
"""
from __future__ import annotations

import asyncio
import json
import logging
import re
import time
import uuid
from datetime import datetime, timezone

from app.config import settings
from app.events.publisher import publish_cloudevent
from app.extraction_profiles import FieldSpec, normalize_class, resolve_profile
from app.persist import (
    load_ocr_text,
    merge_custom_metadata,
    replace_extracted_fields,
)
from app.processing_stages import record_stage
from app.worker import celery_app
from app.events.subjects import FIELDS_EXTRACTED_SUBJECT

log = logging.getLogger(__name__)



def _regex_extract(text: str, specs: list[FieldSpec]) -> dict[str, dict]:
    """Run each field's regexes; return {field_key: {value, confidence,
    method, is_doc_number}} for the ones that matched."""
    out: dict[str, dict] = {}
    for fs in specs:
        for pat in fs.patterns:
            m = re.search(pat, text, re.IGNORECASE)
            if m and m.group(1).strip():
                out[fs.key] = {
                    "value": m.group(1).strip(),
                    "confidence": settings.extraction_regex_confidence,
                    "method": "regex",
                    "is_doc_number": fs.is_doc_number,
                }
                break
    return out


def _llm_extract(text: str, field_keys: list[str], document_class: str, tenant_id: str) -> dict:
    """Ask the LLM for the named fields. Returns {field_key: value}. Best
    effort — any failure returns {} so regex results still flow."""
    from app import llm_gateway
    prompt = (
        f"Extract these fields from the {document_class} text. "
        f"Return ONLY a JSON object with these keys (use null when a field is "
        f"absent): {', '.join(field_keys)}.\n\n"
        f"Text:\n{text[:4000]}"
    )
    resp = llm_gateway.completion(
        tenant_id=tenant_id,
        messages=[{"role": "user", "content": prompt}],
        temperature=0.0,
        max_tokens=800,
    )
    content = resp.get("content", "") if isinstance(resp, dict) else ""
    try:
        return json.loads(content)
    except (json.JSONDecodeError, TypeError):
        start = content.find("{")
        end = content.rfind("}") + 1
        if start >= 0 and end > start:
            try:
                return json.loads(content[start:end])
            except json.JSONDecodeError:
                return {}
        return {}


def _extract_fields_for(text: str, specs: list[FieldSpec], document_class: str, tenant_id: str) -> tuple[list[dict], str]:
    """Produce the per-field extraction list + the overall method label."""
    by_key = _regex_extract(text, specs)
    coverage = len(by_key) / len(specs) if specs else 0.0
    used_llm = False

    if coverage < settings.extraction_llm_fallback_threshold:
        missing = [fs.key for fs in specs if fs.key not in by_key]
        if missing:
            try:
                llm_vals = _llm_extract(text, missing, document_class, tenant_id)
                used_llm = True
                doc_number_keys = {fs.key for fs in specs if fs.is_doc_number}
                for fs in specs:
                    if fs.key in by_key:
                        continue
                    val = llm_vals.get(fs.key)
                    if val in (None, "", []):
                        continue
                    by_key[fs.key] = {
                        "value": str(val).strip(),
                        "confidence": settings.extraction_llm_confidence,
                        "method": "llm",
                        "is_doc_number": fs.key in doc_number_keys,
                    }
            except Exception as e:  # noqa: BLE001
                log.warning("LLM extract failed for class=%s: %s", document_class, e)

    fields = [
        {
            "field_key": key,
            "value": v["value"],
            "confidence": round(float(v["confidence"]), 3),
            "method": v["method"],
            "is_doc_number": v["is_doc_number"],
        }
        for key, v in by_key.items()
    ]
    methods = {f["method"] for f in fields}
    if not fields:
        overall = "none"
    elif methods == {"regex"}:
        overall = "regex"
    elif methods == {"llm"}:
        overall = "llm"
    else:
        overall = "hybrid"
    return fields, overall


def _build_event(*, tenant_id, document_id, version_id, document_class,
                 fields, document_number, correlation_id) -> dict:
    return {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "dms.intelligence.extract",
        "type": FIELDS_EXTRACTED_SUBJECT,
        "subject": f"version/{version_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "document_class": document_class,
            "document_number": document_number,
            "fields": [
                {"field_key": f["field_key"], "value": f["value"], "confidence": f["confidence"]}
                for f in fields
            ],
        },
    }


@celery_app.task(
    name="app.tasks.extract.extract_fields",
    bind=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 2},
    retry_backoff=True,
)
def extract_fields(self, tenant_id: str, document_id: str, version_id: str,
                   document_class: str, text: str = "",
                   event_id: str = "", correlation_id: str = ""):
    start = time.monotonic()

    if not settings.extraction_enabled:
        return {"status": "disabled", "version_id": version_id}

    normalized = normalize_class(document_class)
    specs = asyncio.run(resolve_profile(tenant_id, document_class))
    if not specs:
        # No profile for this class (or tenant disabled it) — nothing to pull.
        record_stage(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            stage="extract", status="skipped", failure_reason="no_profile",
            failure_detail=f"no extraction profile for class '{normalized}'",
        )
        return {"status": "skipped", "reason": "no_profile",
                "document_class": normalized, "version_id": version_id}

    record_stage(
        tenant_id=tenant_id, document_id=document_id, version_id=version_id,
        stage="extract", status="running",
    )

    # The classify.completed event doesn't carry OCR text; pull it from
    # ocr_results unless the caller passed it (tests / direct invocation).
    if not text:
        text = asyncio.run(load_ocr_text(tenant_id=tenant_id, version_id=version_id))
    if not text:
        record_stage(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            stage="extract", status="skipped", failure_reason="no_text",
            failure_detail="no OCR text available for this version",
        )
        return {"status": "skipped", "reason": "no_text", "version_id": version_id}

    try:
        fields, method = _extract_fields_for(text, specs, normalized, tenant_id)

        # Persist the per-field rows (with per-field confidence).
        asyncio.run(replace_extracted_fields(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            document_class=normalized, fields=fields,
        ))

        # Mirror the key business fields onto custom_metadata. `document_number`
        # is the canonical, class-agnostic business key the routing step reads.
        mirror: dict[str, str] = {}
        document_number = ""
        for f in fields:
            mirror[f["field_key"]] = f["value"]
            if f["is_doc_number"] and not document_number:
                document_number = f["value"]
        if document_number:
            mirror["document_number"] = document_number
        asyncio.run(merge_custom_metadata(
            tenant_id=tenant_id, document_id=document_id, values=mirror,
        ))

        # Fan the result out to the search indexer (OpenSearch custom_metadata).
        try:
            asyncio.run(publish_cloudevent(
                FIELDS_EXTRACTED_SUBJECT,
                _build_event(
                    tenant_id=tenant_id, document_id=document_id, version_id=version_id,
                    document_class=normalized, fields=fields,
                    document_number=document_number, correlation_id=correlation_id,
                ),
                correlation_id=correlation_id,
            ))
        except Exception:
            log.exception("fields_extracted publish failed")

        record_stage(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            stage="extract", status="completed",
        )
        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "extract.completed",
            extra={
                "tenant_id": tenant_id, "document_id": document_id,
                "version_id": version_id, "document_class": normalized,
                "field_count": len(fields), "method": method,
                "document_number": document_number,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "document_class": normalized,
            "fields": fields,
            "document_number": document_number,
            "method": method,
            "processing_time_ms": elapsed_ms,
        }
    except Exception as exc:
        if self.request.retries >= 2:
            record_stage(
                tenant_id=tenant_id, document_id=document_id, version_id=version_id,
                stage="extract", status="failed", failure_reason="engine_error",
                failure_detail=str(exc),
            )
        raise
