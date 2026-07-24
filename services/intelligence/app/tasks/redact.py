"""PII redaction.

Two pipelines coexist (see ADR 0079):

1. **Legacy ad-hoc** — `apply_redactions` and
   `detect_redaction_candidates`. Used by the legal-hold
   `/redact` endpoint. Burns regions the admin drew via
   page.search_for(value).

2. **Candidate review** — `populate_candidates` and
   `apply_redaction_job`. Used by the blueprint §6.7 flow.
   Reads `is_pii=true` rows from document_entities, resolves
   per-page rectangles from ocr_results.word_boxes, and writes
   redaction_candidates rows for human review. The apply step
   reads approved candidates back, burns by exact coordinates
   (no search_for guessing), uploads as a fresh
   content_blob, creates a new document_versions row, and
   emits dms.version.uploaded.v1 so OCR/NER re-run on the
   redacted output.
"""
from __future__ import annotations

import asyncio
import hashlib
import io
import json
import logging
import os
import shutil
import tempfile
import time
import uuid
from datetime import datetime, timezone

import fitz

from app.config import settings
from app.events.publisher import publish_cloudevent
from app.tasks.ner import detect_entities
from app.worker import celery_app
from app.events.subjects import (
    NOTIFICATION_SUBJECT,
    REDACTION_APPLIED_SUBJECT,
    VERSION_UPLOADED_SUBJECT,
)

log = logging.getLogger(__name__)


class RedactionVerificationError(Exception):
    """Raised when redacted content survives in the output PDF's text
    layer. This is the guardrail that keeps a burn that DIDN'T actually
    remove text (wrong page, wrong coordinates, image-only glyphs the
    text layer still carries) from ever being marked `applied`."""



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


# ===========================================================================
# Legacy ad-hoc redaction (Wave 11.5 — legal-hold flow)
# ===========================================================================

@celery_app.task(name="app.tasks.redact.detect_redaction_candidates", bind=True)
def detect_redaction_candidates(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    text: str,
    entity_types: list[str] | None = None,
):
    result = detect_entities.apply(
        args=[tenant_id, document_id, version_id, text]
    ).get(timeout=120)

    candidates = result.get("entities", [])
    if entity_types:
        allowed = set(t.upper() for t in entity_types)
        candidates = [e for e in candidates if e["entity_type"].upper() in allowed]

    return {
        "tenant_id": tenant_id,
        "document_id": document_id,
        "version_id": version_id,
        "candidates": candidates,
        "count": len(candidates),
    }


@celery_app.task(name="app.tasks.redact.apply_redactions", bind=True)
def apply_redactions(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    storage_bucket: str,
    storage_key: str,
    entities: list[dict],
    output_bucket: str | None = None,
):
    workdir = tempfile.mkdtemp(prefix="redact-")
    src = os.path.join(workdir, "source.pdf")
    out = os.path.join(workdir, "redacted.pdf")

    try:
        _s3().download_file(storage_bucket, storage_key, src)
        doc = fitz.open(src)

        for entity in entities:
            value = entity.get("entity_value", "")
            if not value:
                continue
            for page in doc:
                areas = page.search_for(value)
                for rect in areas:
                    page.add_redact_annot(rect, fill=(0, 0, 0))
            for page in doc:
                page.apply_redactions()

        doc.save(out)
        doc.close()

        bucket = output_bucket or storage_bucket
        redacted_key = storage_key.rsplit("/", 1)[0] + "/redacted.pdf"
        _s3().upload_file(out, bucket, redacted_key, ExtraArgs={"ContentType": "application/pdf"})

        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "redacted_key": redacted_key,
            "redacted_bucket": bucket,
            "entities_redacted": len(entities),
        }
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


# ===========================================================================
# Candidate review redaction (ADR 0079 — blueprint §6.7)
# ===========================================================================

@celery_app.task(name="app.tasks.redact.populate_candidates", bind=True)
def populate_candidates(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    event_id: str = "",
    correlation_id: str = "",
):
    """Triggered after NER finishes. Reads document_entities rows with
    is_pii=true and writes redaction_candidates rows in `pending` —
    one per entity, with rectangles resolved from ocr_results.word_boxes
    when available. Idempotent: deletes any prior candidates for this
    version before inserting (the apply step has already snapshot'd
    the approved set into redaction_jobs.candidates_snapshot, so
    re-populating is safe)."""
    inserted = asyncio.run(_populate_candidates_async(
        tenant_id=tenant_id,
        document_id=document_id,
        version_id=version_id,
    ))
    log.info(
        "populate_candidates.completed",
        extra={
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "candidate_count": inserted,
            "event_id": event_id,
            "correlation_id": correlation_id,
        },
    )
    return {
        "status": "completed",
        "tenant_id": tenant_id,
        "document_id": document_id,
        "version_id": version_id,
        "candidate_count": inserted,
    }


async def _populate_candidates_async(
    *, tenant_id: str, document_id: str, version_id: str,
) -> int:
    from app.persist import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
            )
            # Wipe any prior pending candidates for this version — apply
            # path has already snapshotted any approved set, so we won't
            # lose audit information by replacing pending rows.
            await conn.execute(
                "DELETE FROM redaction_candidates "
                "WHERE tenant_id = $1 AND version_id = $2 AND status = 'pending'",
                tenant_id, version_id,
            )
            ent_rows = await conn.fetch(
                """SELECT id, entity_type, entity_value, start_offset, end_offset, source
                     FROM document_entities
                    WHERE tenant_id = $1 AND version_id = $2 AND is_pii = true""",
                tenant_id, version_id,
            )
            if not ent_rows:
                return 0
            # Build per-page char-offset → word-box index from ocr_results.
            page_index = await _build_page_index(conn, tenant_id, version_id)
            inserted = 0
            for r in ent_rows:
                etype = r["entity_type"]
                evalue = r["entity_value"]
                doc_start = int(r["start_offset"])
                doc_end = int(r["end_offset"])
                page_no, local_start, local_end, rects = _resolve_rectangles(
                    page_index, doc_start, doc_end,
                )
                cid = uuid.uuid4()
                await conn.execute(
                    """INSERT INTO redaction_candidates (
                           tenant_id, id, document_id, version_id,
                           source, entity_id, entity_type, entity_value,
                           rectangles, page_number, char_start, char_end,
                           status, created_at, updated_at
                       ) VALUES (
                           $1, $2, $3, $4,
                           $5, $6, $7, $8,
                           $9::jsonb, $10, $11, $12,
                           'pending', now(), now()
                       )""",
                    tenant_id, cid, document_id, version_id,
                    "ner", r["id"], etype, evalue,
                    json.dumps(rects), page_no, local_start, local_end,
                )
                inserted += 1
            return inserted


async def _build_page_index(conn, tenant_id: str, version_id: str) -> list[dict]:
    """Returns one entry per OCR page in reading order with the page's
    [doc_start, doc_end) char-offset range and the word_boxes payload.
    The doc-wide offset is "\n\n".join(page_text), matching the worker
    join in services/intelligence/app/tasks/ocr.py."""
    rows = await conn.fetch(
        """SELECT page_number, text_content, word_boxes
             FROM ocr_results
            WHERE tenant_id = $1 AND version_id = $2
            ORDER BY page_number ASC""",
        tenant_id, version_id,
    )
    out = []
    cursor = 0
    for r in rows:
        text = r["text_content"] or ""
        page_start = cursor
        page_end = page_start + len(text)
        cursor = page_end + 2  # "\n\n" join separator
        wb_raw = r["word_boxes"]
        try:
            wb = json.loads(wb_raw) if isinstance(wb_raw, (str, bytes, bytearray)) else (wb_raw or [])
        except (TypeError, ValueError):
            wb = []
        words = []
        page_w = 0.0
        page_h = 0.0
        if isinstance(wb, dict):
            page_w = float(wb.get("page_width") or 0.0)
            page_h = float(wb.get("page_height") or 0.0)
            words = wb.get("words") or []
        out.append({
            "page_number": r["page_number"],
            "doc_start": page_start,
            "doc_end": page_end,
            "page_width": page_w,
            "page_height": page_h,
            "words": words,
        })
    return out


def _resolve_rectangles(
    page_index: list[dict], doc_start: int, doc_end: int,
):
    """Find the page that contains the entity span and the union
    rectangle of word boxes overlapping it. Returns
    (page_number, local_start, local_end, rectangles[]).
    Empty rectangles when no word_boxes are persisted for that page —
    burn-in will skip the candidate (logged once per apply pass)."""
    for p in page_index:
        if doc_start < p["doc_start"] or doc_end > p["doc_end"]:
            continue
        local_start = doc_start - p["doc_start"]
        local_end = doc_end - p["doc_start"]
        rects: list[dict] = []
        if p["words"] and p["page_width"] > 0 and p["page_height"] > 0:
            min_x = min_y = float("inf")
            max_x = max_y = float("-inf")
            touched = False
            for w in p["words"]:
                try:
                    ws = int(w.get("start", -1))
                    we = int(w.get("end", -1))
                except (TypeError, ValueError):
                    continue
                if we <= local_start or ws >= local_end:
                    continue
                touched = True
                min_x = min(min_x, float(w.get("x0", 0)))
                min_y = min(min_y, float(w.get("y0", 0)))
                max_x = max(max_x, float(w.get("x1", 0)))
                max_y = max(max_y, float(w.get("y1", 0)))
            if touched:
                rects.append({
                    "page": p["page_number"] - 1,
                    "x0": min_x, "y0": min_y, "x1": max_x, "y1": max_y,
                })
        return p["page_number"], local_start, local_end, rects
    return None, None, None, []


@celery_app.task(name="app.tasks.redact.apply_redaction_job", bind=True)
def apply_redaction_job(
    self,
    tenant_id: str,
    job_id: str,
    document_id: str,
    source_version_id: str,
    storage_bucket: str,
    storage_key: str,
    candidates: list[dict],
    applied_by: str,
    event_id: str = "",
    correlation_id: str = "",
):
    """Burns approved candidates into a new PDF, uploads it as a fresh
    content_blob + document_versions row, and updates the
    redaction_jobs row with the redacted version. Emits
    dms.version.uploaded.v1 so the natural OCR/NER pipeline re-runs
    on the redacted output (the §6.7 acceptance test rides on this:
    OCR'ing the redacted version must surface no PII)."""
    workdir = tempfile.mkdtemp(prefix="redact-job-")
    src = os.path.join(workdir, "source.pdf")
    out = os.path.join(workdir, "redacted.pdf")
    started = time.monotonic()
    try:
        _s3().download_file(storage_bucket, storage_key, src)
        doc = fitz.open(src)
        burned = 0
        for c in candidates:
            for r in (c.get("rectangles") or []):
                page_idx = int(r.get("page") or 0)
                if page_idx < 0 or page_idx >= doc.page_count:
                    continue
                rect = fitz.Rect(
                    float(r["x0"]), float(r["y0"]),
                    float(r["x1"]), float(r["y1"]),
                )
                doc[page_idx].add_redact_annot(rect, fill=(0, 0, 0))
                burned += 1
        for page in doc:
            page.apply_redactions()
        doc.save(out, garbage=4, clean=True, deflate=True)
        doc.close()

        # Upload as a fresh blob — distinct key so the source key is
        # untouched. The Go side will create the content_blob row +
        # document_versions row on the dms.version.uploaded.v1 event.
        size_bytes = os.path.getsize(out)
        with open(out, "rb") as fh:
            blob_bytes = fh.read()
        sha256 = hashlib.sha256(blob_bytes).hexdigest()
        redacted_key = (
            storage_key.rsplit("/", 1)[0] + f"/redacted-{job_id}.pdf"
        )
        _s3().upload_file(
            out, storage_bucket, redacted_key,
            ExtraArgs={"ContentType": "application/pdf"},
        )

        # Persist the new version + flip the document's current pointer
        # in one tx, then mark the job completed.
        new_version_id = str(uuid.uuid4())
        new_blob_id = str(uuid.uuid4())
        asyncio.run(_persist_redacted_version(
            tenant_id=tenant_id,
            document_id=document_id,
            blob_id=new_blob_id,
            version_id=new_version_id,
            storage_bucket=storage_bucket,
            storage_key=redacted_key,
            size_bytes=size_bytes,
            sha256=sha256,
            applied_by=applied_by,
            job_id=job_id,
        ))

        # Emit dms.version.uploaded.v1 so OCR / NER re-run on the
        # redacted PDF. This is what the §6.7 acceptance test rides on
        # — OCR of the burned output must not surface the PII.
        try:
            envelope = {
                "specversion": "1.0",
                "id": str(uuid.uuid4()),
                "source": "dms.intelligence.redact",
                "type": VERSION_UPLOADED_SUBJECT,
                "subject": f"version/{new_version_id}",
                "time": datetime.now(timezone.utc).isoformat(),
                "datacontenttype": "application/json",
                "tenantid": tenant_id,
                "correlationid": correlation_id,
                "data": {
                    "tenant_id": tenant_id,
                    "document_id": document_id,
                    "version_id": new_version_id,
                    "storage_bucket": storage_bucket,
                    "storage_key": redacted_key,
                    "mime_type": "application/pdf",
                    "size": size_bytes,
                    "sha256": sha256,
                },
            }
            asyncio.run(publish_cloudevent(
                VERSION_UPLOADED_SUBJECT, envelope, correlation_id=correlation_id,
            ))
        except Exception:
            log.exception("redact: version.uploaded publish failed")

        elapsed_ms = int((time.monotonic() - started) * 1000)
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "job_id": job_id,
            "redacted_version_id": new_version_id,
            "redacted_key": redacted_key,
            "candidates_burned": burned,
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        # Best-effort: mark the job failed so the UI surfaces it.
        try:
            asyncio.run(_mark_job_failed(
                tenant_id=tenant_id, job_id=job_id, error=str(exc),
            ))
        except Exception:
            log.exception("redact: failed to mark job failed")
        raise
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


async def _persist_redacted_version(
    *, tenant_id: str, document_id: str, blob_id: str, version_id: str,
    storage_bucket: str, storage_key: str, size_bytes: int, sha256: str,
    applied_by: str, job_id: str,
) -> None:
    from app.persist import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
            )
            # 1. Content blob (storage-owned table; no RLS).
            await conn.execute(
                """INSERT INTO content_blobs (id, size_bytes, mime_type, sha256_hash, storage_uri, created_at)
                   VALUES ($1, $2, 'application/pdf', $3, $4, now())
                   ON CONFLICT (id) DO NOTHING""",
                blob_id, size_bytes, sha256, f"s3://{storage_bucket}/{storage_key}",
            )
            # 2. Next version_number for this document.
            row = await conn.fetchrow(
                """SELECT COALESCE(MAX(version_number), 0) + 1 AS next_n
                     FROM document_versions
                    WHERE tenant_id = $1 AND document_id = $2""",
                tenant_id, document_id,
            )
            next_n = int(row["next_n"])
            # 3. New version row.
            await conn.execute(
                """INSERT INTO document_versions (
                       tenant_id, id, document_id, version_number,
                       content_blob_id, size_bytes, mime_type, sha256_hash,
                       created_by, created_by_name, change_summary, created_at
                   ) VALUES (
                       $1, $2, $3, $4,
                       $5, $6, 'application/pdf', $7,
                       $8, '', 'Redacted version', now()
                   )""",
                tenant_id, version_id, document_id, next_n,
                blob_id, size_bytes, sha256, applied_by,
            )
            # 4. Flip the document's current pointer to the redacted
            # version. The source row stays in document_versions —
            # gated download via the unredacted endpoint.
            await conn.execute(
                """UPDATE documents
                      SET current_version_id = $3, updated_at = now()
                    WHERE tenant_id = $1 AND id = $2""",
                tenant_id, document_id, version_id,
            )
            # 5. Mark the job completed + link the redacted version.
            await conn.execute(
                """UPDATE redaction_jobs
                      SET status = 'completed',
                          redacted_version_id = $3,
                          completed_at = now()
                    WHERE tenant_id = $1 AND id = $2""",
                tenant_id, job_id, version_id,
            )
            # 6. Mark every approved candidate as applied.
            await conn.execute(
                """UPDATE redaction_candidates
                      SET status = 'applied', updated_at = now()
                    WHERE tenant_id = $1
                      AND document_id = $2
                      AND status = 'approved'""",
                tenant_id, document_id,
            )


async def _mark_job_failed(*, tenant_id: str, job_id: str, error: str) -> None:
    from app.persist import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute(
            "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
        )
        await conn.execute(
            """UPDATE redaction_jobs
                  SET status = 'failed', error_message = $3, completed_at = now()
                WHERE tenant_id = $1 AND id = $2""",
            tenant_id, job_id, error[:1000],
        )


# ===========================================================================
# Legal-hold document_redactions path (Wave 12.5 — the /redact endpoint)
# ===========================================================================
#
# The document service's POST /documents/{id}/redact writes a
# document_redactions row (status='queued', admin-drawn `regions` +
# `entity_types`) and emits dms.document.redacted.v1. Previously the
# intelligence consumer flipped that row straight to 'applied' WITHOUT
# touching the PDF — a FALSE compliance signal: reviewers believed
# PII/PHI was removed while the original text stayed readable and
# indexed. This path now really burns the regions, VERIFIES the content
# is gone from the output text layer, stores a new version, and only
# then marks 'applied'; any failure marks 'failed' (never applied).


def _burn_and_verify(
    pdf_bytes: bytes, regions: list[dict], entity_values: list[str],
) -> tuple[bytes, int, list[str]]:
    """Burn `regions` (admin-drawn {page,x,y,width,height} rects, 0-based
    page) and `entity_values` (searched across every page) into the PDF
    via PyMuPDF apply_redactions() — TRUE content removal, not an overlay
    — then VERIFY: re-extract the output text layer and assert none of
    the redacted strings survive. Returns (redacted_bytes, burned_count,
    must_be_absent). Raises RedactionVerificationError if any target
    string is still present in the output.
    """
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    must_absent: set[str] = set()
    burned = 0

    for reg in regions or []:
        try:
            page_idx = int(reg.get("page", 0))
        except (TypeError, ValueError):
            continue
        if page_idx < 0 or page_idx >= doc.page_count:
            log.warning("redaction region page %s out of range (0..%d); skipped",
                        reg.get("page"), doc.page_count - 1)
            continue
        x = float(reg.get("x", 0.0))
        y = float(reg.get("y", 0.0))
        w = float(reg.get("width", 0.0))
        h = float(reg.get("height", 0.0))
        rect = fitz.Rect(x, y, x + w, y + h)
        # Capture the text under the rect BEFORE burning — this is what
        # must be absent afterwards. Tokens ≥3 chars avoid trivial
        # substring false-positives on stray punctuation.
        under = doc[page_idx].get_textbox(rect) or ""
        for tok in under.split():
            if len(tok) >= 3:
                must_absent.add(tok)
        doc[page_idx].add_redact_annot(rect, fill=(0, 0, 0))
        burned += 1

    for val in entity_values or []:
        val = (val or "").strip()
        if not val:
            continue
        for page in doc:
            for rect in page.search_for(val):
                page.add_redact_annot(rect, fill=(0, 0, 0))
                burned += 1
        must_absent.add(val)

    for page in doc:
        page.apply_redactions()

    buf = io.BytesIO()
    doc.save(buf, garbage=4, clean=True, deflate=True)
    redacted = buf.getvalue()
    doc.close()

    # Verify against the OUTPUT's own text layer.
    vdoc = fitz.open(stream=redacted, filetype="pdf")
    out_text = "\n".join(p.get_text("text") for p in vdoc)
    vdoc.close()
    leaked = sorted(s for s in must_absent if s and s in out_text)
    if leaked:
        raise RedactionVerificationError(
            f"redacted content survived in output text layer: {leaked[:5]}"
            + (" …" if len(leaked) > 5 else "")
        )
    return redacted, burned, sorted(must_absent)


def _resolve_entity_values(pdf_bytes: bytes, entity_types: list[str]) -> list[str]:
    """Best-effort: when a redaction targets entity TYPES (not explicit
    regions), run NER over the PDF's own text layer and return the
    concrete values of the requested types to search-and-burn. Failure
    here is non-fatal — regions still burn, and verification only ever
    asserts strings we actually targeted."""
    if not entity_types:
        return []
    try:
        doc = fitz.open(stream=pdf_bytes, filetype="pdf")
        text = "\n\n".join(p.get_text("text") for p in doc)
        doc.close()
        if not text.strip():
            return []
        wanted = {t.upper() for t in entity_types}
        result = detect_entities.apply(
            args=["", "", "", text]
        ).get(timeout=120)
        values = []
        for e in result.get("entities", []):
            if e.get("entity_type", "").upper() in wanted:
                v = e.get("entity_value", "")
                if v:
                    values.append(v)
        return values
    except Exception:
        log.exception("redaction: entity-type NER resolve failed; regions still burn")
        return []


@celery_app.task(name="app.tasks.redact.apply_document_redaction", bind=True)
def apply_document_redaction(
    self,
    tenant_id: str,
    document_id: str,
    redaction_id: str,
    version_id: str,
    storage_bucket: str,
    storage_key: str,
    regions: list[dict],
    entity_types: list[str],
    applied_by: str = "",
    correlation_id: str = "",
):
    """Real burn for the legal-hold document_redactions path. Download →
    burn regions/entities → VERIFY removal → upload → new version (emits
    dms.version.uploaded.v1 so OCR/NER/index re-run on the redacted
    output) → mark document_redactions 'applied'. Any failure marks
    'failed' + notifies; `applied` is NEVER set without a verified
    artifact."""
    workdir = tempfile.mkdtemp(prefix="doc-redact-")
    src = os.path.join(workdir, "source.pdf")
    out = os.path.join(workdir, "redacted.pdf")
    started = time.monotonic()
    try:
        _s3().download_file(storage_bucket, storage_key, src)
        with open(src, "rb") as fh:
            pdf_bytes = fh.read()

        entity_values = _resolve_entity_values(pdf_bytes, entity_types)
        # Burn + VERIFY. Raises RedactionVerificationError on any leak →
        # caught below → status 'failed', never 'applied'.
        redacted, burned, must_absent = _burn_and_verify(
            pdf_bytes, regions or [], entity_values,
        )
        with open(out, "wb") as fh:
            fh.write(redacted)

        size_bytes = len(redacted)
        sha256 = hashlib.sha256(redacted).hexdigest()
        redacted_key = storage_key.rsplit("/", 1)[0] + f"/redacted-{redaction_id}.pdf"
        # Durable store BEFORE we record the version or mark applied.
        _s3().upload_file(
            out, storage_bucket, redacted_key,
            ExtraArgs={"ContentType": "application/pdf"},
        )

        new_version_id = str(uuid.uuid4())
        new_blob_id = str(uuid.uuid4())
        asyncio.run(_persist_redacted_document_version(
            tenant_id=tenant_id,
            document_id=document_id,
            redaction_id=redaction_id,
            blob_id=new_blob_id,
            version_id=new_version_id,
            storage_bucket=storage_bucket,
            storage_key=redacted_key,
            size_bytes=size_bytes,
            sha256=sha256,
            applied_by=applied_by,
            task_id=self.request.id or "",
        ))

        # Re-run the pipeline on the redacted output so OCR/NER/index
        # reflect the removed content (search must not surface it).
        try:
            envelope = {
                "specversion": "1.0",
                "id": str(uuid.uuid4()),
                "source": "dms.intelligence.redact",
                "type": VERSION_UPLOADED_SUBJECT,
                "subject": f"version/{new_version_id}",
                "time": datetime.now(timezone.utc).isoformat(),
                "datacontenttype": "application/json",
                "tenantid": tenant_id,
                "correlationid": correlation_id,
                "data": {
                    "tenant_id": tenant_id,
                    "document_id": document_id,
                    "version_id": new_version_id,
                    "storage_bucket": storage_bucket,
                    "storage_key": redacted_key,
                    "mime_type": "application/pdf",
                    "size": size_bytes,
                    "sha256": sha256,
                },
            }
            asyncio.run(publish_cloudevent(
                VERSION_UPLOADED_SUBJECT, envelope, correlation_id=correlation_id,
            ))
        except Exception:
            log.exception("redact: version.uploaded publish failed")

        elapsed_ms = int((time.monotonic() - started) * 1000)
        log.info(
            "document redaction applied redaction_id=%s burned=%d verified_absent=%d",
            redaction_id, burned, len(must_absent),
        )
        return {
            "status": "applied",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "redaction_id": redaction_id,
            "redacted_version_id": new_version_id,
            "redacted_key": redacted_key,
            "regions_burned": burned,
            "verified_absent": len(must_absent),
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        # Never applied-on-error: mark failed + notify, then re-raise so
        # the task is recorded as failed.
        try:
            asyncio.run(_mark_redaction_failed(
                tenant_id=tenant_id, redaction_id=redaction_id, error=str(exc),
            ))
        except Exception:
            log.exception("redact: failed to mark redaction failed")
        _notify_redaction_failed(
            tenant_id=tenant_id, document_id=document_id,
            redaction_id=redaction_id, error=str(exc),
            correlation_id=correlation_id,
        )
        raise
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


async def _persist_redacted_document_version(
    *, tenant_id: str, document_id: str, redaction_id: str, blob_id: str,
    version_id: str, storage_bucket: str, storage_key: str, size_bytes: int,
    sha256: str, applied_by: str, task_id: str,
) -> None:
    """Create the redacted content_blob + document_versions row, flip the
    document's current pointer, and mark document_redactions 'applied' —
    all in one transaction, so 'applied' can never be observed without
    the redacted version durably recorded."""
    from app.persist import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
            )
            # content_blobs is storage-owned; insert with the REAL schema
            # (tenant_id + storage_bucket/storage_key, not a storage_uri).
            await conn.execute(
                """INSERT INTO content_blobs (
                       id, tenant_id, sha256_hash, storage_region,
                       storage_bucket, storage_key, size_bytes, mime_type
                   ) VALUES ($1, $2, $3, 'us-east-1', $4, $5, $6, 'application/pdf')
                   ON CONFLICT (tenant_id, sha256_hash) DO NOTHING""",
                blob_id, tenant_id, sha256, storage_bucket, storage_key, size_bytes,
            )
            # Resolve the blob id actually stored (dedup may have reused
            # an existing row with the same (tenant, sha256)).
            brow = await conn.fetchrow(
                "SELECT id FROM content_blobs WHERE tenant_id = $1 AND sha256_hash = $2",
                tenant_id, sha256,
            )
            resolved_blob_id = str(brow["id"]) if brow else blob_id

            row = await conn.fetchrow(
                """SELECT COALESCE(MAX(version_number), 0) + 1 AS next_n
                     FROM document_versions
                    WHERE tenant_id = $1 AND document_id = $2""",
                tenant_id, document_id,
            )
            next_n = int(row["next_n"])
            await conn.execute(
                """INSERT INTO document_versions (
                       tenant_id, id, document_id, version_number,
                       content_blob_id, size_bytes, mime_type, sha256_hash,
                       created_by, created_by_name, change_summary, created_at
                   ) VALUES (
                       $1, $2, $3, $4, $5, $6, 'application/pdf', $7,
                       $8, '', 'Redacted version', now()
                   )""",
                tenant_id, version_id, document_id, next_n,
                resolved_blob_id, size_bytes, sha256,
                (applied_by or None),
            )
            await conn.execute(
                """UPDATE documents
                      SET current_version_id = $3, updated_at = now()
                    WHERE tenant_id = $1 AND id = $2""",
                tenant_id, document_id, version_id,
            )
            # Only NOW — verified + stored + versioned — mark applied.
            await conn.execute(
                """UPDATE document_redactions
                      SET status = 'applied', completed_at = now(),
                          intelligence_task_id = $3
                    WHERE tenant_id = $1 AND id = $2""",
                tenant_id, redaction_id, task_id,
            )


async def _mark_redaction_failed(*, tenant_id: str, redaction_id: str, error: str) -> None:
    from app.persist import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute(
            "SELECT set_config('app.current_tenant', $1, true)", tenant_id,
        )
        await conn.execute(
            """UPDATE document_redactions
                  SET status = 'failed', error_message = $3, completed_at = now()
                WHERE tenant_id = $1 AND id = $2""",
            tenant_id, redaction_id, error[:1000],
        )


def _notify_redaction_failed(
    *, tenant_id: str, document_id: str, redaction_id: str,
    error: str, correlation_id: str = "",
) -> None:
    """Best-effort operator notification on redaction failure. Emits a
    role-targeted dms.notification.send.v1 so the compliance team learns
    the redaction did NOT complete (the document still contains the
    unredacted content). Never raises."""
    try:
        envelope = {
            "specversion": "1.0",
            "id": str(uuid.uuid4()),
            "source": "dms.intelligence.redact",
            "type": NOTIFICATION_SUBJECT,
            "time": datetime.now(timezone.utc).isoformat(),
            "datacontenttype": "application/json",
            "tenantid": tenant_id,
            "correlationid": correlation_id,
            "data": {
                "tenant_id": tenant_id,
                "document_id": document_id,
                "subject": "Redaction failed",
                "body": (
                    f"Redaction {redaction_id} on document {document_id} FAILED and "
                    f"was NOT applied — the document still contains the unredacted "
                    f"content. Reason: {error[:200]}"
                ),
                "target_roles": ["compliance_officer", "admin"],
                "category": "compliance",
                "correlation_id": correlation_id,
            },
        }
        asyncio.run(publish_cloudevent(
            NOTIFICATION_SUBJECT, envelope, correlation_id=correlation_id,
        ))
    except Exception:
        log.exception("redact: failure notification emit failed")
