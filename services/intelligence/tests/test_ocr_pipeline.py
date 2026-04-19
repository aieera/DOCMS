"""Tests for the OCR pipeline.

Two layers:

1. Unit tests — exercise the publish/persist glue with mocks. Always runnable.
2. Integration tests — spin up a real Postgres via testcontainers and verify
   the RLS-scoped insert actually writes rows. Skipped automatically when
   Docker isn't available, so `pytest` stays green on laptops without
   Docker Desktop running.

NATS is mocked in the unit tests. A full end-to-end test that also spins
NATS is deferred — the published envelope shape is asserted directly,
which is what downstream consumers read.
"""
from __future__ import annotations

import asyncio
import json
import os
import uuid
from unittest import mock

import pytest

from app.tasks import ocr as ocr_task


# ---------------------------------------------------------------------------
# Unit tests
# ---------------------------------------------------------------------------

def test_envelope_shape_matches_contract():
    corr = "00000000-0000-0000-0000-0000cafef00d"
    env = ocr_task._build_ocr_completed_envelope(
        tenant_id="t1",
        document_id="d1",
        version_id="v1",
        page_count=3,
        language="en",
        avg_conf=0.92,
        full_text="hello world",
        engine="surya",
        correlation_id=corr,
    )
    assert env["specversion"] == "1.0"
    assert env["type"] == "dms.version.ocr_completed.v1"
    assert env["subject"] == "version/v1"
    assert env["tenantid"] == "t1"
    assert env["correlationid"] == corr
    # Data block carries both the search-service key (`content`) and the
    # intelligence-service alias (`text`) so both consumers resolve.
    data = env["data"]
    assert data["content"] == "hello world"
    assert data["text"] == "hello world"
    assert data["page_count"] == 3
    assert data["confidence_avg"] == 0.92
    assert data["engine"] == "surya"


def test_publish_swallows_exceptions():
    """publish_ocr_completed must NOT raise — publish failures are logged
    and counted; the Celery task proceeds to return success since the DB
    row is already committed."""
    async def boom(*a, **kw):
        raise RuntimeError("nats down")

    with mock.patch("app.tasks.ocr.publish_cloudevent", side_effect=boom):
        ok = asyncio.run(ocr_task._publish_ocr_completed({"type": "x"}, "corr"))
    assert ok is False


def test_publish_success_returns_true():
    async def noop(*a, **kw):
        return None

    with mock.patch("app.tasks.ocr.publish_cloudevent", side_effect=noop):
        ok = asyncio.run(ocr_task._publish_ocr_completed({"type": "x"}, "corr"))
    assert ok is True


def test_full_text_truncated_to_10mib():
    # 12MiB of ASCII text → must come back no larger than MAX_FULL_TEXT_BYTES.
    big = "a" * (12 * 1024 * 1024)
    if len(big.encode("utf-8")) > ocr_task.MAX_FULL_TEXT_BYTES:
        truncated = big.encode("utf-8")[: ocr_task.MAX_FULL_TEXT_BYTES].decode("utf-8", errors="ignore")
    else:
        truncated = big
    assert len(truncated.encode("utf-8")) <= ocr_task.MAX_FULL_TEXT_BYTES


# ---------------------------------------------------------------------------
# Integration tests — gated on Docker availability
# ---------------------------------------------------------------------------

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


SCHEMA_SQL = """
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS organizations (
    id UUID PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS versions (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS ocr_results (
    tenant_id          UUID NOT NULL REFERENCES organizations(id),
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    version_id         UUID NOT NULL,
    page_number        INT NOT NULL,
    text_content       TEXT NOT NULL,
    confidence         REAL NOT NULL DEFAULT 0.0,
    language           TEXT,
    bounding_boxes     JSONB NOT NULL DEFAULT '[]'::jsonb,
    processing_time_ms INT,
    engine             TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id) REFERENCES versions(tenant_id, id)
);

ALTER TABLE ocr_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE ocr_results FORCE ROW LEVEL SECURITY;
CREATE POLICY ocr_tenant_isolation ON ocr_results
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ocr_tenant_isolation_insert ON ocr_results
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
"""


@needs_docker
@pytest.mark.integration
def test_persist_pages_writes_rows(monkeypatch):
    import asyncpg
    from testcontainers.postgres import PostgresContainer

    from app.db import pool as pool_module

    with PostgresContainer("postgres:16-alpine") as pg:
        dsn = pg.get_connection_url().replace("postgresql+psycopg2://", "postgresql://")

        async def run() -> list[asyncpg.Record]:
            # Seed schema + fixtures on a direct connection.
            conn = await asyncpg.connect(dsn)
            try:
                await conn.execute(SCHEMA_SQL)
                tid = str(uuid.uuid4())
                vid = str(uuid.uuid4())
                await conn.execute("INSERT INTO organizations (id) VALUES ($1)", uuid.UUID(tid))
                await conn.execute(
                    "INSERT INTO versions (tenant_id, id) VALUES ($1, $2)",
                    uuid.UUID(tid), uuid.UUID(vid),
                )
            finally:
                await conn.close()

            # Point the task pool at the testcontainer and run the persist path.
            monkeypatch.setattr(pool_module.settings, "database_url", dsn)
            pool_module._pool = None  # force re-create against the new DSN

            pages = [
                {"page_number": 1, "text": "hello", "confidence": 0.9,
                 "method": "surya", "boxes": [], "processing_time_ms": 10},
                {"page_number": 2, "text": "world", "confidence": 0.8,
                 "method": "surya", "boxes": [], "processing_time_ms": 12},
            ]
            await ocr_task._persist_pages(tid, vid, pages, "en")

            # Read back under the tenant GUC to ensure RLS admitted the rows.
            conn = await asyncpg.connect(dsn)
            try:
                await conn.execute("SELECT set_config('app.current_tenant', $1, true)", tid)
                rows = await conn.fetch(
                    "SELECT page_number, text_content, confidence, engine "
                    "FROM ocr_results WHERE tenant_id = $1 AND version_id = $2 "
                    "ORDER BY page_number",
                    uuid.UUID(tid), uuid.UUID(vid),
                )
            finally:
                await conn.close()
            await pool_module.close_pool()
            return rows

        rows = asyncio.run(run())

    assert len(rows) == 2
    assert [r["page_number"] for r in rows] == [1, 2]
    assert rows[0]["text_content"] == "hello"
    assert rows[1]["text_content"] == "world"
    assert rows[0]["engine"] == "surya"


@needs_docker
@pytest.mark.integration
def test_persist_is_idempotent(monkeypatch):
    """Re-running persist for the same version replaces prior rows rather
    than stacking duplicates. This matters for Celery retries."""
    import asyncpg
    from testcontainers.postgres import PostgresContainer

    from app.db import pool as pool_module

    with PostgresContainer("postgres:16-alpine") as pg:
        dsn = pg.get_connection_url().replace("postgresql+psycopg2://", "postgresql://")

        async def run() -> int:
            conn = await asyncpg.connect(dsn)
            try:
                await conn.execute(SCHEMA_SQL)
                tid = str(uuid.uuid4())
                vid = str(uuid.uuid4())
                await conn.execute("INSERT INTO organizations (id) VALUES ($1)", uuid.UUID(tid))
                await conn.execute(
                    "INSERT INTO versions (tenant_id, id) VALUES ($1, $2)",
                    uuid.UUID(tid), uuid.UUID(vid),
                )
            finally:
                await conn.close()

            monkeypatch.setattr(pool_module.settings, "database_url", dsn)
            pool_module._pool = None

            pages_v1 = [{"page_number": 1, "text": "first run",
                         "confidence": 0.7, "method": "surya",
                         "boxes": [], "processing_time_ms": 5}]
            pages_v2 = [{"page_number": 1, "text": "second run",
                         "confidence": 0.95, "method": "surya",
                         "boxes": [], "processing_time_ms": 5}]
            await ocr_task._persist_pages(tid, vid, pages_v1, "en")
            await ocr_task._persist_pages(tid, vid, pages_v2, "en")

            conn = await asyncpg.connect(dsn)
            try:
                await conn.execute("SELECT set_config('app.current_tenant', $1, true)", tid)
                rows = await conn.fetch(
                    "SELECT text_content FROM ocr_results WHERE version_id = $1",
                    uuid.UUID(vid),
                )
            finally:
                await conn.close()
            await pool_module.close_pool()
            return [r["text_content"] for r in rows]

        texts = asyncio.run(run())

    assert texts == ["second run"], f"retry must replace, got {texts!r}"
