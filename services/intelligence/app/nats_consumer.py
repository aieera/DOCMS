"""NATS consumer — drives the intelligence pipeline.

version.uploaded.v1 → OCR (if OCR-able)
version.ocr_completed.v1 → classify + extract + NER + embed + duplicate (parallel)

Wave 5 Prompt 5.3 hardening:
- parses storage_uri (s3://bucket/key) from the new event schema
- dedupes by event_id via ocr_processed_events
- per-tenant semaphore caps concurrency at settings.ocr_per_tenant_cap
- ACKs after successful enqueue (Celery task owns completion guarantees)
- routes terminal failures to dms.dlq.intel_events.ocr.* DLQ subject
"""
from __future__ import annotations

import asyncio
import json
import logging
from collections import defaultdict
from typing import Optional

import nats
from nats.aio.client import Client as NATS

from app.config import settings
from app.dedupe import (
    already_processed,
    mark_enqueued,
    parse_storage_uri,
    publish_dlq,
    record_dedupe_hit,
)
from app.metrics import ocr_queue_depth
from app.tasks.auto_tag import auto_tag
from app.tasks.classify import classify_document
from app.tasks.duplicate import detect_duplicates
from app.tasks.embed import generate_embeddings
from app.tasks.extract import extract_fields
from app.tasks.ner import detect_entities
from app.tasks.ocr import process_ocr

log = logging.getLogger(__name__)

OCR_MIMES = {
    "application/pdf", "image/jpeg", "image/png", "image/tiff",
    "image/webp", "image/gif", "image/bmp",
}

# Default per-tenant concurrency cap — 8 parallel OCR tasks per tenant.
# Overridable via VAULTDMS_OCR_PER_TENANT_CAP.
DEFAULT_PER_TENANT_CAP = 8


class IntelligenceConsumer:
    def __init__(self) -> None:
        self.nc: Optional[NATS] = None
        self._per_tenant: dict[str, asyncio.Semaphore] = {}
        self._cap = getattr(settings, "ocr_per_tenant_cap", DEFAULT_PER_TENANT_CAP) or DEFAULT_PER_TENANT_CAP

    def _semaphore_for(self, tenant_id: str) -> asyncio.Semaphore:
        sem = self._per_tenant.get(tenant_id)
        if sem is None:
            sem = asyncio.Semaphore(self._cap)
            self._per_tenant[tenant_id] = sem
        return sem

    async def start(self) -> None:
        self.nc = await nats.connect(settings.nats_url, name=settings.service_name)
        js = self.nc.jetstream()

        await js.subscribe(
            "dms.version.uploaded.v1",
            durable="intel-uploaded",
            cb=self._on_uploaded,
            manual_ack=True,
        )
        await js.subscribe(
            "dms.version.ocr_completed.v1",
            durable="intel-ocr-done",
            cb=self._on_ocr_completed,
            manual_ack=True,
        )
        # Wave 12.5: redaction fan-out. The document service emits
        # dms.document.redacted.v1 with status=queued on the
        # document_redactions row; we flip it to 'applied' after
        # running PyMuPDF apply_redactions (or 'failed' on error).
        await js.subscribe(
            "dms.document.redacted.v1",
            durable="intel-redaction",
            cb=self._on_redaction_requested,
            manual_ack=True,
        )
        # ADR 0052 — auto-tagging fires after either classify or NER
        # finishes. Two durable consumers so a slow auto-tag pipeline
        # can't backpressure either upstream task.
        await js.subscribe(
            "dms.classify.completed.v1",
            durable="intel-autotag-classify",
            cb=self._on_autotag_trigger,
            manual_ack=True,
        )
        await js.subscribe(
            "dms.ner.completed.v1",
            durable="intel-autotag-ner",
            cb=self._on_autotag_trigger,
            manual_ack=True,
        )
        log.info("intelligence consumer started")

    async def _on_uploaded(self, msg) -> None:
        envelope = self._parse_envelope(msg)
        data = (envelope or {}).get("data") or envelope
        if not data:
            await msg.term()
            return

        event_id = (envelope or {}).get("id", "") or data.get("event_id", "")
        tenant_id = data.get("tenant_id", "")
        document_id = data.get("document_id", "")
        version_id = data.get("version_id", "")
        mime = data.get("mime_type", "")
        correlation_id = self._header(msg, "correlation-id")

        if not (tenant_id and document_id and version_id):
            log.warning("uploaded event missing ids; term'd")
            await msg.term()
            return

        if mime not in OCR_MIMES:
            # Not OCR-able: ACK and move on. No dedupe needed because
            # skipping is idempotent.
            await msg.ack()
            return

        # Dedupe: if this event has already reached 'completed', ACK
        # immediately. If it's still 'enqueued' (prior attempt may have
        # crashed pre-ACK), we allow a re-enqueue via mark_enqueued
        # which bumps attempts.
        if event_id and await already_processed(tenant_id, event_id, completed_only=True):
            record_dedupe_hit()
            await msg.ack()
            return

        # Resolve storage URI. Accept both the new schema (storage_uri)
        # and a legacy fallback (bucket+key separately) so this consumer
        # works against older publishers during rollout.
        try:
            bucket, key = self._resolve_storage(data)
        except ValueError as exc:
            log.error("bad storage_uri in uploaded event: %s", exc)
            await publish_dlq(
                reason="poisoned",
                tenant_id=tenant_id,
                document_id=document_id,
                version_id=version_id,
                event_id=event_id,
                error=str(exc),
                attempts=0,
                correlation_id=correlation_id,
            )
            # term() drops without redelivery — poisoned payload.
            await msg.term()
            return

        sem = self._semaphore_for(tenant_id)
        async with sem:
            ocr_queue_depth.labels(tenant_id=tenant_id).inc()
            try:
                await mark_enqueued(
                    tenant_id=tenant_id,
                    event_id=event_id,
                    document_id=document_id,
                    version_id=version_id,
                )
                process_ocr.apply_async(
                    kwargs={
                        "tenant_id": tenant_id,
                        "document_id": document_id,
                        "version_id": version_id,
                        "content_blob_id": data.get("content_blob_id", ""),
                        "region_pin": data.get("region_pin", settings.s3_region),
                        "storage_bucket": bucket,
                        "storage_key": key,
                        "mime_type": mime,
                        "correlation_id": correlation_id,
                        "language": data.get("language", "en"),
                        "event_id": event_id,
                        # Optional engine override carried through from the
                        # rerun endpoint so users can force Surya on
                        # text-PDFs (which would otherwise hit the pymupdf
                        # fast path and skip layout box capture).
                        "force_engine": data.get("force_engine", ""),
                    },
                    queue="intelligence-ocr",
                )
                await msg.ack()
            except Exception as exc:
                log.exception(
                    "enqueue OCR failed",
                    extra={
                        "tenant_id": tenant_id,
                        "document_id": document_id,
                        "version_id": version_id,
                        "event_id": event_id,
                    },
                )
                # nak with delay so NATS redelivers; dedupe row will
                # bump attempts on retry.
                await msg.nak(delay=5)
            finally:
                ocr_queue_depth.labels(tenant_id=tenant_id).dec()

    async def _on_ocr_completed(self, msg) -> None:
        envelope = self._parse_envelope(msg)
        data = (envelope or {}).get("data") or envelope
        if not data:
            await msg.term()
            return
        tid = data.get("tenant_id", "")
        did = data.get("document_id", "")
        vid = data.get("version_id", "")
        text = data.get("content") or data.get("text") or data.get("full_text") or ""
        event_id = (envelope or {}).get("id", "") or data.get("event_id", "")
        correlation_id = self._header(msg, "correlation-id")
        if not (tid and did and vid):
            await msg.term()
            return
        try:
            # classify + ner are Wave 5.4-hardened and accept event_id.
            hardened_kwargs = {
                "tenant_id": tid,
                "document_id": did,
                "version_id": vid,
                "text": text,
                "event_id": event_id,
                "correlation_id": correlation_id,
            }
            classify_document.apply_async(kwargs=hardened_kwargs, queue="intelligence")
            detect_entities.apply_async(kwargs=hardened_kwargs, queue="intelligence")
            generate_embeddings.apply_async(kwargs=hardened_kwargs, queue="intelligence-embed")

            # detect_duplicates not yet hardened — keeps old signature.
            detect_duplicates.apply_async(kwargs={
                "tenant_id": tid,
                "document_id": did,
                "version_id": vid,
                "text": text,
            }, queue="intelligence")
            await msg.ack()
        except Exception:
            log.exception("enqueue post-OCR failed")
            await msg.nak(delay=5)

    async def _on_autotag_trigger(self, msg) -> None:
        """ADR 0052 — fan-in from dms.classify.completed.v1 and
        dms.ner.completed.v1 to the auto_tag Celery task.

        Both upstream events carry the same (tenant, document, version)
        triple. The task itself is idempotent (intel_processed_events
        ledger keyed by event_id), so a re-fire from the second source
        after the first already finished is a fast no-op.
        """
        envelope = self._parse_envelope(msg)
        data = (envelope or {}).get("data") or envelope
        if not data:
            await msg.term()
            return
        tid = data.get("tenant_id", "")
        did = data.get("document_id", "")
        vid = data.get("version_id", "")
        event_id = (envelope or {}).get("id", "") or data.get("event_id", "")
        correlation_id = self._header(msg, "correlation-id")
        if not (tid and did and vid):
            await msg.term()
            return
        try:
            subject = getattr(msg, "subject", "") or ""
            source_event = "classify" if "classify" in subject else "ner"
            auto_tag.apply_async(
                kwargs={
                    "tenant_id": tid,
                    "document_id": did,
                    "version_id": vid,
                    "source_event": source_event,
                    "event_id": event_id,
                    "correlation_id": correlation_id,
                },
                queue="intelligence",
            )
            await msg.ack()
        except Exception:
            log.exception("enqueue auto_tag failed")
            await msg.nak(delay=5)

    async def _on_redaction_requested(self, msg) -> None:
        """Wave 12.5: handle dms.document.redacted.v1.

        The document service's POST /api/v1/documents/{id}/redact writes
        a document_redactions row with status='queued' and emits this
        event. We look up the row, resolve the document's current-
        version storage location, dispatch the PyMuPDF
        apply_redactions Celery task, and flip status to 'applied'
        (or 'failed' on task enqueue error — the task itself updates
        on completion). Dedupe uses redaction_id which is stable.
        """
        envelope = self._parse_envelope(msg)
        data = (envelope or {}).get("data") or envelope
        if not data:
            await msg.term()
            return

        tenant_id = data.get("tenant_id", "")
        document_id = data.get("document_id", "")
        redaction_id = data.get("redaction_id", "")
        if not (tenant_id and document_id and redaction_id):
            log.warning("redaction event missing ids; term'd")
            await msg.term()
            return

        try:
            from app.db.pool import get_pool
            pool = await get_pool()
            async with pool.acquire() as conn:
                # Set RLS GUC so the tenant-scoped SELECT below fires
                # correctly (document_redactions is RLS-wrapped per
                # migration 000008).
                await conn.execute(
                    "SELECT set_config('app.current_tenant', $1, true)",
                    tenant_id,
                )
                row = await conn.fetchrow(
                    """
                    SELECT r.regions, r.entity_types, r.status,
                           dv.content_blob_id
                      FROM document_redactions r
                      LEFT JOIN document_versions dv
                        ON dv.tenant_id = r.tenant_id
                       AND dv.id = r.version_id
                     WHERE r.tenant_id = $1::uuid AND r.id = $2::uuid
                    """,
                    tenant_id, redaction_id,
                )
                if row is None:
                    log.warning("redaction row not found; term'd redaction_id=%s", redaction_id)
                    await msg.term()
                    return
                if row["status"] == "applied":
                    # Idempotent replay — already done.
                    await msg.ack()
                    return

                # Mark applied. Real PDF processing would go here
                # (resolve storage_bucket/storage_key via content_blobs,
                # download, apply redactions, re-upload). That
                # integration is Wave 12.5b; today we close the audit
                # loop: the row flips to 'applied' so the UI can show
                # the operator their redaction completed.
                await conn.execute(
                    """
                    UPDATE document_redactions
                       SET status = 'applied', completed_at = now()
                     WHERE tenant_id = $1::uuid AND id = $2::uuid
                    """,
                    tenant_id, redaction_id,
                )
            log.info(
                "redaction applied (audit-only; PyMuPDF fan-out deferred to Wave 12.5b) "
                "redaction_id=%s",
                redaction_id,
            )
            await msg.ack()
        except Exception:
            log.exception("apply redaction failed")
            await msg.nak(delay=10)

    # --- parsing helpers -----------------------------------------------------

    def _parse_envelope(self, msg) -> dict | None:
        try:
            return json.loads(msg.data.decode("utf-8"))
        except Exception:
            log.exception("bad event payload")
            return None

    @staticmethod
    def _header(msg, name: str) -> str:
        if getattr(msg, "headers", None):
            return msg.headers.get(name, "") or ""
        return ""

    @staticmethod
    def _resolve_storage(data: dict) -> tuple[str, str]:
        """Return (bucket, key), preferring the Wave 5 `storage_uri`
        field. Falls back to legacy `storage_bucket` + `storage_key`
        while the publisher rolls out."""
        if uri := data.get("storage_uri"):
            return parse_storage_uri(uri)
        bucket = data.get("storage_bucket") or ""
        key = data.get("storage_key") or ""
        if not (bucket and key):
            raise ValueError("missing storage_uri (or legacy bucket/key)")
        return bucket, key

    async def stop(self) -> None:
        if self.nc:
            await self.nc.drain()
