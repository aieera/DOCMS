"""Persistence helpers for ADR 0080 — rag_query_log + workspace_ai_settings.

The /rag/query endpoint uses these to:
  - resolve workspace_ai_settings (toggle, model, per-day quota),
  - count today's queries for the user (rate-limit check),
  - compute the allowed doc-id set for permission-filtered retrieval,
  - persist the query + result snapshot for audit + thumbs feedback.

Same set_config('app.current_tenant', $1) pattern as qa_persist.py.
"""
from __future__ import annotations

import json
import logging
import uuid
from typing import Any

from app.db.pool import get_pool

log = logging.getLogger(__name__)

# Tenant default when no workspace_ai_settings row exists for the
# workspace (or the call is tenant-wide). Mirrors the column defaults
# in migrations/000025_rag_pipeline.up.sql so behavior is the same
# whether or not an admin has touched the settings UI yet.
DEFAULT_RAG_QUERIES_PER_DAY = 200
DEFAULT_ANSWER_MODEL = "anthropic/claude-haiku-4-5"


async def get_workspace_ai_settings(
    *, tenant_id: str, workspace_id: str | None,
) -> dict[str, Any]:
    """Return {rag_enabled, answer_model, rag_queries_per_day} for the
    workspace.

    answer_model is None on the fallback paths (no row / tenant-wide
    query): None means "defer to the tenant's ADR-0081 LLM routing".
    Forcing DEFAULT_ANSWER_MODEL here overrode the tenant's configured
    provider on every /rag/query — an OpenAI tenant was pushed onto an
    anthropic model. Only an explicitly stored row pins a model."""
    if not workspace_id:
        return {
            "rag_enabled": True,
            "answer_model": None,
            "rag_queries_per_day": DEFAULT_RAG_QUERIES_PER_DAY,
        }
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT rag_enabled, answer_model, rag_queries_per_day
                  FROM workspace_ai_settings
                 WHERE tenant_id = $1 AND workspace_id = $2
                """,
                tenant_id, workspace_id,
            )
    if not row:
        return {
            "rag_enabled": True,
            "answer_model": None,  # defer to tenant LLM routing
            "rag_queries_per_day": DEFAULT_RAG_QUERIES_PER_DAY,
        }
    # A stored answer_model equal to the schema default is NOT an
    # explicit choice — the upsert COALESCEs it in whenever an admin
    # saves without touching the model field. Deferring it to tenant
    # routing is identical for anthropic-configured tenants and fixes
    # the provider mismatch for everyone else.
    stored_model = row["answer_model"]
    if stored_model == DEFAULT_ANSWER_MODEL:
        stored_model = None
    return {
        "rag_enabled": bool(row["rag_enabled"]),
        "answer_model": stored_model,
        "rag_queries_per_day": int(row["rag_queries_per_day"]),
    }


async def count_user_queries_24h(*, tenant_id: str, user_id: str) -> int:
    """Rolling-24h count for the per-user quota check. Uses the
    idx_rag_query_log_user_window index — the predicate orders by
    created_at DESC so the planner can stop scanning as soon as it
    crosses the 24h boundary on a busy tenant."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT count(*) AS n
                  FROM rag_query_log
                 WHERE tenant_id = $1
                   AND user_id   = $2
                   AND created_at >= now() - interval '24 hours'
                """,
                tenant_id, user_id,
            )
    return int(row["n"]) if row else 0


async def list_allowed_doc_ids(
    *, tenant_id: str, user_id: str, workspace_id: str | None,
) -> list[str]:
    """Compute the set of document ids the user can read. Used to
    pre-filter Qdrant retrieval (ADR 0080 §"Permission-filtered
    retrieval").

    Workspace-scoped: docs in that workspace, gated by membership.
    Tenant-wide (workspace_id is None): docs across every workspace
    the user is a member of.

    Returns an empty list when the user has no access — workspace_query
    short-circuits to an "I don't know." answer in that case."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            if workspace_id:
                # Membership gate: if the user isn't a member, return
                # zero doc-ids (denying retrieval rather than 403'ing —
                # the answer becomes "I don't know." which doesn't leak
                # whether the workspace exists).
                member = await conn.fetchrow(
                    """
                    SELECT 1 FROM workspace_members
                     WHERE tenant_id = $1 AND workspace_id = $2 AND user_id = $3
                    """,
                    tenant_id, workspace_id, user_id,
                )
                if not member:
                    return []
                rows = await conn.fetch(
                    """
                    SELECT id::text AS id FROM documents
                     WHERE tenant_id = $1 AND workspace_id = $2
                       AND deleted_at IS NULL
                    """,
                    tenant_id, workspace_id,
                )
            else:
                rows = await conn.fetch(
                    """
                    SELECT d.id::text AS id
                      FROM documents d
                      JOIN workspace_members wm
                        ON wm.tenant_id = d.tenant_id
                       AND wm.workspace_id = d.workspace_id
                     WHERE d.tenant_id = $1
                       AND wm.user_id  = $2
                       AND d.deleted_at IS NULL
                    """,
                    tenant_id, user_id,
                )
    return [r["id"] for r in rows]


async def get_document_titles(
    *, tenant_id: str, doc_ids: list[str],
) -> dict[str, str]:
    """Map document id -> human-readable title for the given ids.

    Used by workspace_query to put titles (not bare UUIDs) into the LLM
    context and citations — without this the model can't answer
    "what documents are available?" and citations render as a bare
    "Page 1" with no source name (ADR 0080 §6.8). Looked up fresh at
    query time so it reflects renames and works for chunks embedded
    before titles were stored in the Qdrant payload.

    Returns {} on empty input or lookup failure; callers fall back to
    the doc id."""
    ids = [d for d in dict.fromkeys(doc_ids) if d]  # de-dupe, drop blanks
    if not ids:
        return {}
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT id::text AS id, title
                  FROM documents
                 WHERE tenant_id = $1 AND id = ANY($2::uuid[])
                """,
                tenant_id, ids,
            )
    return {r["id"]: r["title"] for r in rows if r["title"]}


def _truncate(text: str | None, limit: int = 4000) -> str:
    if not text:
        return ""
    if len(text) <= limit:
        return text
    return text[: limit - 1] + "…"  # ellipsis


async def insert_query_log(
    *,
    tenant_id: str,
    user_id: str,
    workspace_id: str | None,
    question: str,
    result: dict[str, Any],
    retrieval_mode: str = "hybrid",
) -> str:
    """Append the query + result snapshot to rag_query_log. Returns
    the new row id so the caller can echo it to the client for
    follow-up thumbs feedback."""
    qid = uuid.uuid4()
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO rag_query_log
                    (tenant_id, id, user_id, workspace_id,
                     query, answer, citations,
                     model, retrieval_mode,
                     input_tokens, output_tokens, cost_usd, elapsed_ms)
                VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb,
                        $8, $9, $10, $11, $12, $13)
                """,
                tenant_id, qid, user_id,
                workspace_id if workspace_id else None,
                _truncate(question),
                _truncate(result.get("answer") or ""),
                json.dumps(result.get("citations") or []),
                result.get("model") or "",
                retrieval_mode,
                int(result.get("input_tokens") or 0),
                int(result.get("output_tokens") or 0),
                float(result.get("cost_usd") or 0.0),
                int(result.get("elapsed_ms") or 0),
            )
    return str(qid)


async def upsert_workspace_ai_settings(
    *,
    tenant_id: str,
    workspace_id: str,
    rag_enabled: bool | None = None,
    answer_model: str | None = None,
    embedding_model: str | None = None,
    rag_queries_per_day: int | None = None,
) -> dict[str, Any]:
    """Insert-or-update the workspace_ai_settings row. Only the
    supplied fields are written; the rest fall back to the existing
    row's values (or schema defaults on first write). Returns the
    row state after the upsert so the UI can re-render without a
    second GET."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            # Upsert in one statement so concurrent admins don't race.
            row = await conn.fetchrow(
                """
                INSERT INTO workspace_ai_settings
                    (tenant_id, workspace_id, rag_enabled, answer_model,
                     embedding_model, rag_queries_per_day, updated_at)
                VALUES ($1, $2,
                        COALESCE($3, TRUE),
                        COALESCE($4, 'anthropic/claude-haiku-4-5'),
                        COALESCE($5, 'bge-large-en-v1.5'),
                        COALESCE($6, 200),
                        now())
                ON CONFLICT (tenant_id, workspace_id) DO UPDATE
                   SET rag_enabled         = COALESCE($3, workspace_ai_settings.rag_enabled),
                       answer_model        = COALESCE($4, workspace_ai_settings.answer_model),
                       embedding_model     = COALESCE($5, workspace_ai_settings.embedding_model),
                       rag_queries_per_day = COALESCE($6, workspace_ai_settings.rag_queries_per_day),
                       updated_at          = now()
                RETURNING rag_enabled, answer_model, embedding_model,
                          rag_queries_per_day, updated_at
                """,
                tenant_id, workspace_id,
                rag_enabled, answer_model, embedding_model, rag_queries_per_day,
            )
    return {
        "rag_enabled": bool(row["rag_enabled"]),
        "answer_model": row["answer_model"],
        "embedding_model": row["embedding_model"],
        "rag_queries_per_day": int(row["rag_queries_per_day"]),
        "updated_at": row["updated_at"].isoformat() if row["updated_at"] else None,
    }


async def get_workspace_ai_settings_full(
    *, tenant_id: str, workspace_id: str,
) -> dict[str, Any]:
    """Same shape as upsert_workspace_ai_settings returns. When no row
    exists yet, returns the schema defaults so the UI can render a
    consistent form before the admin's first save."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT rag_enabled, answer_model, embedding_model,
                       rag_queries_per_day, updated_at
                  FROM workspace_ai_settings
                 WHERE tenant_id = $1 AND workspace_id = $2
                """,
                tenant_id, workspace_id,
            )
    if not row:
        return {
            "rag_enabled": True,
            "answer_model": DEFAULT_ANSWER_MODEL,
            "embedding_model": "bge-large-en-v1.5",
            "rag_queries_per_day": DEFAULT_RAG_QUERIES_PER_DAY,
            "updated_at": None,
        }
    return {
        "rag_enabled": bool(row["rag_enabled"]),
        "answer_model": row["answer_model"],
        "embedding_model": row["embedding_model"],
        "rag_queries_per_day": int(row["rag_queries_per_day"]),
        "updated_at": row["updated_at"].isoformat() if row["updated_at"] else None,
    }


async def record_feedback(
    *,
    tenant_id: str,
    query_id: str,
    user_id: str,
    feedback: str,
    note: str | None = None,
) -> bool:
    """Set feedback on a query the user owns. Returns False when no
    matching row (wrong tenant, wrong owner, or unknown id) — caller
    should 404 in that case."""
    if feedback not in ("up", "down", "flag"):
        raise ValueError(f"feedback must be up|down|flag, got {feedback!r}")
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                UPDATE rag_query_log
                   SET feedback = $4, feedback_note = $5
                 WHERE tenant_id = $1 AND id = $2 AND user_id = $3
                 RETURNING id
                """,
                tenant_id, query_id, user_id, feedback, note,
            )
    return row is not None
