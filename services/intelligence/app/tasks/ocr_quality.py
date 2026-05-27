"""OCR quality scoring (ADR 0057) — composite per-page score + per-doc grade.

Triggered by: dms.version.ocr_completed.v1
Persists to:  ocr_quality_scores + ocr_quality_summary
Emits:        dms.ocr.quality.completed.v1
              dms.version.ocr_retry_requested.v1   (auto-retry path)
              dms.notification.send.v1             (notify_on_poor + grade=poor)

Five sub-scores (per ADR 0057 §"Five sub-scores"):
  char_confidence  — engine's own number (re-normalised)
  word_density     — words/100 capped at 1.0; catches blank pages
  line_regularity  — 1 - stddev(line_heights)/mean(line_heights)
  noise_ratio      — 1 - non_alphanum/total
  language_score   — token-shape heuristic (length + alpha ratio)

Composite weights live in QUALITY_WEIGHTS. When char_confidence is
NULL (engine didn't report), weights re-normalise across the
remaining four so no page is unfairly penalised.
"""
from __future__ import annotations

import asyncio
import json
import logging
import re
import statistics
import time
import uuid
from datetime import datetime, timezone

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

OCR_QUALITY_COMPLETED_SUBJECT = "dms.ocr.quality.completed.v1"
OCR_RETRY_REQUESTED_SUBJECT = "dms.version.ocr_retry_requested.v1"
NOTIFICATION_SUBJECT = "dms.notification.send.v1"
CONSUMER = "ocr_quality"

QUALITY_WEIGHTS: dict[str, float] = {
    "char_confidence":  0.35,
    "word_density":     0.15,
    "line_regularity":  0.15,
    "noise_ratio":      0.15,
    "language_score":   0.20,
}

# Issue thresholds (per-page, on the sub-scores).
LOW_CONFIDENCE_AT  = 0.50
SPARSE_TEXT_AT     = 0.10
SKEWED_AT          = 0.50
NOISY_AT           = 0.30   # noise_ratio > 0.3 raw → score < 0.7 → flag
GARBLED_AT         = 0.50

DEFAULT_CONFIG = {
    "enabled": True,
    "review_threshold":     0.60,
    "excellent_threshold":  0.90,
    "good_threshold":       0.75,
    "fair_threshold":       0.60,
    "auto_retry_below":     0.40,
    "notify_on_poor":       False,
}


@celery_app.task(
    name="app.tasks.ocr_quality.score",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=120,
    time_limit=180,
)
def score(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id, document_id=document_id, version_id=version_id,
        event_id=event_id, correlation_id=correlation_id,
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

        pages = await _fetch_ocr_pages(tenant_id, version_id)
        if not pages:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "skipped", "reason": "no ocr results"}

        scored = [_score_page(p, cfg) for p in pages]
        summary = _build_summary(scored, cfg)

        prior_auto_retried = await _prior_auto_retried(tenant_id, document_id)
        retry_emitted = False
        engines = {p.get("engine") or "" for p in pages}
        original_engine = next(iter(engines)) if len(engines) == 1 else ""

        await _persist(
            tenant_id=tenant_id, document_id=document_id, version_id=version_id,
            scored=scored, summary=summary, cfg=cfg,
            auto_retried_flag=prior_auto_retried,
            correlation_id=correlation_id,
        )

        # Emit auto-retry only when:
        #   * composite below threshold
        #   * we haven't already auto-retried this document
        #   * engine is tesseract (we can swap to surya)
        if (summary["avg_score"] < cfg["auto_retry_below"]
                and not prior_auto_retried
                and original_engine.lower() == "tesseract"):
            await _emit_retry(
                tenant_id=tenant_id, document_id=document_id,
                version_id=version_id, correlation_id=correlation_id,
            )
            await _set_auto_retried(tenant_id, document_id)
            retry_emitted = True

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "ocr_quality.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "avg_score": summary["avg_score"],
                "min_score": summary["min_score"],
                "grade": summary["quality_grade"],
                "pages_needing_review": summary["pages_needing_review"],
                "auto_retry_emitted": retry_emitted,
                "elapsed_ms": elapsed_ms,
                "correlation_id": correlation_id,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "grade": summary["quality_grade"],
            "avg_score": summary["avg_score"],
            "pages_needing_review": summary["pages_needing_review"],
            "auto_retry_emitted": retry_emitted,
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
                log.exception("ocr_quality terminal DLQ publish failed")
        raise


# ---- pure scorers --------------------------------------------------------

_TOKEN_RE = re.compile(r"[A-Za-zÀ-ɏͰ-ϿЀ-ӿ֐-׿؀-ۿ一-鿿]+", re.UNICODE)


def _word_density_score(word_count: int) -> float:
    if word_count <= 0:
        return 0.0
    return min(1.0, word_count / 100.0)


def _noise_ratio_score(text: str) -> float:
    if not text:
        return 0.0
    total = len(text)
    if total == 0:
        return 0.0
    alnum = sum(1 for c in text if c.isalnum() or c.isspace())
    noise = (total - alnum) / total
    return max(0.0, 1.0 - noise)


def _language_score(text: str) -> float:
    """Token-shape heuristic: ratio of tokens that look like real
    words (length 2–20, captured by the unicode-aware regex above).
    Language-agnostic — see ADR 0057 for the rationale."""
    if not text:
        return 0.0
    raw = text.split()
    if not raw:
        return 0.0
    matches = _TOKEN_RE.findall(text)
    valid = sum(1 for m in matches if 2 <= len(m) <= 20)
    return min(1.0, valid / max(1, len(raw)))


def _line_regularity_score(boxes: list) -> float | None:
    """Boxes is the bounding_boxes JSONB. We expect each entry to have
    a `bbox` of [x0, y0, x1, y1] or a height field. Returns None when
    the engine didn't supply enough boxes to compute regularity."""
    heights: list[float] = []
    for b in boxes or []:
        if isinstance(b, dict):
            bbox = b.get("bbox") or b.get("box")
            if isinstance(bbox, list) and len(bbox) == 4:
                try:
                    h = float(bbox[3]) - float(bbox[1])
                    if h > 0:
                        heights.append(h)
                        continue
                except (TypeError, ValueError):
                    pass
            for k in ("height", "h"):
                if isinstance(b.get(k), (int, float)) and b[k] > 0:
                    heights.append(float(b[k]))
                    break
    if len(heights) < 3:
        return None
    mean = statistics.mean(heights)
    if mean <= 0:
        return None
    sd = statistics.pstdev(heights)
    return max(0.0, min(1.0, 1.0 - (sd / mean)))


def _score_page(page: dict, cfg: dict) -> dict:
    text = page.get("text_content") or ""
    char_count = len(text)
    word_count = len(text.split())

    # char_confidence: trust engine when > 0; treat 0.0 as "missing" so
    # we re-normalise weights instead of penalising unfairly.
    raw_conf = page.get("confidence")
    if raw_conf is None or raw_conf <= 0.0:
        char_conf = None
    else:
        char_conf = max(0.0, min(1.0, float(raw_conf)))

    word_density   = _word_density_score(word_count)
    line_reg       = _line_regularity_score(page.get("bounding_boxes") or [])
    noise_score    = _noise_ratio_score(text)
    lang_score     = _language_score(text)

    sub = {
        "char_confidence":  char_conf,
        "word_density":     word_density,
        "line_regularity":  line_reg,
        "noise_ratio":      noise_score,
        "language_score":   lang_score,
    }

    # Re-normalise weights across the sub-scores that are NOT None.
    available = {k: v for k, v in sub.items() if v is not None}
    if not available:
        composite = 0.0
    else:
        weight_sum = sum(QUALITY_WEIGHTS[k] for k in available)
        composite = sum(QUALITY_WEIGHTS[k] * v for k, v in available.items()) / weight_sum
    composite = max(0.0, min(1.0, round(composite, 4)))

    issues = []
    if char_conf is not None and char_conf < LOW_CONFIDENCE_AT:
        issues.append("low_confidence")
    if word_density < SPARSE_TEXT_AT:
        issues.append("sparse_text")
    if line_reg is not None and line_reg < SKEWED_AT:
        issues.append("skewed")
    raw_noise = 1.0 - noise_score
    if raw_noise > NOISY_AT:
        issues.append("noisy")
    if lang_score < GARBLED_AT and word_count > 5:
        # Don't flag short pages as garbled (no signal).
        issues.append("garbled")

    needs_review = composite < cfg["review_threshold"]
    return {
        "page_number":     int(page.get("page_number") or 0),
        "overall_score":   composite,
        "char_confidence": char_conf,
        "word_density":    round(word_density, 4),
        "line_regularity": round(line_reg, 4) if line_reg is not None else None,
        "noise_ratio":     round(noise_score, 4),
        "language_score":  round(lang_score, 4),
        "issues":          issues,
        "word_count":      word_count,
        "char_count":      char_count,
        "needs_review":    needs_review,
    }


# ---- summary -------------------------------------------------------------

def _build_summary(scored: list[dict], cfg: dict) -> dict:
    scores = [s["overall_score"] for s in scored] or [0.0]
    avg = statistics.mean(scores)
    mn = min(scores)
    mx = max(scores)
    needs = sum(1 for s in scored if s["needs_review"])
    grade = _grade_for(avg, cfg)
    return {
        "avg_score":            round(avg, 4),
        "min_score":            round(mn, 4),
        "max_score":            round(mx, 4),
        "total_pages":          len(scored),
        "pages_needing_review": needs,
        "quality_grade":        grade,
    }


def _grade_for(avg: float, cfg: dict) -> str:
    if avg >= cfg["excellent_threshold"]:
        return "excellent"
    if avg >= cfg["good_threshold"]:
        return "good"
    if avg >= cfg["fair_threshold"]:
        return "fair"
    return "poor"


# ---- DB helpers ----------------------------------------------------------

async def _load_config(tenant_id: str) -> dict:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT enabled, review_threshold, excellent_threshold,
                       good_threshold, fair_threshold, auto_retry_below,
                       notify_on_poor
                  FROM ocr_quality_config WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return dict(DEFAULT_CONFIG)
    return {
        "enabled":              row["enabled"],
        "review_threshold":     float(row["review_threshold"]),
        "excellent_threshold":  float(row["excellent_threshold"]),
        "good_threshold":       float(row["good_threshold"]),
        "fair_threshold":       float(row["fair_threshold"]),
        "auto_retry_below":     float(row["auto_retry_below"]),
        "notify_on_poor":       row["notify_on_poor"],
    }


async def _fetch_ocr_pages(tenant_id: str, version_id: str) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT page_number, text_content, confidence,
                       COALESCE(language, '') AS language,
                       bounding_boxes,
                       COALESCE(engine, '') AS engine
                  FROM ocr_results
                 WHERE tenant_id = $1 AND version_id = $2
                 ORDER BY page_number
                """,
                tenant_id, version_id,
            )
    out = []
    for r in rows:
        bbs = r["bounding_boxes"]
        if isinstance(bbs, str):
            try:
                bbs = json.loads(bbs)
            except json.JSONDecodeError:
                bbs = []
        out.append({
            "page_number":    r["page_number"],
            "text_content":   r["text_content"] or "",
            "confidence":     float(r["confidence"] or 0.0),
            "language":       r["language"] or "",
            "bounding_boxes": bbs or [],
            "engine":         r["engine"] or "",
        })
    return out


async def _prior_auto_retried(tenant_id: str, document_id: str) -> bool:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT auto_retried FROM ocr_quality_summary
                 WHERE tenant_id = $1 AND document_id = $2
                """,
                tenant_id, document_id,
            )
    return bool(row and row["auto_retried"])


async def _set_auto_retried(tenant_id: str, document_id: str) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE ocr_quality_summary
                   SET auto_retried = true
                 WHERE tenant_id = $1 AND document_id = $2
                """,
                tenant_id, document_id,
            )


async def _emit_retry(
    *, tenant_id: str, document_id: str, version_id: str, correlation_id: str,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'document', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                OCR_RETRY_REQUESTED_SUBJECT, document_id,
                json.dumps({
                    "tenant_id": tenant_id,
                    "document_id": document_id,
                    "version_id": version_id,
                    "force_engine": "surya",
                    "reason": "auto_retry_low_quality",
                    "correlation_id": correlation_id,
                    "emitted_at": datetime.now(timezone.utc).isoformat(),
                }),
            )


async def _persist(
    *, tenant_id: str, document_id: str, version_id: str,
    scored: list[dict], summary: dict, cfg: dict,
    auto_retried_flag: bool, correlation_id: str,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )

            for s in scored:
                await conn.execute(
                    """
                    INSERT INTO ocr_quality_scores
                        (tenant_id, document_id, version_id, page_number,
                         overall_score, char_confidence, word_density,
                         line_regularity, noise_ratio, language_score,
                         issues, word_count, char_count, needs_review)
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
                            $11, $12, $13, $14)
                    ON CONFLICT (tenant_id, version_id, page_number) DO UPDATE
                      SET overall_score   = EXCLUDED.overall_score,
                          char_confidence = EXCLUDED.char_confidence,
                          word_density    = EXCLUDED.word_density,
                          line_regularity = EXCLUDED.line_regularity,
                          noise_ratio     = EXCLUDED.noise_ratio,
                          language_score  = EXCLUDED.language_score,
                          issues          = EXCLUDED.issues,
                          word_count      = EXCLUDED.word_count,
                          char_count      = EXCLUDED.char_count,
                          needs_review    = EXCLUDED.needs_review,
                          updated_at      = NOW()
                    """,
                    tenant_id, document_id, version_id, s["page_number"],
                    s["overall_score"], s["char_confidence"], s["word_density"],
                    s["line_regularity"], s["noise_ratio"], s["language_score"],
                    s["issues"], s["word_count"], s["char_count"], s["needs_review"],
                )

            await conn.execute(
                """
                INSERT INTO ocr_quality_summary
                    (tenant_id, document_id, version_id,
                     avg_score, min_score, max_score,
                     total_pages, pages_needing_review,
                     quality_grade, auto_retried, scored_at)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
                ON CONFLICT (tenant_id, document_id) DO UPDATE
                   SET version_id           = EXCLUDED.version_id,
                       avg_score            = EXCLUDED.avg_score,
                       min_score            = EXCLUDED.min_score,
                       max_score            = EXCLUDED.max_score,
                       total_pages          = EXCLUDED.total_pages,
                       pages_needing_review = EXCLUDED.pages_needing_review,
                       quality_grade        = EXCLUDED.quality_grade,
                       scored_at            = NOW()
                """,
                tenant_id, document_id, version_id,
                summary["avg_score"], summary["min_score"], summary["max_score"],
                summary["total_pages"], summary["pages_needing_review"],
                summary["quality_grade"], auto_retried_flag,
            )

            payload = {
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "avg_score": summary["avg_score"],
                "grade": summary["quality_grade"],
                "pages_needing_review": summary["pages_needing_review"],
                "total_pages": summary["total_pages"],
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
                OCR_QUALITY_COMPLETED_SUBJECT, document_id,
                json.dumps(payload),
            )

            if cfg.get("notify_on_poor", False) and summary["quality_grade"] == "poor":
                notify_payload = {
                    "tenant_id": tenant_id,
                    "document_id": document_id,
                    "subject": "OCR quality is poor",
                    "body": (
                        f"Document {document_id} OCR scored {summary['avg_score']:.2f} "
                        f"({summary['pages_needing_review']} pages need review)."
                    ),
                    "target_roles": ["compliance_officer", "admin"],
                    "category": "ocr_quality",
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
