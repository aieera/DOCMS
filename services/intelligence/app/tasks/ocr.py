"""OCR task — extracts text from PDFs and images via Surya, persists the
per-page results to Postgres, and publishes a `dms.version.ocr_completed.v1`
CloudEvent so the search service can fold the text into OpenSearch and the
rest of the intelligence pipeline (classify/NER/embed/duplicate) can fan out.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import random
import shutil
import signal
import tempfile
import time
import uuid
from datetime import datetime, timezone
from contextlib import contextmanager

import fitz
from PIL import Image

from app.config import settings
from app.db.pool import get_pool
from app.dedupe import mark_completed, mark_failed, publish_dlq
from app.events.publisher import publish_cloudevent
from app.metrics import (
    ocr_documents_total,
    ocr_duration_seconds,
    ocr_errors_total,
    ocr_pages_processed_total,
    ocr_pages_total,
    ocr_processing_seconds,
    ocr_publish_failed_total,
)
from app.models.ocr_model import surya_ocr_page
from app.worker import celery_app

log = logging.getLogger(__name__)

OCR_MIMES = {
    "application/pdf", "image/jpeg", "image/png", "image/tiff",
    "image/webp", "image/gif", "image/bmp",
}

OCR_COMPLETED_SUBJECT = "dms.version.ocr_completed.v1"

# Cap the full_text payload to 10 MiB so a single pathological PDF cannot
# blow up the NATS message or the downstream search index doc.
MAX_FULL_TEXT_BYTES = 10 * 1024 * 1024


def _s3():
    import boto3
    from botocore.client import Config
    return boto3.client(
        "s3", endpoint_url=settings.s3_endpoint,
        aws_access_key_id=settings.s3_access_key or None,
        aws_secret_access_key=settings.s3_secret_key or None,
        use_ssl=settings.s3_use_ssl,
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
    )


def _extract_text_pdf(path: str) -> list[dict]:
    doc = fitz.open(path)
    pages = []
    for i, page in enumerate(doc):
        t0 = time.perf_counter()
        text = page.get_text("text").strip()
        words = _pymupdf_word_boxes(page, text) if text else []
        # Wrap with page dimensions so the frontend can scale word
        # rectangles against the rendered PDF without a separate dim
        # column. Empty wrapper when no words to keep the shape stable.
        word_boxes_payload: dict | list = (
            {
                "page_width": float(page.rect.width),
                "page_height": float(page.rect.height),
                "words": words,
            }
            if words else []
        )
        dur = time.perf_counter() - t0
        pages.append({
            "page_number": i + 1,
            "text": text,
            "confidence": 1.0 if text else 0.0,
            "method": "pymupdf" if text else "none",
            "boxes": [],
            "word_boxes": word_boxes_payload,
            "processing_time_ms": int(dur * 1000),
        })
        ocr_processing_seconds.labels(engine="pymupdf").observe(dur)
    doc.close()
    return pages


def _pymupdf_word_boxes(page, page_text: str) -> list[dict]:
    """Map pymupdf's word records to {start, end, x0, y0, x1, y1} dicts
    where start/end are character offsets into `page_text`.

    pymupdf returns words in reading order with PDF user-space coords
    (top-left origin, y-down) but no character offsets — we reconstruct
    them by scanning page_text forward from the previous cursor and
    matching each word substring. Words that can't be located (rare;
    usually OCR artifacts where pymupdf and get_text disagree on
    whitespace handling) are dropped — better to lose a box than emit
    one with bogus offsets.
    """
    try:
        # get_text("words") returns
        #   [(x0, y0, x1, y1, "word", block, line, word), ...]
        records = page.get_text("words")
    except Exception:
        return []
    out: list[dict] = []
    cursor = 0
    n = len(page_text)
    for r in records:
        if len(r) < 5:
            continue
        x0, y0, x1, y1 = float(r[0]), float(r[1]), float(r[2]), float(r[3])
        word = r[4]
        if not word:
            continue
        # Scan forward in the page text starting at the cursor.
        idx = page_text.find(word, cursor)
        if idx < 0:
            # Pymupdf occasionally normalizes ligatures or hyphens; try
            # a slightly broader scan from the beginning before giving
            # up. Cheap because pages are usually <50KB.
            idx = page_text.find(word)
            if idx < 0:
                continue
        end = idx + len(word)
        out.append({
            "start": idx,
            "end": end,
            "x0": x0, "y0": y0, "x1": x1, "y1": y1,
        })
        cursor = end
        if cursor >= n:
            break
    return out


def _pdf_has_text(path: str) -> bool:
    doc = fitz.open(path)
    try:
        return any(page.get_text().strip() for page in doc)
    finally:
        doc.close()


def _ocr_pdf_pages(path: str) -> list[dict]:
    doc = fitz.open(path)
    pages = []
    for i, page in enumerate(doc):
        pix = page.get_pixmap(dpi=150)
        img = Image.frombytes("RGB", [pix.width, pix.height], pix.samples)
        t0 = time.perf_counter()
        result = surya_ocr_page(img, ["en"])
        dur = time.perf_counter() - t0
        method = "surya"
        if result["confidence"] < settings.ocr_confidence_threshold:
            log.info("page %d confidence %.2f below threshold, would try PaddleOCR", i + 1, result["confidence"])
        pages.append({
            "page_number": i + 1,
            "text": result["text"],
            "confidence": result["confidence"],
            "method": method,
            "boxes": result["boxes"],
            "processing_time_ms": int(dur * 1000),
        })
        ocr_processing_seconds.labels(engine=method).observe(dur)
    doc.close()
    return pages


def _ocr_image(path: str) -> list[dict]:
    img = Image.open(path).convert("RGB")
    t0 = time.perf_counter()
    result = surya_ocr_page(img, ["en"])
    dur = time.perf_counter() - t0
    ocr_processing_seconds.labels(engine="surya").observe(dur)
    return [{
        "page_number": 1,
        "text": result["text"],
        "confidence": result["confidence"],
        "method": "surya",
        "boxes": result["boxes"],
        "processing_time_ms": int(dur * 1000),
    }]


async def _persist_pages(tenant_id: str, version_id: str, pages: list[dict], language: str) -> None:
    """Write every page to ocr_results inside one RLS-scoped transaction.

    The schema primary key is (tenant_id, id) and there is no UNIQUE
    constraint on (tenant_id, version_id, page_number), so `ON CONFLICT`
    would not fire. To stay idempotent across retries we delete any rows
    for this version first and re-insert — the delete + insert run in the
    same transaction so there is no window where the version has
    partially-populated results.
    """
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)",
                tenant_id,
            )
            await conn.execute(
                "DELETE FROM ocr_results WHERE tenant_id = $1 AND version_id = $2",
                tenant_id, version_id,
            )
            for p in pages:
                await conn.execute(
                    """
                    INSERT INTO ocr_results (
                        tenant_id, id, version_id, page_number,
                        text_content, confidence, language,
                        bounding_boxes, word_boxes, processing_time_ms, engine, created_at
                    ) VALUES (
                        $1, gen_random_uuid(), $2, $3, $4, $5, $6,
                        $7::jsonb, $8::jsonb, $9, $10, NOW()
                    )
                    """,
                    tenant_id,
                    version_id,
                    int(p["page_number"]),
                    p["text"],
                    float(p["confidence"]),
                    language,
                    json.dumps(p.get("boxes") or []),
                    json.dumps(p.get("word_boxes") or []),
                    int(p.get("processing_time_ms") or 0),
                    p.get("method") or "surya",
                )


def _build_ocr_completed_envelope(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    page_count: int,
    language: str,
    avg_conf: float,
    full_text: str,
    engine: str,
    correlation_id: str,
) -> dict:
    return {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "dms.intelligence",
        "type": OCR_COMPLETED_SUBJECT,
        "subject": f"version/{version_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "page_count": page_count,
            "language": language,
            "confidence_avg": avg_conf,
            # `content` is the key the search indexer reads; `full_text`
            # and `text` are kept as aliases for intelligence consumers
            # that still use the older names.
            "content": full_text,
            "full_text": full_text,
            "text": full_text,
            "engine": engine,
        },
    }


async def _publish_ocr_completed(envelope: dict, correlation_id: str) -> bool:
    """Publish the event, returning True on success. Exceptions are logged
    and counted but never raised — failure here must not cause Celery to
    retry, because the ocr_results row is already committed."""
    try:
        await publish_cloudevent(OCR_COMPLETED_SUBJECT, envelope, correlation_id=correlation_id)
        return True
    except Exception:
        log.exception("ocr_completed publish failed")
        ocr_publish_failed_total.inc()
        return False


def _jittered_backoff(attempt: int) -> float:
    """Exponential backoff with ±jitter%. attempt is 0-indexed."""
    base = settings.ocr_retry_base_seconds * (2 ** attempt)
    jitter = base * settings.ocr_retry_jitter
    return base + random.uniform(-jitter, jitter)


def _classify_error(exc: Exception) -> str:
    """Map Python exceptions to a DLQ reason label. Kept small and
    stable because the labels drive Grafana panels + alerts."""
    name = type(exc).__name__
    msg = str(exc).lower()
    if "timeout" in name.lower() or "timeout" in msg:
        return "timeout"
    if "s3" in msg or "boto" in name.lower() or "nosuchkey" in msg:
        return "s3_error"
    if "ocr" in msg or "surya" in msg or "paddle" in msg:
        return "engine_error"
    if "postgres" in msg or "pgx" in msg or "insert" in msg or "relation" in msg:
        return "persist_error"
    return "engine_error"


@celery_app.task(
    name="app.tasks.ocr.process_ocr",
    bind=True,
    acks_late=True,           # don't ACK Celery broker until task finishes
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    # 90s/page * typical 20 pages = ~30min. soft_time_limit fires just
    # before the hard cap so cleanup runs; hard cap terminates the
    # worker.
    soft_time_limit=1800,
    time_limit=1830,
)
def process_ocr(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    content_blob_id: str,
    region_pin: str,
    storage_bucket: str,
    storage_key: str,
    mime_type: str,
    correlation_id: str = "",
    language: str = "en",
    event_id: str = "",
    force_engine: str = "",
):
    """Run OCR for one document. Per Wave 5 Prompt 5.3:

    - 3 retries with jittered exponential backoff (Celery handles the
      wait; retry_jitter=True adds variance, retry_backoff=True does
      the exponential component).
    - acks_late=True so the broker redelivers on worker crash mid-run.
    - DLQ publish on terminal failure (after all retries exhausted),
      routed by classified error reason.
    - Completion metrics: ocr_documents_total{status},
      ocr_duration_seconds, ocr_pages_total.
    - Completion ledger: ocr_processed_events row flipped to
      completed/failed. See `app.dedupe`.
    """
    if mime_type not in OCR_MIMES:
        ocr_documents_total.labels(status="skipped").inc()
        return {"status": "skipped", "reason": f"mime {mime_type} not OCR-able"}

    t_start = time.perf_counter()
    workdir = tempfile.mkdtemp(prefix="ocr-")
    src = os.path.join(workdir, "source")
    try:
        _s3().download_file(storage_bucket, storage_key, src)

        if mime_type == "application/pdf":
            # force_engine="surya" bypasses the pymupdf fast path so the
            # layout viewer can get bounding boxes even on text-PDFs.
            if force_engine == "surya" or not _pdf_has_text(src):
                pages = _ocr_pdf_pages(src)
            else:
                pages = _extract_text_pdf(src)
        else:
            pages = _ocr_image(src)

        if pages:
            method_counts: dict[str, int] = {}
            for p in pages:
                method_counts[p.get("method", "surya")] = method_counts.get(p.get("method", "surya"), 0) + 1
            engine = max(method_counts, key=method_counts.get)
        else:
            engine = "none"

        total_text = "\n\n".join(p["text"] for p in pages if p["text"])
        if len(total_text.encode("utf-8")) > MAX_FULL_TEXT_BYTES:
            total_text = total_text.encode("utf-8")[:MAX_FULL_TEXT_BYTES].decode("utf-8", errors="ignore")
        total_chars = len(total_text)
        avg_conf = (sum(p["confidence"] for p in pages) / len(pages)) if pages else 0.0

        try:
            asyncio.run(_persist_pages(tenant_id, version_id, pages, language))
        except Exception:
            ocr_errors_total.labels(error_type="db_insert").inc()
            log.exception("ocr_results insert failed, retrying")
            raise

        for p in pages:
            ocr_pages_processed_total.labels(
                engine=p.get("method", "surya"),
                language=language,
            ).inc()
        ocr_pages_total.inc(len(pages))

        envelope = _build_ocr_completed_envelope(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            page_count=len(pages),
            language=language,
            avg_conf=round(avg_conf, 3),
            full_text=total_text,
            engine=engine,
            correlation_id=correlation_id,
        )
        published = asyncio.run(_publish_ocr_completed(envelope, correlation_id))

        ocr_documents_total.labels(status="completed").inc()
        ocr_duration_seconds.observe(time.perf_counter() - t_start)

        # Ledger flip: this event_id is now observably complete and
        # will dedupe on any redelivery.
        try:
            asyncio.run(mark_completed(tenant_id=tenant_id, event_id=event_id))
        except Exception:
            log.exception("mark_completed failed; dedupe row may retry falsely")

        log.info(
            "ocr.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "page_count": len(pages),
                "total_chars": total_chars,
                "engine": engine,
                "published": published,
                "attempt": self.request.retries + 1,
            },
        )

        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "page_count": len(pages),
            "total_chars": total_chars,
            "avg_confidence": round(avg_conf, 3),
            "engine": engine,
            "published": published,
        }
    except Exception as exc:
        ocr_errors_total.labels(error_type="task").inc()
        # Celery re-raises on retry; this branch also runs on the
        # FINAL attempt (self.request.retries == max_retries). Detect
        # terminality and route to DLQ.
        is_terminal = self.request.retries >= settings.ocr_max_retries
        if is_terminal:
            reason = _classify_error(exc)
            ocr_documents_total.labels(status="failed").inc()
            try:
                asyncio.run(publish_dlq(
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
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                ))
            except Exception:
                log.exception("terminal DLQ publish failed")
        raise
    finally:
        shutil.rmtree(workdir, ignore_errors=True)
