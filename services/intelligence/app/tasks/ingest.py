"""WS3 pre-commit ingestion — OCR + business-key extraction against a staged
blob (no version yet).

Dispatched off dms.ingestion.received.v1 (emitted by the document service's
POST /ingest). Unlike process_ocr — which runs AFTER a version exists and writes
ocr_results keyed by version_id — this task runs against the blob ref directly
and writes its results onto the ingestion_item (+ an ingestion_ocr sidecar).
On completion it emits dms.ingestion.processed.v1, which the document service
consumes to start the IngestAndRoute routing workflow.

Reuses the shared OCR engine core (ocr.ocr_file) and the field-extraction core
(extract._extract_fields_for / extraction_profiles) so a staged read is parsed
identically to the version path. The "external key" is the extracted field
flagged is_doc_number for the document class (e.g. invoice_number); its
confidence is the routing confidence.
"""
from __future__ import annotations

import asyncio
import logging
import os
import shutil
import tempfile
import uuid
from datetime import datetime, timezone

from app.config import settings
from app.events.publisher import publish_cloudevent
from app.extraction_profiles import DEFAULT_PROFILES, normalize_class, resolve_profile
from app.persist import (
    get_ingestion_status,
    mark_ingestion_status,
    write_ingestion_result,
)
from app.tasks.extract import _extract_fields_for, _regex_extract
from app.tasks.ocr import (
    OCR_MIMES,
    TerminalOCRError,
    _decrypt_src_if_envelope,
    _is_text_mime,
    _s3,
    ocr_file,
)
from app.worker import celery_app
from app.events.subjects import INGESTION_PROCESSED_SUBJECT

log = logging.getLogger(__name__)



def _best_doc_number(fields: list[dict]) -> tuple[str, float]:
    """Pick the highest-confidence is_doc_number field — the business key the
    routing step matches on."""
    best_key, best_conf = "", 0.0
    for f in fields:
        if f.get("is_doc_number") and f.get("value") and float(f["confidence"]) > best_conf:
            best_key, best_conf = str(f["value"]), float(f["confidence"])
    return best_key, best_conf


async def _extract_external_key(tenant_id: str, document_class: str, text: str) -> tuple[str, float]:
    """Resolve the external key + its confidence from OCR text.

    With a class hint: run the full (regex + LLM-fallback) extraction for that
    class. Without one: a cheap regex-only sweep across the default profiles so
    we don't fire an LLM call per class.
    """
    if not text:
        return "", 0.0
    if document_class:
        specs = await resolve_profile(tenant_id, document_class)
        if not specs:
            return "", 0.0
        fields, _ = _extract_fields_for(text, specs, normalize_class(document_class), tenant_id)
        return _best_doc_number(fields)

    best_key, best_conf = "", 0.0
    for _cls, specs in DEFAULT_PROFILES.items():
        by_key = _regex_extract(text, specs)
        for fs in specs:
            if fs.is_doc_number and fs.key in by_key:
                v = by_key[fs.key]
                if v["value"] and float(v["confidence"]) > best_conf:
                    best_key, best_conf = str(v["value"]), float(v["confidence"])
    return best_key, best_conf


def _build_processed_envelope(
    *, tenant_id: str, ingestion_item_id: str, external_key: str,
    confidence: float, document_class: str, correlation_id: str,
) -> dict:
    return {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "dms.intelligence.ingest",
        "type": INGESTION_PROCESSED_SUBJECT,
        "subject": f"ingestion/{ingestion_item_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "ingestion_item_id": ingestion_item_id,
            "tenant_id": tenant_id,
            "extracted_external_key": external_key,
            "confidence": round(float(confidence), 3),
            "document_class": document_class,
        },
    }


@celery_app.task(
    name="app.tasks.ingest.process_ingestion",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    throws=(TerminalOCRError,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=1800,
    time_limit=1830,
)
def process_ingestion(
    self,
    tenant_id: str,
    ingestion_item_id: str,
    content_blob_id: str,
    storage_bucket: str,
    storage_key: str,
    blob_checksum: str = "",
    mime_type: str = "",
    document_class: str = "",
    target_customer_ref: str = "",
    region_pin: str = "",
    event_id: str = "",
    correlation_id: str = "",
):
    """OCR + extract a staged blob, write results onto the ingestion_item, and
    emit dms.ingestion.processed.v1. Idempotent on ingestion_item.id: a
    re-delivery on an already-processed/terminal item is a no-op."""
    # Idempotency: don't re-OCR an item that already advanced.
    status = asyncio.run(get_ingestion_status(
        tenant_id=tenant_id, ingestion_item_id=ingestion_item_id))
    if status in ("processed", "routed", "needs_review", "committed", "rejected"):
        return {"status": "skipped", "reason": f"already_{status}",
                "ingestion_item_id": ingestion_item_id}
    if status == "":
        # Item vanished (or never existed) — nothing to do, don't retry.
        return {"status": "skipped", "reason": "missing",
                "ingestion_item_id": ingestion_item_id}

    asyncio.run(mark_ingestion_status(
        tenant_id=tenant_id, ingestion_item_id=ingestion_item_id, status="ocr_running"))

    workdir = tempfile.mkdtemp(prefix="ingest-")
    src = os.path.join(workdir, "source")
    try:
        # OCR-able? Non-OCR mimes yield no text → empty key → review (handled
        # by the routing step), rather than crashing the engine on raw bytes.
        if mime_type in OCR_MIMES or _is_text_mime(mime_type):
            _s3().download_file(storage_bucket, storage_key, src)
            _decrypt_src_if_envelope(src, tenant_id, content_blob_id)
            pages, full_text, engine, avg_conf = ocr_file(src, mime_type, "en")
        else:
            pages, full_text, engine, avg_conf = [], "", "none", 0.0
            log.info("ingestion: non-OCR mime %s; staging with empty text", mime_type)

        external_key, key_conf = asyncio.run(
            _extract_external_key(tenant_id, document_class, full_text))

        asyncio.run(write_ingestion_result(
            tenant_id=tenant_id,
            ingestion_item_id=ingestion_item_id,
            full_text=full_text,
            page_count=len(pages),
            confidence_avg=round(avg_conf, 3),
            engine=engine,
            external_key=external_key,
            confidence=key_conf,
        ))

        try:
            asyncio.run(publish_cloudevent(
                INGESTION_PROCESSED_SUBJECT,
                _build_processed_envelope(
                    tenant_id=tenant_id,
                    ingestion_item_id=ingestion_item_id,
                    external_key=external_key,
                    confidence=key_conf,
                    document_class=document_class,
                    correlation_id=correlation_id,
                ),
                correlation_id=correlation_id,
            ))
        except Exception:
            # The document service also reconciles via the item status, but a
            # missed event means routing won't auto-start — surface it loudly.
            log.exception("ingestion.processed publish failed for %s", ingestion_item_id)

        log.info(
            "ingestion.processed",
            extra={
                "tenant_id": tenant_id,
                "ingestion_item_id": ingestion_item_id,
                "external_key": external_key,
                "confidence": key_conf,
                "page_count": len(pages),
                "engine": engine,
            },
        )
        return {
            "status": "processed",
            "ingestion_item_id": ingestion_item_id,
            "extracted_external_key": external_key,
            "confidence": key_conf,
            "page_count": len(pages),
        }
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


# settings is imported for parity with the other tasks (future per-tenant caps);
# referenced here to keep linters from flagging an unused import.
_ = settings
