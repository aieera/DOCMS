"""ADR 0115 — per-stage processing status writer.

Each intelligence task records its progress into
`document_processing_stages` (running → completed/failed/skipped) so the
document service can surface "processing failed because X, retry" instead
of empty tabs. The roll-up column `documents.processing_status` is
recomputed from the per-stage rows on every transition.

Design rules:
  * **Never break the task.** Instrumentation is best-effort — every
    public helper swallows and logs its own errors. A failure to record
    a stage must not fail OCR/classify/etc.
  * **RLS-safe.** `document_processing_stages` FORCEs row-level security
    (migration 000052); we set `app.current_tenant` inside the same tx,
    mirroring app/persist.py, so writes aren't silently dropped.
  * **Feature-flagged.** SEDOC_PROCESSING_FAILURE_TRACKING=false makes
    every helper a no-op, matching the ADR rollback path.
"""
from __future__ import annotations

import asyncio
import logging
import os

from app.db.pool import get_pool

log = logging.getLogger(__name__)

# ADR 0115 failure-reason taxonomy. Stable codes — the UI maps these to
# localized copy and dashboards group on them, so resist renaming.
REASON_UNSUPPORTED_MIME = "unsupported_mime"
REASON_FILE_CORRUPTED = "file_corrupted"
REASON_DEPENDENCY_UNAVAILABLE = "dependency_unavailable"
REASON_DEPENDENCY_QUOTA = "dependency_quota"
REASON_TIMEOUT = "timeout"
REASON_DECRYPT_FAILED = "decrypt_failed"
REASON_EVENT_PUBLISH_FAILED = "event_publish_failed"
REASON_WORKER_CRASH = "worker_crash"
REASON_POLICY_DENIED = "policy_denied"
REASON_UNKNOWN = "unknown"


def _enabled() -> bool:
    return os.getenv("SEDOC_PROCESSING_FAILURE_TRACKING", "true").lower() != "false"


def ocr_failure_reason(exc: Exception) -> str:
    """Map an OCR exception to a taxonomy code. OCR-specific because the
    terminal-error reasons (`parse_error`, `decrypt_error`) come from
    TerminalOCRError; generic stages can pass their own code instead.
    """
    reason = getattr(exc, "reason", "")
    if reason == "parse_error":
        return REASON_FILE_CORRUPTED
    if reason in ("decrypt_error", "decrypt_failed"):
        return REASON_DECRYPT_FAILED
    name = type(exc).__name__.lower()
    msg = str(exc).lower()
    if "softtimelimit" in name or "timelimit" in name or "timeout" in msg:
        return REASON_TIMEOUT
    if "ratelimit" in msg or "quota" in msg or "429" in msg:
        return REASON_DEPENDENCY_QUOTA
    if "connection" in msg or "unavailable" in msg or "refused" in msg:
        return REASON_DEPENDENCY_UNAVAILABLE
    return REASON_UNKNOWN


def _rollup(statuses: set[str]) -> str:
    if "failed" in statuses and "completed" in statuses:
        return "partial"
    if "failed" in statuses:
        return "failed"
    if "running" in statuses:
        return "running"
    if statuses and statuses <= {"completed", "skipped"}:
        return "completed"
    return "pending"


async def _record(
    tenant_id: str,
    document_id: str,
    version_id: str,
    stage: str,
    status: str,
    failure_reason: str | None,
    failure_detail: str | None,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            # Upsert the (tenant, document, stage) row. attempts increments
            # on every running transition; timestamps land per status.
            await conn.execute(
                """
                INSERT INTO document_processing_stages
                    (tenant_id, document_id, version_id, stage, status,
                     attempts, failure_reason, failure_detail,
                     started_at, completed_at)
                VALUES ($1,$2,$3,$4,$5,
                        CASE WHEN $5='running' THEN 1 ELSE 0 END,
                        $6,$7,
                        CASE WHEN $5='running' THEN now() END,
                        CASE WHEN $5 IN ('completed','failed','skipped') THEN now() END)
                ON CONFLICT (tenant_id, document_id, stage) DO UPDATE SET
                    version_id     = EXCLUDED.version_id,
                    status         = EXCLUDED.status,
                    attempts       = document_processing_stages.attempts
                                     + CASE WHEN EXCLUDED.status='running' THEN 1 ELSE 0 END,
                    failure_reason = EXCLUDED.failure_reason,
                    failure_detail = EXCLUDED.failure_detail,
                    started_at     = COALESCE(document_processing_stages.started_at,
                                              EXCLUDED.started_at),
                    completed_at   = EXCLUDED.completed_at
                """,
                tenant_id, document_id, version_id, stage, status,
                failure_reason, failure_detail,
            )
            rows = await conn.fetch(
                "SELECT status FROM document_processing_stages "
                "WHERE tenant_id=$1 AND document_id=$2",
                tenant_id, document_id,
            )
            rollup = _rollup({r["status"] for r in rows})
            await conn.execute(
                "UPDATE documents SET processing_status=$3 "
                "WHERE tenant_id=$1 AND id=$2",
                tenant_id, document_id, rollup,
            )


def record_stage(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    stage: str,
    status: str,
    failure_reason: str | None = None,
    failure_detail: str | None = None,
) -> None:
    """Synchronous, fire-and-forget stage write for Celery tasks. Runs
    its own event loop (Celery tasks are sync) and swallows every error
    so instrumentation can never break the underlying task.
    """
    if not _enabled():
        return
    # `failure_detail` is operator-facing; keep it short and avoid
    # dumping unbounded exception text into a displayed column.
    if failure_detail and len(failure_detail) > 500:
        failure_detail = failure_detail[:497] + "..."
    try:
        asyncio.run(
            _record(
                tenant_id, document_id, version_id, stage, status,
                failure_reason, failure_detail,
            )
        )
    except Exception:  # noqa: BLE001 — instrumentation must never raise
        log.warning(
            "processing_stages: failed to record stage=%s status=%s doc=%s",
            stage, status, document_id, exc_info=True,
        )
