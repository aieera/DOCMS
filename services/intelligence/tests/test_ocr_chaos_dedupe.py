"""Dedupe contract test: simulates a worker killed mid-batch.

This is the CI-runnable counterpart to the staging chaos scenario at
docs/chaos/scenarios/07-ocr-worker-kill.md. It does NOT exercise SIGKILL,
broker redelivery, or the Celery worker process — see the staging
scenario for that. What it DOES pin is the database-level invariant the
chaos test relies on:

    Given N events where the worker died after marking some completed
    and others only enqueued, a redelivery pass must produce exactly
    N completed rows and N "completed" emissions in total — never more,
    never fewer.

If `app.dedupe.already_processed` ever loses its `completed_only=True`
semantics, or the PK on (tenant_id, event_id) is dropped, this test
fails. That's the regression coverage we want in CI; the wall-clock
chaos scenario is what proves the contract end-to-end.
"""
from __future__ import annotations

import asyncio
import uuid

import pytest


def _docker_available() -> bool:
    try:
        import docker  # type: ignore
        docker.from_env().ping()
        return True
    except Exception:
        return False


needs_docker = pytest.mark.skipif(
    not _docker_available(),
    reason="testcontainers needs a running Docker daemon",
)


# Schema: organizations + the real ocr_processed_events table copied
# verbatim from services/intelligence/migrations/000001_ocr_processed_events.up.sql.
# Keeping it inline (rather than running the migration) avoids pulling
# golang-migrate into Python tests.
SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS organizations (id UUID PRIMARY KEY);

CREATE TABLE ocr_processed_events (
    tenant_id     UUID        NOT NULL REFERENCES organizations(id),
    event_id      UUID        NOT NULL,
    document_id   UUID        NOT NULL,
    version_id    UUID        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'enqueued'
                  CHECK (status IN ('enqueued', 'completed', 'failed')),
    attempts      INT         NOT NULL DEFAULT 0,
    last_error    TEXT,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, event_id)
);

ALTER TABLE ocr_processed_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY ocr_processed_events_tenant_isolation ON ocr_processed_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
"""


N_EVENTS = 100
KILL_AFTER = 50  # Worker dies after completing the first 50 of N_EVENTS.


@needs_docker
@pytest.mark.integration
def test_redelivery_after_midbatch_crash_produces_exactly_n_completions(monkeypatch):
    import asyncpg
    from testcontainers.postgres import PostgresContainer

    from app.db import pool as pool_module
    from app.dedupe import already_processed, mark_completed, mark_enqueued

    with PostgresContainer("postgres:16-alpine") as pg:
        dsn = pg.get_connection_url().replace(
            "postgresql+psycopg2://", "postgresql://"
        )

        async def run() -> dict[str, int]:
            # Seed schema + tenant.
            conn = await asyncpg.connect(dsn)
            try:
                await conn.execute(SCHEMA_SQL)
                tenant = uuid.uuid4()
                await conn.execute(
                    "INSERT INTO organizations (id) VALUES ($1)", tenant
                )
            finally:
                await conn.close()

            monkeypatch.setattr(pool_module.settings, "database_url", dsn)
            pool_module._pool = None  # force recreation against testcontainer

            # Synthesize a batch of N events. Each represents one
            # dms.version.uploaded.v1 message.
            events = [
                {
                    "event_id": str(uuid.uuid4()),
                    "document_id": str(uuid.uuid4()),
                    "version_id": str(uuid.uuid4()),
                }
                for _ in range(N_EVENTS)
            ]
            tenant_str = str(tenant)

            # ---- Pass 1: worker processes all N, but dies after KILL_AFTER ----
            # Every event reaches mark_enqueued (matches what the consumer
            # does on receipt). The first KILL_AFTER also reach
            # mark_completed (worker finished them); the remainder are
            # "in flight" when the SIGKILL lands.
            for ev in events:
                await mark_enqueued(
                    tenant_id=tenant_str,
                    event_id=ev["event_id"],
                    document_id=ev["document_id"],
                    version_id=ev["version_id"],
                )
            for ev in events[:KILL_AFTER]:
                await mark_completed(tenant_id=tenant_str, event_id=ev["event_id"])

            # ---- Pass 2: NATS redelivers all N events to the new worker ----
            # The dedupe gate (`completed_only=True`) is the contract:
            # rows in 'enqueued' must re-process; rows in 'completed' must
            # be skipped.
            second_emits = 0
            dedupe_hits = 0
            for ev in events:
                if await already_processed(
                    tenant_str, ev["event_id"], completed_only=True
                ):
                    dedupe_hits += 1
                    continue
                # New worker re-runs OCR and marks completed.
                await mark_completed(
                    tenant_id=tenant_str, event_id=ev["event_id"]
                )
                second_emits += 1

            # ---- Read back final state ----
            conn = await asyncpg.connect(dsn)
            try:
                await conn.execute(
                    "SELECT set_config('app.current_tenant', $1, true)", tenant_str
                )
                completed = await conn.fetchval(
                    "SELECT COUNT(*) FROM ocr_processed_events "
                    "WHERE tenant_id=$1 AND status='completed'",
                    tenant,
                )
                enqueued = await conn.fetchval(
                    "SELECT COUNT(*) FROM ocr_processed_events "
                    "WHERE tenant_id=$1 AND status='enqueued'",
                    tenant,
                )
                # PK already prevents duplicates; this query catches PK regression.
                dupes = await conn.fetchval(
                    "SELECT COUNT(*) FROM ("
                    "  SELECT 1 FROM ocr_processed_events "
                    "   WHERE tenant_id=$1 GROUP BY event_id HAVING COUNT(*) > 1"
                    ") d",
                    tenant,
                )
            finally:
                await conn.close()
                await pool_module.close_pool()

            return {
                "completed": completed,
                "enqueued": enqueued,
                "dupes": dupes,
                "dedupe_hits": dedupe_hits,
                "second_emits": second_emits,
            }

        result = asyncio.run(run())

    # The four invariants from docs/chaos/scenarios/07-ocr-worker-kill.md:
    assert result["dupes"] == 0, "PK regression: duplicate dedupe rows"
    assert result["completed"] == N_EVENTS, (
        f"expected {N_EVENTS} completed rows, got {result['completed']} "
        "— means a redelivered event was lost"
    )
    assert result["enqueued"] == 0, (
        f"{result['enqueued']} rows stuck in 'enqueued' — queue would block"
    )
    # Total emissions = first-pass completions + second-pass completions.
    # Must equal N_EVENTS exactly: no duplicate dms.ocr.completed.v1.
    total_emits = KILL_AFTER + result["second_emits"]
    assert total_emits == N_EVENTS, (
        f"expected exactly {N_EVENTS} dms.ocr.completed.v1 emissions, got {total_emits}"
    )
    # And dedupe must have suppressed the redelivery for the first batch.
    assert result["dedupe_hits"] == KILL_AFTER, (
        f"expected {KILL_AFTER} dedupe hits, got {result['dedupe_hits']} "
        "— dedupe gate is broken"
    )
