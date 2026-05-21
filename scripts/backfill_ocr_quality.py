"""Backfill OCR quality scores for documents that have OCR results but no
quality score row.

Symptom this fixes: the OCR worker publishes
`dms.version.ocr_completed.v1` AFTER writing ocr_results, and publish
failures are caught + logged but never raised (see
services/intelligence/app/tasks/ocr.py::_publish_ocr_completed). If
NATS hiccups between the DB commit and the publish, OCR finishes
silently and the downstream consumers (lang_detect, ocr_quality,
classify, NER, embed) never fire for that version.

This script:
  1. queries every tenant + version_id that has rows in `ocr_results`
     but no matching rows in `ocr_quality_scores`,
  2. enqueues `app.tasks.ocr_quality.score` for each one, using the
     same task signature the nats consumer uses.

Run inside the intelligence-worker container so the celery client +
DB pool config are picked up from the worker's env:

  docker exec -i vaultdms-intelligence-worker-misc \
    python /app/scripts/backfill_ocr_quality.py

The script is idempotent: ocr_quality.score's intel_dedupe layer keys
on event_id, so a second run for the same (tenant, version) is a
no-op as long as the previous attempt either succeeded or its dedupe
row is still around.
"""
from __future__ import annotations

import argparse
import logging
import os
import sys
import uuid
from typing import Iterable

import asyncpg

# The worker container has /app on PYTHONPATH; the script also needs to
# work when invoked as `python scripts/backfill_ocr_quality.py` from
# repo root, hence both insertions.
_APP_ROOT = os.environ.get("APP_ROOT", "/app")
if _APP_ROOT not in sys.path:
    sys.path.insert(0, _APP_ROOT)


logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("backfill_ocr_quality")


async def find_missing(dsn: str, tenant_id: str | None) -> list[tuple[str, str, str]]:
    """Return [(tenant_id, document_id, version_id)] needing scoring."""
    where_tenant = "AND r.tenant_id = $1" if tenant_id else ""
    # NOTE: SELECT DISTINCT is sufficient (no ORDER BY) — pg requires
    # the order columns to be in the projection, and we don't care
    # about ordering for an enqueue batch.
    q = f"""
        SELECT DISTINCT r.tenant_id::text, v.document_id::text, r.version_id::text
        FROM ocr_results r
        JOIN document_versions v
          ON v.tenant_id = r.tenant_id AND v.id = r.version_id
        WHERE r.version_id NOT IN (
            SELECT version_id FROM ocr_quality_scores
            WHERE tenant_id = COALESCE($1, ocr_quality_scores.tenant_id)
        )
        {where_tenant}
    """
    conn = await asyncpg.connect(dsn)
    try:
        rows = await conn.fetch(q, tenant_id)
    finally:
        await conn.close()
    return [(r[0], r[1], r[2]) for r in rows]


def enqueue(tasks: Iterable[tuple[str, str, str]], dry_run: bool) -> int:
    """Enqueue ocr_quality.score for each (tenant, doc, version)."""
    if dry_run:
        for tenant_id, document_id, version_id in tasks:
            log.info("DRY-RUN tenant=%s doc=%s version=%s", tenant_id, document_id, version_id)
        return 0

    # Import lazily so dry-run mode works on hosts without celery installed.
    from app.tasks.ocr_quality import score as ocr_quality_score

    queued = 0
    for tenant_id, document_id, version_id in tasks:
        # Mint a fresh event_id per enqueue. The dedupe layer keys on it,
        # so each backfill attempt is a separate logical event.
        event_id = str(uuid.uuid4())
        correlation_id = str(uuid.uuid4())
        ocr_quality_score.apply_async(
            kwargs={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "event_id": event_id,
                "correlation_id": correlation_id,
            },
            # Must match the queue nats_consumer uses (see
            # services/intelligence/app/nats_consumer.py
            # ::_on_ocr_quality_trigger) — the worker subscribes to
            # `intelligence`, `intelligence-embed`, `intelligence-rag`.
            # `misc` looks tempting because there's a separate misc
            # worker container, but it doesn't bind that queue.
            queue="intelligence",
        )
        queued += 1
        log.info("queued tenant=%s doc=%s version=%s event=%s",
                 tenant_id, document_id, version_id, event_id)
    return queued


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--tenant", default=None,
        help="Tenant UUID to limit the scan to. Omit to backfill across all tenants.",
    )
    parser.add_argument(
        "--dry-run", action="store_true",
        help="List missing versions without enqueueing.",
    )
    parser.add_argument(
        "--dsn", default=os.environ.get("VAULTDMS_DB_DSN"),
        help="Postgres DSN. Falls back to $VAULTDMS_DB_DSN.",
    )
    args = parser.parse_args()

    if not args.dsn:
        log.error("Missing --dsn and $VAULTDMS_DB_DSN unset")
        return 2

    import asyncio
    missing = asyncio.run(find_missing(args.dsn, args.tenant))
    log.info("found %d version(s) needing scoring", len(missing))
    if not missing:
        return 0

    queued = enqueue(missing, dry_run=args.dry_run)
    if args.dry_run:
        log.info("DRY-RUN complete; would enqueue %d task(s)", len(missing))
    else:
        log.info("enqueued %d task(s); the misc worker should consume them shortly", queued)
    return 0


if __name__ == "__main__":
    sys.exit(main())
