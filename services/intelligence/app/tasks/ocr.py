"""OCR task — extracts text from PDFs and images via Surya, persists the
per-page results to Postgres, and publishes a `dms.version.ocr_completed.v1`
CloudEvent so the search service can fold the text into OpenSearch and the
rest of the intelligence pipeline (classify/NER/embed/duplicate) can fan out.
"""
from __future__ import annotations

import asyncio
import json
import logging
import math
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

from app import mime_gate as _mime_gate
from app.config import settings
from app.db.pool import get_pool
from app.dedupe import mark_completed, mark_failed, publish_dlq
from app.events.publisher import publish_cloudevent
from app.processing_stages import ocr_failure_reason, record_stage
from app.storage.envelope import EnvelopeError, decrypt_blob
from app.metrics import (
    ocr_documents_total,
    ocr_duration_seconds,
    ocr_errors_total,
    ocr_pages_processed_total,
    ocr_pages_total,
    ocr_processing_seconds,
    ocr_publish_failed_total,
)
from app.models.ocr_model import (
    mean_confidence,
    paddle_ocr_page,
    surya_ocr_page,
    trocr_ocr_page,
)
from app.worker import celery_app
from app.events.subjects import OCR_COMPLETED_SUBJECT

log = logging.getLogger(__name__)

_redis_client = None


def _get_redis():
    """Lazy Redis client for per-tenant OCR config (mirrors llm_gateway)."""
    global _redis_client
    if _redis_client is None:
        import redis
        _redis_client = redis.from_url(settings.redis_cache_url, decode_responses=True)
    return _redis_client


def _normalize_engine(value: str) -> str:
    """Map any engine spelling to one of: auto | printed | handwriting | surya.
    'surya' is the legacy 'printed + force-OCR' value (bypasses the pymupdf
    text fast path); kept for the existing ?force=surya rerun callers."""
    e = (value or "").strip().lower()
    if e in ("icr", "handwriting", "handwritten", "trocr"):
        return "handwriting"
    if e in ("printed", "paddle", "type", "typed"):
        return "printed"
    if e == "surya":
        return "surya"
    return "auto"


def _resolve_engine(tenant_id: str, doc_type: str, force_engine: str) -> str:
    """Decide which engine to run, most-specific wins:

      1. an explicit non-auto force_engine (rerun / event override)
      2. a per-doc-type override in the tenant's ocr_config
      3. the tenant default in ocr_config
      4. settings.ocr_default_engine

    Reads Redis key ocr_config:{tenant_id} = {"engine": "...",
    "doc_type_overrides": {"<doc_type>": "..."}}. Any Redis failure falls
    back to the configured default — engine choice must never block OCR.
    The resolved value is normalized and returned for both routing AND the
    audit trail (persisted engine label + dms.ocr.completed envelope)."""
    norm = _normalize_engine(force_engine)
    if norm != "auto":
        return norm
    try:
        raw = _get_redis().get(f"ocr_config:{tenant_id}")
        if raw:
            cfg = json.loads(raw)
            overrides = cfg.get("doc_type_overrides") or {}
            if doc_type and doc_type in overrides:
                return _normalize_engine(overrides[doc_type])
            if cfg.get("engine"):
                return _normalize_engine(cfg["engine"])
    except Exception:
        log.warning("ocr_config lookup failed for tenant %s; using default", tenant_id, exc_info=True)
    return _normalize_engine(settings.ocr_default_engine)

# Re-exported from app.mime_gate (single source of truth shared with the
# upload-event consumer — see that module's docstring for the drift bug).
OCR_MIMES = _mime_gate.OCR_MIMES


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


_is_text_mime = _mime_gate.is_text_mime


def _extract_text_file(path: str) -> list[dict]:
    """Read a textual file directly as one page. Matches the page dict shape
    _extract_text_pdf produces so the downstream persist/emit path is
    unchanged. Decodes as UTF-8 (replacing undecodable bytes) — wrong but
    safe for the rare non-UTF-8 text file; detection refinement is a TODO."""
    t0 = time.perf_counter()
    with open(path, "rb") as fh:
        raw = fh.read()
    text = raw.decode("utf-8", errors="replace").strip()
    if not text:
        return []
    return [{
        "page_number": 1,
        "text": text,
        "confidence": 1.0,
        "method": "text",
        "boxes": [],
        "word_boxes": [],
        "processing_time_ms": int((time.perf_counter() - t0) * 1000),
    }]


def _extract_text_pdf(path: str) -> list[dict]:
    doc = _safe_fitz_open(path)
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


# PDFs with CID-encoded Arabic/CJK fonts and no ToUnicode CMap make
# pymupdf return raw glyph IDs (0x01, 0x02, ...) instead of Unicode.
# `.strip()` alone treats that garbage as valid text and sends us down
# the fast path, leaving the user with tofu boxes. Require that the
# extracted bytes are mostly real characters before trusting them.
_GARBAGE_CTRL_THRESHOLD = 0.30


class TerminalOCRError(Exception):
    """Raised for failures we KNOW won't succeed on retry — bytes that
    aren't actually parseable (encrypted blob, corrupted upload, wrong
    mime). The task handler short-circuits autoretry and routes to DLQ
    on the first occurrence, saving compute and giving operators a
    clean failure signal in ocr_processed_events.last_error.
    """
    def __init__(self, reason: str, msg: str):
        super().__init__(msg)
        self.reason = reason


def _safe_fitz_open(path: str):
    """fitz.open with our terminal-error classification. Raises
    TerminalOCRError(reason='parse_error') when the bytes aren't
    parseable by mupdf.

    Common cause: the storage blob is envelope-encrypted (AES-GCM
    wrapped by per-tenant KEK) and the OCR worker doesn't decrypt
    before passing to mupdf. See TODO at the top of process_ocr.
    """
    try:
        return fitz.open(path)
    except fitz.FileDataError as exc:
        size = os.path.getsize(path) if os.path.exists(path) else -1
        raise TerminalOCRError(
            "parse_error",
            f"mupdf cannot open '{path}' (size={size}B): {exc}. "
            "Likely an envelope-encrypted blob, partial upload, or wrong mime."
        ) from exc


def _pdf_has_text(path: str) -> bool:
    doc = _safe_fitz_open(path)
    try:
        for page in doc:
            txt = page.get_text().strip()
            if not txt:
                continue
            ctrl = sum(1 for c in txt if ord(c) < 0x20 and c not in "\t\n\r")
            if ctrl / len(txt) <= _GARBAGE_CTRL_THRESHOLD:
                return True
        return False
    finally:
        doc.close()


def _maybe_paddle_fallback(result: dict, img, page_no: int) -> tuple[dict, str]:
    """When the Surya result is below the configured floor, re-run the page
    through PaddleOCR and keep whichever engine scored higher. Returns
    (chosen_result, engine_label). A disabled floor (<=0) or an unavailable
    PaddleOCR keeps the Surya result untouched."""
    floor = settings.ocr_paddle_fallback_threshold
    surya_conf = result.get("confidence", 0.0) or 0.0
    if floor <= 0 or surya_conf >= floor:
        return result, "surya"
    pad = paddle_ocr_page(img, ["en"])
    if pad is None:
        log.info(
            "page %d surya conf %.2f < %.2f; PaddleOCR unavailable, keeping surya",
            page_no, surya_conf, floor,
        )
        return result, "surya"
    if (pad.get("confidence", 0.0) or 0.0) > surya_conf:
        log.info(
            "page %d PaddleOCR fallback win: conf %.2f -> %.2f",
            page_no, surya_conf, pad.get("confidence", 0.0),
        )
        return pad, "paddle"
    return result, "surya"


def _iou(a: dict, b: dict) -> float:
    """Intersection-over-union of two {x1,y1,x2,y2} boxes."""
    ix1, iy1 = max(a["x1"], b["x1"]), max(a["y1"], b["y1"])
    ix2, iy2 = min(a["x2"], b["x2"]), min(a["y2"], b["y2"])
    iw, ih = max(0.0, ix2 - ix1), max(0.0, iy2 - iy1)
    inter = iw * ih
    if inter <= 0:
        return 0.0
    area_a = max(0.0, a["x2"] - a["x1"]) * max(0.0, a["y2"] - a["y1"])
    area_b = max(0.0, b["x2"] - b["x1"]) * max(0.0, b["y2"] - b["y1"])
    union = area_a + area_b - inter
    return inter / union if union > 0 else 0.0


def _merge_ocr_results(printed: dict, handwriting: dict) -> dict:
    """Merge a printed-engine result with an ICR result into one page.

    The two engines see the same page differently: printed (Surya/Paddle)
    nails typed labels; ICR (TrOCR) recovers ink the printed engine missed or
    scored badly. We take the union of their line boxes and, wherever two
    boxes overlap (IoU > 0.5), keep the higher-confidence one — so typed
    regions stay typed and handwritten regions become ICR. Text is rebuilt
    top-to-bottom, left-to-right from the kept boxes so reading order holds
    on forms (printed prompt + handwritten answer on the same row)."""
    kept: list[dict] = list(printed.get("boxes") or [])
    for hb in handwriting.get("boxes") or []:
        clash = next((i for i, pb in enumerate(kept) if _iou(pb, hb) > 0.5), None)
        if clash is None:
            kept.append(hb)
        elif (hb.get("confidence", 0.0) or 0.0) > (kept[clash].get("confidence", 0.0) or 0.0):
            kept[clash] = hb
    kept.sort(key=lambda b: (round(b["y1"] / 10.0), b["x1"]))
    text = "\n".join(b["text"] for b in kept if b.get("text"))
    confs = [b["confidence"] for b in kept if b.get("confidence") is not None]
    return {
        "text": text,
        "confidence": mean_confidence(confs),
        "boxes": kept,
    }


def _ocr_page_image(img, page_no: int, engine: str) -> tuple[dict, str]:
    """Run one rasterised page through the selected engine. `engine` is the
    normalized choice (auto | printed | surya | handwriting). Returns
    (result, method_label) where the label is what gets persisted + audited:

      - printed/surya : Surya, with the existing PaddleOCR confidence fallback
      - handwriting   : TrOCR primary, MERGED with Surya so typed regions on a
                        mixed form aren't lost -> "trocr+surya"
      - auto          : printed first; if the page scores below the handwriting
                        threshold (and autodetect is on), also run TrOCR and
                        merge -> "surya+trocr" (recovers handwriting without a
                        manual flag)
    """
    if engine == "handwriting":
        hw = trocr_ocr_page(img, ["en"])
        printed = surya_ocr_page(img, ["ar", "en"])
        return _merge_ocr_results(printed, hw), "trocr+surya"

    result = surya_ocr_page(img, ["ar", "en"])
    result, method = _maybe_paddle_fallback(result, img, page_no)
    if engine == "auto" and settings.ocr_handwriting_autodetect:
        if (result.get("confidence", 0.0) or 0.0) < settings.ocr_handwriting_threshold:
            log.info("page %d conf %.2f below handwriting floor; trying ICR merge",
                     page_no, result.get("confidence", 0.0) or 0.0)
            hw = trocr_ocr_page(img, ["en"])
            merged = _merge_ocr_results(result, hw)
            return merged, method + "+trocr"
    return result, method


def _ocr_pdf_pages(path: str, engine: str = "auto") -> list[dict]:
    doc = _safe_fitz_open(path)
    pages = []
    for i, page in enumerate(doc):
        pix = page.get_pixmap(dpi=150)
        img = Image.frombytes("RGB", [pix.width, pix.height], pix.samples)
        t0 = time.perf_counter()
        result, method = _ocr_page_image(img, i + 1, engine)
        dur = time.perf_counter() - t0
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


def _ocr_image(path: str, engine: str = "auto") -> list[dict]:
    img = Image.open(path).convert("RGB")
    t0 = time.perf_counter()
    result, method = _ocr_page_image(img, 1, engine)
    dur = time.perf_counter() - t0
    ocr_processing_seconds.labels(engine=method).observe(dur)
    return [{
        "page_number": 1,
        "text": result["text"],
        "confidence": result["confidence"],
        "method": method,
        "boxes": result["boxes"],
        "processing_time_ms": int(dur * 1000),
    }]


def _scrub_nonfinite(v):
    """Recursively replace NaN/+Inf/-Inf floats with None so json.dumps + Postgres jsonb accept the payload.

    surya occasionally emits float('nan') for confidence or bbox
    coordinates on pages with no detectable text. Python's json.dumps
    serialises NaN as the literal token "NaN" (not standard JSON), and
    asyncpg's jsonb encoder rejects it with InvalidTextRepresentationError,
    aborting the whole transaction and leaving the OCR job in a
    permanent failed state. Converting non-finite floats to None
    keeps the payload valid JSON.
    """
    if isinstance(v, float):
        return None if (math.isnan(v) or math.isinf(v)) else v
    if isinstance(v, dict):
        return {k: _scrub_nonfinite(x) for k, x in v.items()}
    if isinstance(v, (list, tuple)):
        return [_scrub_nonfinite(x) for x in v]
    return v


async def _fetch_blob_crypto(tenant_id: str, content_blob_id: str) -> dict | None:
    """Pull envelope-encryption metadata for a blob.

    Returns None when content_blob_id is empty/missing. Returns a dict
    with keys 'encrypted_dek', 'dek_nonce', 'kek_id' otherwise — those
    fields are populated for envelope-encrypted blobs and absent
    (NULL) for plaintext blobs. Caller checks for non-empty
    encrypted_dek before decrypting.
    """
    if not content_blob_id:
        return None
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)",
                tenant_id,
            )
            row = await conn.fetchrow(
                """
                SELECT encrypted_dek, dek_nonce, kek_id
                  FROM content_blobs
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, content_blob_id,
            )
            if row is None:
                return None
            return {
                "encrypted_dek": bytes(row["encrypted_dek"]) if row["encrypted_dek"] else b"",
                "dek_nonce":     bytes(row["dek_nonce"])     if row["dek_nonce"]     else b"",
                "kek_id":        row["kek_id"] or "",
            }


def _decrypt_src_if_envelope(src: str, tenant_id: str, content_blob_id: str) -> None:
    """If the blob is envelope-encrypted, replace `src` bytes with plaintext.

    Looks up content_blobs for encrypted_dek/dek_nonce/kek_id; if all
    three are populated, decrypts the file in-place. Plaintext blobs
    are left untouched. On crypto failure raises TerminalOCRError
    with reason='decrypt_error' so the task DLQs without retry burn
    (the bytes / key won't be different on retry).

    Mirrors decrypt_stream.go's full-buffer model — AES-GCM tag
    can only be verified after all ciphertext is read.
    """
    crypto = asyncio.run(_fetch_blob_crypto(tenant_id, content_blob_id))
    if not crypto or not crypto["encrypted_dek"]:
        # Plaintext blob — leave src untouched.
        return
    try:
        with open(src, "rb") as fh:
            ciphertext = fh.read()
        plaintext = decrypt_blob(
            ciphertext,
            crypto["dek_nonce"],
            crypto["encrypted_dek"],
            crypto["kek_id"],
        )
        with open(src, "wb") as fh:
            fh.write(plaintext)
    except EnvelopeError as exc:
        raise TerminalOCRError(
            "decrypt_error",
            f"envelope decrypt failed for blob {content_blob_id}: {exc}",
        ) from exc


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
                raw_conf = p.get("confidence")
                try:
                    conf = float(raw_conf) if raw_conf is not None else 0.0
                except (TypeError, ValueError):
                    conf = 0.0
                if math.isnan(conf) or math.isinf(conf):
                    log.warning(
                        "ocr: non-finite confidence %r on version %s page %s — coercing to 0.0",
                        raw_conf, version_id, p.get("page_number"),
                    )
                    conf = 0.0
                boxes = _scrub_nonfinite(p.get("boxes") or [])
                word_boxes = _scrub_nonfinite(p.get("word_boxes") or [])
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
                    conf,
                    language,
                    json.dumps(boxes),
                    json.dumps(word_boxes),
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
    if isinstance(exc, TerminalOCRError):
        return exc.reason
    name = type(exc).__name__
    msg = str(exc).lower()
    if "filedataerror" in name.lower() or "fzerror" in name.lower():
        return "parse_error"
    if "timeout" in name.lower() or "timeout" in msg:
        return "timeout"
    if "s3" in msg or "boto" in name.lower() or "nosuchkey" in msg:
        return "s3_error"
    if "ocr" in msg or "surya" in msg or "paddle" in msg:
        return "engine_error"
    if "postgres" in msg or "pgx" in msg or "insert" in msg or "relation" in msg:
        return "persist_error"
    return "engine_error"


def ocr_file(src: str, mime_type: str, language: str = "en",
             force_engine: str = "") -> tuple[list[dict], str, str, float]:
    """OCR a local file that is already downloaded AND decrypted.

    Shared engine core for both the version path (process_ocr) and the WS3
    pre-commit ingestion path (process_ingestion) so they read pages
    identically. Returns (pages, full_text, engine, avg_confidence). The
    caller owns S3 download + envelope decrypt + persistence.
    """
    engine = _normalize_engine(force_engine)  # auto | printed | surya | handwriting
    # 'surya' (layout boxes) and 'handwriting' (a text layer can't carry ink)
    # both force the rasterise+OCR path, bypassing the pymupdf text fast path.
    force_ocr = engine in ("surya", "handwriting")
    # The rasterised path runs a real page engine; collapse the legacy 'surya'
    # alias to 'printed'.
    page_engine = "printed" if engine == "surya" else engine
    if mime_type == "application/pdf":
        if force_ocr or not _pdf_has_text(src):
            pages = _ocr_pdf_pages(src, page_engine)
        else:
            pages = _extract_text_pdf(src)
    elif _is_text_mime(mime_type):
        pages = _extract_text_file(src)
    else:
        pages = _ocr_image(src, page_engine)

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
    avg_conf = mean_confidence(p["confidence"] for p in pages)
    return pages, total_text, engine, avg_conf


@celery_app.task(
    name="app.tasks.ocr.process_ocr",
    bind=True,
    acks_late=True,           # don't ACK Celery broker until task finishes
    autoretry_for=(Exception,),
    # Bytes-not-parseable failures (TerminalOCRError) bypass autoretry —
    # they will never succeed on a retry. Listed in `throws` so Celery
    # logs them as expected without traceback noise; the manual handler
    # below still publishes to DLQ + ledger.
    throws=(TerminalOCRError,),
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
    doc_type: str = "",
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
    if mime_type not in OCR_MIMES and not _is_text_mime(mime_type):
        ocr_documents_total.labels(status="skipped").inc()
        record_stage(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            stage="ocr", status="skipped", failure_reason="unsupported_mime",
            failure_detail=f"mime {mime_type} is not OCR-able",
        )
        return {"status": "skipped", "reason": f"mime {mime_type} not OCR-able"}

    # ADR 0115 — mark the OCR stage running so the UI can show progress
    # (and so a silently-dropped job is visibly stuck at 'running' rather
    # than invisible). Best-effort; never blocks OCR.
    record_stage(
        tenant_id=tenant_id, document_id=document_id, version_id=version_id,
        stage="ocr", status="running",
    )

    # Envelope-encrypted blobs are decrypted in-place after download.
    # _decrypt_src_if_envelope looks up content_blobs for the wrapped
    # DEK + nonce + kek_id and, when present, replaces `src` with
    # plaintext. Plaintext blobs (no wrapped DEK) pass through. Local
    # KEK mode only — Vault prod path is a TODO and would call the
    # document service's /decrypt-stream endpoint instead.

    t_start = time.perf_counter()
    workdir = tempfile.mkdtemp(prefix="ocr-")
    src = os.path.join(workdir, "source")
    try:
        _s3().download_file(storage_bucket, storage_key, src)
        _decrypt_src_if_envelope(src, tenant_id, content_blob_id)

        # Resolve the engine per tenant/doc-type (explicit force_engine wins).
        # The resolved value drives routing AND is what gets persisted +
        # emitted on dms.ocr.completed, so the engine choice is auditable.
        resolved_engine = _resolve_engine(tenant_id, doc_type, force_engine)
        log.info(
            "ocr engine resolved: tenant=%s doc_type=%s force=%r -> %s",
            tenant_id, doc_type or "-", force_engine or "-", resolved_engine,
        )
        pages, total_text, engine, avg_conf = ocr_file(src, mime_type, language, resolved_engine)
        total_chars = len(total_text)

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

        # ADR 0115 — terminal success. A publish failure is itself a
        # failure surface (downstream stages never fire), so record
        # 'failed' with event_publish_failed when the completed event
        # couldn't be published; otherwise 'completed'.
        if published:
            record_stage(
                tenant_id=tenant_id, document_id=document_id,
                version_id=version_id, stage="ocr", status="completed",
            )
        else:
            record_stage(
                tenant_id=tenant_id, document_id=document_id,
                version_id=version_id, stage="ocr", status="failed",
                failure_reason="event_publish_failed",
                failure_detail="OCR finished but the ocr_completed event "
                "could not be published; downstream stages were not triggered.",
            )

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
        #
        # TerminalOCRError short-circuits the retry budget entirely:
        # parse_error etc. won't succeed on retry, so DLQ immediately
        # rather than burning 3 attempts. The `throws` tuple on the
        # task decorator also tells Celery to log without traceback.
        is_terminal = (
            isinstance(exc, TerminalOCRError)
            or self.request.retries >= settings.ocr_max_retries
        )
        if is_terminal:
            reason = _classify_error(exc)
            ocr_documents_total.labels(status="failed").inc()
            # ADR 0115 — surface the terminal failure to the user with a
            # sanitized, displayable reason. TerminalOCRError carries a
            # short message; generic exceptions are mapped to a taxonomy
            # code (str(exc) is operator-only via failure_detail).
            record_stage(
                tenant_id=tenant_id, document_id=document_id,
                version_id=version_id, stage="ocr", status="failed",
                failure_reason=ocr_failure_reason(exc),
                failure_detail=str(exc),
            )
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
