"""Persistence helpers for classify + NER.

Kept small and side-effect-free so they can be unit-tested in
isolation and so a test run against testcontainers is the only way
to exercise the SQL paths.
"""
from __future__ import annotations

import json
from typing import Any, Iterable

from app.db.pool import get_pool


async def upsert_classification(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    category_key: str,
    confidence: float,
    method: str,
    model_version: str = "",
    top3: list[dict[str, Any]] | None = None,
) -> None:
    """Insert or replace the classification for this version."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO document_classifications
                    (tenant_id, version_id, document_id, category_key,
                     confidence, method, model_version, top3, classified_at)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, NOW())
                ON CONFLICT (tenant_id, version_id) DO UPDATE
                  SET category_key = EXCLUDED.category_key,
                      confidence    = EXCLUDED.confidence,
                      method        = EXCLUDED.method,
                      model_version = EXCLUDED.model_version,
                      top3          = EXCLUDED.top3,
                      classified_at = NOW()
                """,
                tenant_id,
                version_id,
                document_id,
                category_key,
                float(confidence),
                method,
                model_version,
                json.dumps(top3 or []),
            )


async def replace_entities(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    entities: Iterable[dict[str, Any]],
) -> int:
    """Delete and re-insert entities for this version in one tx.
    Returns the number of rows inserted. Same idempotency strategy as
    the OCR task's _persist_pages."""
    inserted = 0
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                "DELETE FROM document_entities WHERE tenant_id=$1 AND version_id=$2",
                tenant_id,
                version_id,
            )
            for e in entities:
                await conn.execute(
                    """
                    INSERT INTO document_entities
                        (tenant_id, id, version_id, document_id, entity_type,
                         entity_value, start_offset, end_offset, confidence,
                         is_pii, source, detected_at)
                    VALUES ($1, gen_random_uuid(), $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
                    """,
                    tenant_id,
                    version_id,
                    document_id,
                    e["entity_type"],
                    str(e.get("entity_value", ""))[:500],
                    int(e.get("start_offset", 0)),
                    int(e.get("end_offset", 0)),
                    float(e.get("confidence", 0.0)),
                    bool(e.get("is_pii", False)),
                    str(e.get("source", "spacy")),
                )
                inserted += 1
    return inserted
