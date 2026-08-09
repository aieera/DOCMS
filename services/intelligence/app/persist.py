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


# A human correction writes classification_confidence = 1.0 (see
# services/document/internal/service/classify_correction_service.go). Any
# document at or above this is human-owned and automation must not touch it.
HUMAN_CONFIDENCE = 1.0


async def apply_document_class(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    category_key: str,
    confidence: float,
) -> bool:
    """Write a high-confidence classification onto `documents.document_class`.

    Until this existed the classify pipeline only ever wrote
    `document_classifications`; nothing propagated the result to the
    document row, so `documents.document_class` stayed '' forever and every
    document rendered as "Unclassified" / grouped under "(uncategorized)"
    no matter how confident the classifier was (BUG-30).

    Never overwrites a label a person owns. The UPDATE only fires when the
    document is still unclassified, or when its current label is exactly
    the one automation last wrote for an *earlier* version (so a re-upload
    can correct a stale auto-label), and never when
    classification_confidence has been pinned to 1.0 by a human correction.

    Returns True when a row was updated.
    """
    if not category_key:
        return False
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            prev = await conn.fetchval(
                """
                SELECT category_key
                  FROM document_classifications
                 WHERE tenant_id = $1 AND document_id = $2 AND version_id <> $3
                 ORDER BY classified_at DESC
                 LIMIT 1
                """,
                tenant_id, document_id, version_id,
            )
            tag = await conn.execute(
                """
                UPDATE documents
                   SET document_class = $3,
                       classification_confidence = $4,
                       updated_at = now()
                 WHERE tenant_id = $1
                   AND id = $2
                   AND deleted_at IS NULL
                   AND classification_confidence < $5
                   AND (COALESCE(document_class, '') = ''
                        OR ($6::text IS NOT NULL AND document_class = $6))
                """,
                tenant_id, document_id, category_key, float(confidence),
                HUMAN_CONFIDENCE, prev,
            )
    return tag.endswith(" 1")


async def load_ocr_text(*, tenant_id: str, version_id: str) -> str:
    """Concatenate a version's OCR pages back into one document string,
    in page order. Returns "" when no OCR rows exist yet."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute(
            "SELECT set_config('app.current_tenant', $1, true)", tenant_id
        )
        rows = await conn.fetch(
            """
            SELECT text_content FROM ocr_results
             WHERE tenant_id = $1 AND version_id = $2
             ORDER BY page_number ASC
            """,
            tenant_id, version_id,
        )
    return "\n\n".join((r["text_content"] or "") for r in rows).strip()


# ---- WS3 pre-commit ingestion pipeline -------------------------------------

# Statuses past which process_ingestion must NOT re-run (idempotency).
_INGESTION_DONE_STATES = ("processed", "routed", "needs_review", "committed", "rejected")


async def get_ingestion_status(*, tenant_id: str, ingestion_item_id: str) -> str:
    """Return the current status of a staged ingestion item ("" if missing)."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute(
            "SELECT set_config('app.current_tenant', $1, true)", tenant_id
        )
        row = await conn.fetchrow(
            "SELECT status FROM ingestion_items WHERE tenant_id = $1 AND id = $2",
            tenant_id, ingestion_item_id,
        )
    return (row["status"] if row else "") or ""


async def mark_ingestion_status(
    *, tenant_id: str, ingestion_item_id: str, status: str, failure_reason: str = ""
) -> None:
    """Flip a staged item's status (e.g. → ocr_running). Never moves a terminal
    item backwards."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute(
            "SELECT set_config('app.current_tenant', $1, true)", tenant_id
        )
        await conn.execute(
            """
            UPDATE ingestion_items
               SET status = $3, failure_reason = $4, updated_at = now()
             WHERE tenant_id = $1 AND id = $2
               AND status NOT IN ('committed','rejected')
            """,
            tenant_id, ingestion_item_id, status, failure_reason,
        )


async def write_ingestion_result(
    *,
    tenant_id: str,
    ingestion_item_id: str,
    full_text: str,
    page_count: int,
    confidence_avg: float,
    engine: str,
    external_key: str,
    confidence: float,
) -> bool:
    """Persist OCR + extraction output for a staged item and flip it to
    'processed'. Upserts the ingestion_ocr sidecar (ocr_result_ref → it) so a
    redelivery overwrites rather than duplicates. Returns False (no-op) when the
    item is already terminal. Idempotent on ingestion_item.id.
    """
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            ocr_id = await conn.fetchval(
                """
                INSERT INTO ingestion_ocr
                    (tenant_id, id, ingestion_item_id, full_text, page_count,
                     confidence_avg, engine, created_at)
                VALUES ($1, gen_random_uuid(), $2, $3, $4, $5, $6, NOW())
                ON CONFLICT (tenant_id, ingestion_item_id) DO UPDATE
                  SET full_text = EXCLUDED.full_text,
                      page_count = EXCLUDED.page_count,
                      confidence_avg = EXCLUDED.confidence_avg,
                      engine = EXCLUDED.engine,
                      created_at = NOW()
                RETURNING id
                """,
                tenant_id, ingestion_item_id, full_text or "", int(page_count),
                float(confidence_avg), engine or "",
            )
            tag = await conn.execute(
                """
                UPDATE ingestion_items
                   SET ocr_result_ref = $3,
                       extracted_external_key = $4,
                       confidence = $5,
                       status = 'processed',
                       updated_at = now()
                 WHERE tenant_id = $1 AND id = $2
                   AND status NOT IN ('committed','rejected','needs_review','routed')
                """,
                tenant_id, ingestion_item_id, ocr_id,
                (external_key or "")[:255], float(confidence),
            )
    # asyncpg returns e.g. "UPDATE 1" — non-zero means we advanced the item.
    return tag.endswith(" 1")


async def replace_extracted_fields(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    document_class: str,
    fields: Iterable[dict[str, Any]],
) -> int:
    """Delete + re-insert the per-field extraction rows for this version in
    one tx (same idempotency strategy as replace_entities). Each field dict is
    {field_key, value, confidence, method}. Returns the row count inserted."""
    inserted = 0
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                "DELETE FROM extracted_fields WHERE tenant_id=$1 AND version_id=$2",
                tenant_id, version_id,
            )
            for f in fields:
                key = str(f.get("field_key", "")).strip()
                if not key:
                    continue
                await conn.execute(
                    """
                    INSERT INTO extracted_fields
                        (tenant_id, id, version_id, document_id, document_class,
                         field_key, value, confidence, method, extracted_at)
                    VALUES ($1, gen_random_uuid(), $2, $3, $4, $5, $6, $7, $8, NOW())
                    """,
                    tenant_id,
                    version_id,
                    document_id,
                    document_class,
                    key,
                    (str(f["value"])[:2000] if f.get("value") is not None else None),
                    float(f.get("confidence", 0.0)),
                    str(f.get("method", "regex")),
                )
                inserted += 1
    return inserted


async def merge_custom_metadata(
    *,
    tenant_id: str,
    document_id: str,
    values: dict[str, Any],
) -> None:
    """Shallow-merge `values` into documents.custom_metadata (jsonb `||`).

    Used to mirror the key extracted business fields onto the document so the
    routing step + UI + search can read them without joining extracted_fields.
    No-op when `values` is empty. RLS-scoped; the documents metadata size CHECK
    bounds the payload."""
    if not values:
        return
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute(
            "SELECT set_config('app.current_tenant', $1, true)", tenant_id
        )
        await conn.execute(
            """
            UPDATE documents
               SET custom_metadata = COALESCE(custom_metadata, '{}'::jsonb) || $3::jsonb,
                   updated_at = now()
             WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
            """,
            tenant_id, document_id, json.dumps(values),
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
