"""Persistence helpers for ADR 0055 — qa_conversations + qa_messages.

Same set_config('app.current_tenant', $1) pattern as the rest of the
intelligence service. All writes from the /qa endpoints land here.
"""
from __future__ import annotations

import json
import logging
import uuid
from typing import Any

from app.db.pool import get_pool

log = logging.getLogger(__name__)


async def ensure_conversation(
    *,
    tenant_id: str,
    user_id: str,
    conversation_id: str | None,
    first_question: str,
    scope: str = "document",
    document_id: str | None = None,
    workspace_id: str | None = None,
) -> str:
    """Return the conversation_id to use. Creates a new row when
    conversation_id is None or doesn't belong to this user + exact scope
    (defensive: prevents thread-jacking via spoofed id, and stops a
    conversation from one scope being reused under another).

    `scope` is one of document | workspace | global. The matching scope id
    (document_id / workspace_id) must be supplied; global uses neither.
    """
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            if conversation_id:
                # IS NOT DISTINCT FROM so NULLs (global scope) compare equal.
                row = await conn.fetchrow(
                    """
                    SELECT id FROM qa_conversations
                     WHERE tenant_id = $1 AND id = $2 AND user_id = $3
                       AND scope = $4
                       AND document_id  IS NOT DISTINCT FROM $5
                       AND workspace_id IS NOT DISTINCT FROM $6
                    """,
                    tenant_id, conversation_id, user_id, scope,
                    document_id, workspace_id,
                )
                if row:
                    return str(row["id"])
            new_id = uuid.uuid4()
            title = (first_question or "")[:60].strip() or "Untitled"
            await conn.execute(
                """
                INSERT INTO qa_conversations
                    (tenant_id, id, scope, document_id, workspace_id, user_id, title)
                VALUES ($1, $2, $3, $4, $5, $6, $7)
                """,
                tenant_id, new_id, scope, document_id, workspace_id, user_id, title,
            )
            return str(new_id)


async def append_message(
    *,
    tenant_id: str,
    conversation_id: str,
    role: str,
    content: str,
    citations: list[dict] | None = None,
    model_used: str | None = None,
    tokens_used: int | None = None,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO qa_messages
                    (tenant_id, conversation_id, role, content, citations,
                     model_used, tokens_used)
                VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
                """,
                tenant_id, conversation_id, role, content,
                json.dumps(citations or []),
                model_used, tokens_used,
            )
            await conn.execute(
                """
                UPDATE qa_conversations
                   SET updated_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, conversation_id,
            )


_LIST_SELECT = """
    SELECT id::text, scope, workspace_id::text AS workspace_id,
           title, created_at, updated_at
      FROM qa_conversations
     WHERE tenant_id = $1 AND user_id = $2
"""


async def list_conversations(
    *,
    tenant_id: str,
    user_id: str,
    scope: str | None = None,
    document_id: str | None = None,
    workspace_id: str | None = None,
    limit: int = 30,
) -> list[dict]:
    """List a user's conversations, newest first.

    scope='document'  → that document's chats (document_id required)
    scope='workspace' → that workspace's chats (workspace_id required)
    scope='global'    → tenant-wide chats
    scope=None        → every cross-document chat (workspace + global) — the
                        Ask page's history sidebar.
    """
    if scope == "document":
        query = _LIST_SELECT + " AND scope='document' AND document_id=$3 ORDER BY updated_at DESC LIMIT $4"
        args: tuple = (tenant_id, user_id, document_id, limit)
    elif scope == "workspace":
        query = _LIST_SELECT + " AND scope='workspace' AND workspace_id=$3 ORDER BY updated_at DESC LIMIT $4"
        args = (tenant_id, user_id, workspace_id, limit)
    elif scope == "global":
        query = _LIST_SELECT + " AND scope='global' ORDER BY updated_at DESC LIMIT $3"
        args = (tenant_id, user_id, limit)
    else:
        query = _LIST_SELECT + " AND scope IN ('workspace','global') ORDER BY updated_at DESC LIMIT $3"
        args = (tenant_id, user_id, limit)

    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(query, *args)
    return [
        {
            "id": r["id"],
            "scope": r["scope"],
            "workspace_id": r["workspace_id"],
            "title": r["title"] or "Untitled",
            "created_at": r["created_at"].isoformat(),
            "updated_at": r["updated_at"].isoformat(),
        }
        for r in rows
    ]


async def list_messages(
    *, tenant_id: str, conversation_id: str,
) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT id::text, role, content, citations,
                       COALESCE(model_used, '') AS model_used,
                       COALESCE(tokens_used, 0) AS tokens_used,
                       created_at
                  FROM qa_messages
                 WHERE tenant_id = $1 AND conversation_id = $2
                 ORDER BY created_at ASC
                """,
                tenant_id, conversation_id,
            )
    out = []
    for r in rows:
        c = r["citations"]
        if isinstance(c, str):
            c = json.loads(c)
        out.append({
            "id": r["id"],
            "role": r["role"],
            "content": r["content"],
            "citations": c or [],
            "model_used": r["model_used"],
            "tokens_used": r["tokens_used"],
            "created_at": r["created_at"].isoformat(),
        })
    return out
