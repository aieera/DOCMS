"""Anomaly detection — workspace-level outlier scan (ADR 0058).

Triggered by: REST POST /api/v1/admin/anomalies/run (the API creates
              a pending anomaly_reports row + enqueues this task).
Persists to:  anomaly_reports.status='completed'/'failed' +
              anomaly_findings rows.
Emits:        dms.anomaly.completed.v1

Three independent strategies:
  metadata    z-score on size_bytes + word_count; misclassified
              detection (rare class in workspace)
  content     Qdrant centroid distance — pull one chunk per doc,
              compute workspace centroid, flag distance > threshold
  behavioral  unusual_upload_time + rapid_uploads (>50/hr by one user)

Never auto-acts. All findings carry status='open' for human review.
"""
from __future__ import annotations

import asyncio
import json
import logging
import math
import statistics
import time
import uuid
from collections import Counter, defaultdict
from datetime import datetime, timezone
from typing import Any

from app.config import settings
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

ANOMALY_COMPLETED_SUBJECT = "dms.anomaly.completed.v1"
CONSUMER = "anomaly_detect"

WORKSPACE_DOC_CAP = 50_000  # ADR 0058 — cap to bound memory
RAPID_UPLOAD_WINDOW_SEC = 3600
RARE_CLASS_THRESHOLD = 0.10  # < 10% of workspace shares this class

DEFAULT_CONFIG = {
    "enabled": True,
    "z_score_threshold": 2.5,
    "content_distance_threshold": 0.7,
    "min_documents_for_analysis": 20,
    "analyze_metadata": True,
    "analyze_content": True,
    "analyze_behavioral": True,
}


@celery_app.task(
    name="app.tasks.anomaly_detect.run",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 2},
    retry_backoff=True,
    retry_backoff_max=120,
    retry_jitter=True,
    soft_time_limit=600,
    time_limit=900,
)
def run(
    self,
    tenant_id: str,
    report_id: str,
    workspace_id: str | None = None,
    analysis_type: str = "combined",
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id, report_id=report_id, workspace_id=workspace_id,
        analysis_type=analysis_type, event_id=event_id,
        correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 2,
    ))


# ---- entry point ---------------------------------------------------------

async def _run_async(
    *, tenant_id, report_id, workspace_id, analysis_type, event_id,
    correlation_id, attempt, is_terminal,
) -> dict:
    start = time.monotonic()
    if not (tenant_id and report_id):
        return {"status": "skipped", "reason": "missing ids"}

    try:
        if event_id and await already_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ):
            return {"status": "duplicate", "event_id": event_id}
        await mark_enqueued(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
            document_id=report_id, version_id=report_id,
        )

        cfg = await _load_config(tenant_id)
        if not cfg["enabled"]:
            await _mark_report_failed(tenant_id, report_id, "anomaly detection disabled")
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "disabled"}

        await _set_report_status(tenant_id, report_id, "processing")

        docs = await _fetch_workspace_docs(tenant_id, workspace_id)
        if len(docs) < cfg["min_documents_for_analysis"]:
            summary = {
                "reason": "insufficient_documents",
                "needed": cfg["min_documents_for_analysis"],
                "got": len(docs),
            }
            await _persist_completion(
                tenant_id=tenant_id, report_id=report_id,
                total_docs=len(docs), findings=[],
                summary=summary, correlation_id=correlation_id,
            )
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "completed", "anomalies_found": 0,
                    "reason": "insufficient_documents"}
        if len(docs) > WORKSPACE_DOC_CAP:
            summary = {
                "reason": "workspace_too_large",
                "cap": WORKSPACE_DOC_CAP,
                "got": len(docs),
            }
            await _persist_completion(
                tenant_id=tenant_id, report_id=report_id,
                total_docs=len(docs), findings=[],
                summary=summary, correlation_id=correlation_id,
            )
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "completed", "anomalies_found": 0,
                    "reason": "workspace_too_large"}

        findings: list[dict] = []
        per_strategy_counts: dict[str, int] = {}

        if analysis_type in ("metadata", "combined") and cfg["analyze_metadata"]:
            m = _metadata_findings(docs, cfg)
            per_strategy_counts["metadata"] = len(m)
            findings.extend(m)

        if analysis_type in ("content", "combined") and cfg["analyze_content"]:
            c = await _content_findings(tenant_id, docs, cfg)
            per_strategy_counts["content"] = len(c)
            findings.extend(c)

        if analysis_type in ("behavioral", "combined") and cfg["analyze_behavioral"]:
            b = _behavioral_findings(docs, cfg)
            per_strategy_counts["behavioral"] = len(b)
            findings.extend(b)

        summary = {
            "analysis_type": analysis_type,
            "per_strategy": per_strategy_counts,
            "thresholds": {
                "z_score":           cfg["z_score_threshold"],
                "content_distance":  cfg["content_distance_threshold"],
            },
        }

        await _persist_completion(
            tenant_id=tenant_id, report_id=report_id,
            total_docs=len(docs), findings=findings,
            summary=summary, correlation_id=correlation_id,
        )
        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)

        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "anomaly_detect.completed",
            extra={
                "tenant_id": tenant_id,
                "report_id": report_id,
                "workspace_id": workspace_id,
                "analysis_type": analysis_type,
                "total_docs": len(docs),
                "anomalies_found": len(findings),
                "per_strategy": per_strategy_counts,
                "elapsed_ms": elapsed_ms,
                "correlation_id": correlation_id,
            },
        )
        return {
            "status": "completed",
            "report_id": report_id,
            "total_documents": len(docs),
            "anomalies_found": len(findings),
            "per_strategy": per_strategy_counts,
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        if is_terminal:
            try:
                await _mark_report_failed(tenant_id, report_id,
                                          f"{type(exc).__name__}: {exc}"[:1000])
            except Exception:
                log.exception("mark report failed (within DLQ branch)")
            reason = classify_error_reason(exc)
            try:
                await publish_dlq(
                    consumer=CONSUMER, reason=reason, tenant_id=tenant_id,
                    document_id=report_id, version_id=report_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=attempt, correlation_id=correlation_id,
                )
                await mark_failed(
                    tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                )
            except Exception:
                log.exception("anomaly_detect terminal DLQ publish failed")
        raise


# ---- strategy: metadata --------------------------------------------------

def _metadata_findings(docs: list[dict], cfg: dict) -> list[dict]:
    """z-score outliers on size_bytes + word_count, plus rare-class
    flagging (misclassified)."""
    findings: list[dict] = []
    z_thresh = float(cfg["z_score_threshold"])

    for metric in ("size_bytes", "word_count"):
        values = [float(d.get(metric) or 0) for d in docs]
        non_zero = [v for v in values if v > 0]
        if len(non_zero) < 5:
            continue
        mean = statistics.mean(non_zero)
        sd = statistics.pstdev(non_zero)
        if sd == 0:
            continue
        for d, v in zip(docs, values):
            if v <= 0:
                continue
            z = (v - mean) / sd
            if abs(z) >= z_thresh:
                anomaly_type = "size_outlier" if metric == "size_bytes" else "wrong_workspace"
                # Reuse 'wrong_workspace' for word_count outliers because
                # the schema's enum doesn't include word_count specifically;
                # the description carries the precise signal.
                if metric == "word_count":
                    anomaly_type = "size_outlier"  # collapse to size-class outlier
                findings.append({
                    "document_id": d["id"],
                    "anomaly_type": anomaly_type,
                    "severity": _severity_from_z(abs(z)),
                    "description": f"{metric} z-score {z:+.2f} (value {int(v)}, mean {int(mean)}, σ {int(sd)})",
                    "evidence": {
                        "metric": metric,
                        "value": v,
                        "mean": mean,
                        "stddev": sd,
                        "z_score": round(z, 4),
                    },
                    "z_score": round(z, 4),
                })

    # Rare classification within workspace.
    classes = [d.get("document_class") or "" for d in docs]
    counts = Counter(c for c in classes if c)
    total = sum(counts.values())
    if total > 0:
        for d in docs:
            cls = d.get("document_class") or ""
            if not cls:
                continue
            share = counts[cls] / total
            if share < RARE_CLASS_THRESHOLD and counts[cls] <= 2:
                findings.append({
                    "document_id": d["id"],
                    "anomaly_type": "misclassified",
                    "severity": "low",
                    "description": (
                        f"document_class '{cls}' appears in {counts[cls]} of "
                        f"{total} ({share * 100:.1f}%) workspace docs"
                    ),
                    "evidence": {
                        "class": cls,
                        "count_in_workspace": counts[cls],
                        "share": round(share, 4),
                    },
                })
    return findings


def _severity_from_z(abs_z: float) -> str:
    if abs_z >= 4.0:
        return "high"
    if abs_z >= 3.0:
        return "medium"
    return "low"


# ---- strategy: content ---------------------------------------------------

async def _content_findings(tenant_id: str, docs: list[dict], cfg: dict) -> list[dict]:
    """Pull one centroid per doc from Qdrant, compute workspace centroid,
    flag docs with cosine distance > threshold. Returns [] when Qdrant
    is unreachable or no docs have embeddings."""
    try:
        from qdrant_client import QdrantClient
        from qdrant_client.models import FieldCondition, Filter, MatchAny, MatchValue
    except Exception:
        return []

    client = QdrantClient(url=settings.qdrant_url)
    coll = settings.qdrant_collection
    doc_ids = [d["id"] for d in docs]

    # Pull up to 5 chunks per doc to compute a per-doc centroid. Single
    # scroll over the whole workspace would explode for large sets; we
    # iterate per-doc with a small limit instead.
    per_doc_centroid: dict[str, list[float]] = {}
    for did in doc_ids:
        try:
            points, _ = client.scroll(
                collection_name=coll,
                scroll_filter=Filter(must=[
                    FieldCondition(key="tenant_id", match=MatchValue(value=tenant_id)),
                    FieldCondition(key="document_id", match=MatchValue(value=did)),
                ]),
                limit=5, with_payload=False, with_vectors=True,
            )
        except Exception:
            continue
        vecs = [p.vector for p in points if getattr(p, "vector", None)]
        if not vecs:
            continue
        per_doc_centroid[did] = _mean_vector(vecs)

    if len(per_doc_centroid) < 5:
        return []

    workspace_centroid = _mean_vector(list(per_doc_centroid.values()))
    threshold = float(cfg["content_distance_threshold"])

    findings: list[dict] = []
    for did, vec in per_doc_centroid.items():
        dist = _cosine_distance(vec, workspace_centroid)
        if dist > threshold:
            findings.append({
                "document_id": did,
                "anomaly_type": "content_outlier",
                "severity": "high" if dist > threshold + 0.2 else "medium",
                "description": (
                    f"Content centroid is {dist:.3f} cosine distance from workspace mean "
                    f"(threshold {threshold:.2f})"
                ),
                "evidence": {
                    "cosine_distance": round(dist, 4),
                    "threshold": threshold,
                    "workspace_size": len(per_doc_centroid),
                },
                "similarity_score": round(1.0 - dist, 4),
            })
    return findings


def _mean_vector(vectors: list[list[float]]) -> list[float]:
    if not vectors:
        return []
    dim = len(vectors[0])
    sums = [0.0] * dim
    for v in vectors:
        for i, x in enumerate(v):
            sums[i] += x
    return [s / len(vectors) for s in sums]


def _cosine_distance(a: list[float], b: list[float]) -> float:
    if not a or not b or len(a) != len(b):
        return 1.0
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    if na == 0 or nb == 0:
        return 1.0
    sim = dot / (na * nb)
    return max(0.0, min(2.0, 1.0 - sim))


# ---- strategy: behavioral ------------------------------------------------

UNUSUAL_HOUR_START = 23
UNUSUAL_HOUR_END = 6


def _behavioral_findings(docs: list[dict], cfg: dict) -> list[dict]:
    findings: list[dict] = []

    # Unusual upload time — outside [06:00, 22:00) UTC. (Per ADR 0058,
    # local-tz-aware analysis is a v2 follow-up.)
    for d in docs:
        ts = d.get("created_at")
        if not ts:
            continue
        hour = ts.hour
        if hour >= UNUSUAL_HOUR_START or hour < UNUSUAL_HOUR_END:
            findings.append({
                "document_id": d["id"],
                "anomaly_type": "unusual_upload_time",
                "severity": "low",
                "description": f"Uploaded at {hour:02d}:{ts.minute:02d} UTC",
                "evidence": {"hour_utc": hour, "minute": ts.minute},
            })

    # Rapid uploads — > 50 docs in any rolling-hour window by one user.
    by_user: dict[str, list[dict]] = defaultdict(list)
    for d in docs:
        if d.get("created_by"):
            by_user[d["created_by"]].append(d)
    rapid_threshold = 50
    for user, items in by_user.items():
        items.sort(key=lambda x: x["created_at"])
        # Sliding window — for each doc, count how many docs by this
        # user landed within the previous RAPID_UPLOAD_WINDOW_SEC seconds.
        window: list[dict] = []
        flagged_ids: set[str] = set()
        for d in items:
            cutoff = d["created_at"]
            window.append(d)
            window = [
                w for w in window
                if (cutoff - w["created_at"]).total_seconds() <= RAPID_UPLOAD_WINDOW_SEC
            ]
            if len(window) > rapid_threshold:
                for w in window:
                    if w["id"] in flagged_ids:
                        continue
                    flagged_ids.add(w["id"])
                    findings.append({
                        "document_id": w["id"],
                        "anomaly_type": "rapid_uploads",
                        "severity": "medium",
                        "description": (
                            f"User uploaded {len(window)} docs in the surrounding hour"
                        ),
                        "evidence": {
                            "user_id": user,
                            "window_count": len(window),
                            "threshold": rapid_threshold,
                        },
                    })
    return findings


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
                SELECT enabled, z_score_threshold, content_distance_threshold,
                       min_documents_for_analysis,
                       analyze_metadata, analyze_content, analyze_behavioral
                  FROM anomaly_config WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return dict(DEFAULT_CONFIG)
    return {
        "enabled":                    row["enabled"],
        "z_score_threshold":          float(row["z_score_threshold"]),
        "content_distance_threshold": float(row["content_distance_threshold"]),
        "min_documents_for_analysis": int(row["min_documents_for_analysis"]),
        "analyze_metadata":           row["analyze_metadata"],
        "analyze_content":            row["analyze_content"],
        "analyze_behavioral":         row["analyze_behavioral"],
    }


async def _fetch_workspace_docs(tenant_id: str, workspace_id: str | None) -> list[dict]:
    """Pull document metadata + best-effort word_count from any OCR results.
    workspace_id=None → tenant-wide."""
    pool = await get_pool()
    where = "d.tenant_id = $1 AND d.deleted_at IS NULL"
    params: list[Any] = [tenant_id]
    if workspace_id:
        where += " AND d.workspace_id = $2"
        params.append(workspace_id)
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                f"""
                WITH doc_words AS (
                  SELECT v.tenant_id, v.document_id,
                         COALESCE(SUM(array_length(string_to_array(o.text_content, ' '), 1)), 0) AS word_count
                    FROM versions v
                    LEFT JOIN ocr_results o
                      ON o.tenant_id = v.tenant_id AND o.version_id = v.id
                   WHERE v.tenant_id = $1
                     AND v.is_current = true
                   GROUP BY v.tenant_id, v.document_id
                )
                SELECT d.id::text, d.workspace_id::text, d.created_by::text,
                       d.created_at, d.size_bytes,
                       COALESCE(d.document_class, '') AS document_class,
                       COALESCE(dw.word_count, 0) AS word_count
                  FROM documents d
                  LEFT JOIN doc_words dw
                    ON dw.tenant_id = d.tenant_id AND dw.document_id = d.id
                 WHERE {where}
                 LIMIT $%d
                """ % (len(params) + 1),
                *params, WORKSPACE_DOC_CAP + 1,
            )
    return [
        {
            "id":              r["id"],
            "workspace_id":    r["workspace_id"],
            "created_by":      r["created_by"],
            "created_at":      r["created_at"],
            "size_bytes":      int(r["size_bytes"] or 0),
            "document_class":  r["document_class"],
            "word_count":      int(r["word_count"] or 0),
        }
        for r in rows
    ]


async def _set_report_status(tenant_id: str, report_id: str, status: str) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE anomaly_reports
                   SET status = $3
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, report_id, status,
            )


async def _mark_report_failed(tenant_id: str, report_id: str, error: str) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE anomaly_reports
                   SET status = 'failed', error_message = $3,
                       completed_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, report_id, error[:1000],
            )


async def _persist_completion(
    *, tenant_id: str, report_id: str, total_docs: int,
    findings: list[dict], summary: dict, correlation_id: str,
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
                    INSERT INTO anomaly_findings
                        (tenant_id, report_id, document_id, anomaly_type,
                         severity, description, evidence, z_score, similarity_score)
                    VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9)
                    ON CONFLICT (tenant_id, report_id, document_id, anomaly_type) DO NOTHING
                    """,
                    tenant_id, report_id, f["document_id"], f["anomaly_type"],
                    f["severity"], f["description"],
                    json.dumps(f.get("evidence") or {}),
                    f.get("z_score"), f.get("similarity_score"),
                )

            await conn.execute(
                """
                UPDATE anomaly_reports
                   SET status          = 'completed',
                       total_documents = $3,
                       anomalies_found = $4,
                       summary         = $5::jsonb,
                       completed_at    = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, report_id, total_docs, len(findings),
                json.dumps(summary or {}),
            )

            payload = {
                "tenant_id": tenant_id,
                "report_id": report_id,
                "total_documents": total_docs,
                "anomalies_found": len(findings),
                "summary": summary,
                "correlation_id": correlation_id,
                "emitted_at": datetime.now(timezone.utc).isoformat(),
            }
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'anomaly_report', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                ANOMALY_COMPLETED_SUBJECT, report_id,
                json.dumps(payload),
            )
