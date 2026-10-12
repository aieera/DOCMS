"""REST endpoints for on-demand intelligence operations."""
from __future__ import annotations

import asyncio
import json
import logging
from typing import Optional

from fastapi import APIRouter, Depends, HTTPException
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import StreamingResponse
from pydantic import BaseModel

from app.identity import (
    Identity, require_admin, require_identity, require_internal_key, require_user,
)
from app.qa_persist import (
    append_message,
    ensure_conversation,
    list_conversations,
    list_messages,
)
from app.tasks.rag import ask, stream_ask, workspace_query
from app.tasks.redact import detect_redaction_candidates
from app.tasks.summarize import summarize_document
from app.tasks.anomaly_detect import run as anomaly_detect_run
from app.tasks.translate import translate as translate_task

log = logging.getLogger(__name__)
# C-8 / DEC-4: every user-facing route authenticates through
# require_identity (gateway signature + dms_session cookie, or the internal
# service key + identity headers). Declared on the router as well so a new
# route can never ship unauthenticated; FastAPI caches the dependency, so
# routes that also take `ident` resolve it once per request.
router = APIRouter(
    prefix="/api/v1/intelligence", tags=["intelligence"],
    dependencies=[Depends(require_identity)],
)
admin_router = APIRouter(
    prefix="/api/v1/admin", tags=["intelligence-admin"],
    dependencies=[Depends(require_identity)],
)

# §7.1 / D6 part 2 — internal embed-query endpoint.
# Not publicly routable through the gateway (path `/internal/` is not
# in deploy/gateway/routes.yaml); the search service calls it
# service-to-service. C-8 / DEC-4: the published port is reachable, so
# every /internal/v1 route also requires the internal service key.
internal_router = APIRouter(
    prefix="/internal/v1", tags=["internal"],
    dependencies=[Depends(require_internal_key)],
)


class EmbedQueryRequest(BaseModel):
    query: str


class EmbedQueryResponse(BaseModel):
    embedding: list[float]
    dimension: int
    model: str


@internal_router.post("/embed-query", response_model=EmbedQueryResponse)
def embed_query_endpoint(body: EmbedQueryRequest):
    """Return a single dense vector for `query` using the same
    embedder that powers chunk ingestion (app.models.embedder.embed_single).

    Clamped at 1 KiB because a real user query is never longer —
    guards against callers using this as a general-purpose batch
    embed endpoint, which would change the model's cost profile.
    """
    q = (body.query or "").strip()
    if not q:
        raise HTTPException(400, "query required")
    if len(q) > 1024:
        raise HTTPException(400, "query too long (max 1024 chars)")

    # Lazy import keeps the router module light at boot.
    from app.models.embedder import embed_single
    from app.config import settings

    vec = embed_single(q)
    return EmbedQueryResponse(
        embedding=list(vec),
        dimension=len(vec),
        # Report the ACTUAL model so query/ingest mismatches are obvious
        # in logs. embed_single + the ingest embedder both use
        # settings.embedding_model (all-MiniLM-L6-v2, 384-dim).
        model=settings.embedding_model,
    )


class AskRequest(BaseModel):
    question: str
    scope: str = "tenant"
    scope_id: Optional[str] = None
    conversation_id: Optional[str] = None
    model_preference: Optional[str] = None


class SummarizeRequest(BaseModel):
    document_id: str
    text: str
    length: str = "medium"


class RedactDetectRequest(BaseModel):
    document_id: str
    version_id: str
    text: str
    entity_types: Optional[list[str]] = None
    auto_detect: bool = True


@router.post("/ask")
async def ask_endpoint(
    body: AskRequest,
    ident: Identity = Depends(require_identity),
):
    tenant = ident.tenant_id
    user_id = require_user(ident)
    groups = list(ident.groups)
    from app import rag_persist

    # M-20: chunks are indexed readable_by ["everyone"], so retrieval must
    # be restricted to the documents this user can actually read. Awaited
    # on the app loop: asyncio.run() here made get_pool() rebuild (and
    # leak) the shared asyncpg pool on every call.
    if body.scope == "document" and body.scope_id:
        readable = await rag_persist.document_readable(
            tenant_id=tenant, user_id=user_id, document_id=body.scope_id)
        allowed = [body.scope_id] if readable else []
    else:
        allowed = await rag_persist.list_allowed_doc_ids(
            tenant_id=tenant, user_id=user_id,
            workspace_id=body.scope_id if body.scope == "workspace" else None)
    # ask() is blocking (embedding, vector search, LLM): run it off the loop.
    return await run_in_threadpool(
        ask,
        tenant_id=tenant,
        user_id=user_id,
        user_groups=groups,
        question=body.question,
        scope=body.scope,
        scope_id=body.scope_id,
        allowed_doc_ids=allowed,
    )


@router.post("/summarize")
def summarize_endpoint(
    body: SummarizeRequest,
    ident: Identity = Depends(require_identity),
):
    tenant = ident.tenant_id
    result = summarize_document.apply(
        args=[tenant, body.document_id, body.text, body.length]
    ).get(timeout=120)
    return result


@router.post("/redact/detect")
def redact_detect_endpoint(
    body: RedactDetectRequest,
    ident: Identity = Depends(require_identity),
):
    tenant = ident.tenant_id
    result = detect_redaction_candidates.apply(
        args=[tenant, body.document_id, body.version_id, body.text],
        kwargs={"entity_types": body.entity_types},
    ).get(timeout=120)
    return result


class QARequest(BaseModel):
    question: str
    # document scope keeps document_id; workspace/global add scope+workspace_id.
    document_id: Optional[str] = None
    scope: str = "document"
    workspace_id: Optional[str] = None
    conversation_id: Optional[str] = None
    model: Optional[str] = None


async def _resolve_qa_scope(
    body: "QARequest", tenant: str, user_id: str
) -> tuple[str, Optional[str], Optional[list[str]]]:
    """Validate the requested scope and return (scope, scope_id, allowed_doc_ids).

    - document  → (document_id, None) after checking the document is in the
      caller's allowed-doc-ids set (M-20: chunks are indexed
      readable_by ["everyone"], so the group filter alone let a document
      UUID read any document in the tenant). 404, not 403, so the answer
      never reveals whether the document exists.
    - workspace → (workspace_id, permission-filtered doc ids for that workspace).
    - global    → (None, permission-filtered doc ids across the user's workspaces).

    allowed_doc_ids is the defense-in-depth permission gate for cross-document
    scopes (empty list ⇒ retrieval returns nothing ⇒ "couldn't find" answer,
    which never leaks whether docs/workspaces exist).
    """
    from app import rag_persist

    scope = (body.scope or "document").lower()
    if scope == "document":
        if not body.document_id:
            raise HTTPException(400, "document_id required for document scope")
        if not await rag_persist.document_readable(
            tenant_id=tenant, user_id=user_id, document_id=body.document_id,
        ):
            raise HTTPException(404, "document not found")
        return "document", body.document_id, None
    if scope == "workspace":
        if not body.workspace_id:
            raise HTTPException(400, "workspace_id required for workspace scope")
        allowed = await rag_persist.list_allowed_doc_ids(
            tenant_id=tenant, user_id=user_id, workspace_id=body.workspace_id,
        )
        return "workspace", body.workspace_id, allowed
    if scope == "global":
        allowed = await rag_persist.list_allowed_doc_ids(
            tenant_id=tenant, user_id=user_id, workspace_id=None,
        )
        return "global", None, allowed
    raise HTTPException(400, f"invalid scope: {scope}")


def _resolve_caller(ident: Identity) -> tuple[str, str, list[str]]:
    return ident.tenant_id, require_user(ident), list(ident.groups)


@router.post("/qa")
async def qa_stream_endpoint(
    body: QARequest,
    ident: Identity = Depends(require_identity),
):
    """ADR 0055 — SSE streaming Q&A. Each `data:` line is a JSON
    object: {type, ...}. type ∈ {citations, chunk, done, error}."""
    tenant, user_id, groups = _resolve_caller(ident)
    if not body.question:
        raise HTTPException(400, "question required")
    scope, scope_id, allowed_doc_ids = await _resolve_qa_scope(body, tenant, user_id)

    conv_id = await ensure_conversation(
        tenant_id=tenant, user_id=user_id,
        conversation_id=body.conversation_id, first_question=body.question,
        scope=scope,
        document_id=scope_id if scope == "document" else None,
        workspace_id=scope_id if scope == "workspace" else None,
    )
    history = await list_messages(tenant_id=tenant, conversation_id=conv_id)
    history_for_llm = [{"role": m["role"], "content": m["content"]} for m in history]
    # Persist the user turn before we stream so a mid-stream client
    # disconnect doesn't lose the question.
    await append_message(
        tenant_id=tenant, conversation_id=conv_id,
        role="user", content=body.question,
    )

    async def event_stream():
        loop = asyncio.get_running_loop()
        # Bridge the sync generator into the async loop so the
        # FastAPI/Uvicorn worker isn't blocked on the LLM call.
        gen = stream_ask(
            tenant_id=tenant, user_id=user_id, user_groups=groups,
            question=body.question, scope=scope, scope_id=scope_id,
            allowed_doc_ids=allowed_doc_ids,
            conversation_history=history_for_llm, model=body.model,
        )
        # First event carries conversation id so the client can
        # round-trip it on subsequent calls.
        yield _sse({"type": "conversation", "conversation_id": conv_id})

        last_done: dict = {}
        last_citations: list = []
        try:
            while True:
                event = await loop.run_in_executor(None, _next_or_none, gen)
                if event is None:
                    break
                etype, payload = event
                if etype == "citations":
                    last_citations = payload.get("citations") or []
                yield _sse({"type": etype, **payload})
                if etype == "done":
                    last_done = payload
        except Exception:
            log.exception("qa stream failed")
            # Generic: provider exception text can carry upstream response
            # bodies (M-23).
            yield _sse({"type": "error", "message": "the answer could not be generated"})
            # Persist what we have so the conversation row isn't a
            # one-sided dangling user turn.
            await append_message(
                tenant_id=tenant, conversation_id=conv_id,
                role="assistant",
                content=last_done.get("full_text") or "(generation failed)",
                citations=last_citations,
                model_used=last_done.get("model"),
                tokens_used=last_done.get("output_tokens"),
            )
            return

        await append_message(
            tenant_id=tenant, conversation_id=conv_id,
            role="assistant",
            content=last_done.get("full_text") or "",
            # `done` is authoritative, INCLUDING an explicit empty list:
            # stream_ask empties the citations when the answer says it
            # found nothing, and `or last_citations` would have resurrected
            # the pre-generation list and re-created the contradiction on
            # every reload of the thread (BUG-11).
            citations=last_done.get("citations", last_citations),
            model_used=last_done.get("model"),
            tokens_used=last_done.get("output_tokens"),
        )

    return StreamingResponse(event_stream(), media_type="text/event-stream",
                             headers={"Cache-Control": "no-cache",
                                      "X-Accel-Buffering": "no"})


@router.post("/qa/sync")
async def qa_sync_endpoint(
    body: QARequest,
    ident: Identity = Depends(require_identity),
):
    """Non-streaming variant — runs the same pipeline, collects all
    chunks, returns the full response at once."""
    tenant, user_id, groups = _resolve_caller(ident)
    if not body.question:
        raise HTTPException(400, "question required")
    scope, scope_id, allowed_doc_ids = await _resolve_qa_scope(body, tenant, user_id)

    conv_id = await ensure_conversation(
        tenant_id=tenant, user_id=user_id,
        conversation_id=body.conversation_id, first_question=body.question,
        scope=scope,
        document_id=scope_id if scope == "document" else None,
        workspace_id=scope_id if scope == "workspace" else None,
    )
    history = await list_messages(tenant_id=tenant, conversation_id=conv_id)
    history_for_llm = [{"role": m["role"], "content": m["content"]} for m in history]
    await append_message(
        tenant_id=tenant, conversation_id=conv_id,
        role="user", content=body.question,
    )

    loop = asyncio.get_running_loop()

    def _drain() -> dict:
        out = {"citations": [], "full_text": "", "model": "",
               "input_tokens": 0, "output_tokens": 0, "cost_usd": 0.0}
        for etype, payload in stream_ask(
            tenant_id=tenant, user_id=user_id, user_groups=groups,
            question=body.question, scope=scope, scope_id=scope_id,
            allowed_doc_ids=allowed_doc_ids,
            conversation_history=history_for_llm, model=body.model,
        ):
            if etype == "citations":
                out["citations"] = payload.get("citations") or []
            elif etype == "chunk":
                out["full_text"] += payload.get("text") or ""
            elif etype == "done":
                out["full_text"] = payload.get("full_text") or out["full_text"]
                # Key presence, not truthiness — an empty list from `done`
                # is the "answer found nothing, show no chips" signal and
                # must not fall back to the pre-generation list (BUG-11).
                out["citations"] = payload.get("citations", out["citations"])
                out["model"] = payload.get("model", "")
                out["input_tokens"] = payload.get("input_tokens", 0)
                out["output_tokens"] = payload.get("output_tokens", 0)
                out["cost_usd"] = payload.get("cost_usd", 0.0)
        return out

    result = await loop.run_in_executor(None, _drain)

    await append_message(
        tenant_id=tenant, conversation_id=conv_id,
        role="assistant", content=result["full_text"],
        citations=result["citations"], model_used=result["model"],
        tokens_used=result["output_tokens"],
    )

    return {
        "conversation_id": conv_id,
        "answer": result["full_text"],
        "citations": result["citations"],
        "model": result["model"],
        "input_tokens": result["input_tokens"],
        "output_tokens": result["output_tokens"],
        "cost_usd": result["cost_usd"],
    }


@router.get("/qa/history")
async def qa_history_scoped_endpoint(
    scope: Optional[str] = None,
    workspace_id: Optional[str] = None,
    conversation_id: Optional[str] = None,
    ident: Identity = Depends(require_identity),
):
    """Scope-aware history for the cross-document Ask chat.

    scope=None (default) → every workspace + global chat for the user (the Ask
    page's history sidebar); scope='workspace' + workspace_id → that workspace's
    chats; scope='global' → global chats. When conversation_id is supplied, also
    returns that conversation's full message thread.
    """
    tenant = ident.tenant_id
    user_id = require_user(ident)
    convs = await list_conversations(
        tenant_id=tenant, user_id=user_id, scope=scope, workspace_id=workspace_id,
    )
    out: dict = {"conversations": convs}
    if conversation_id:
        out["messages"] = await list_messages(
            tenant_id=tenant, conversation_id=conversation_id,
        )
    return out


@router.get("/qa/history/{document_id}")
async def qa_history_endpoint(
    document_id: str,
    conversation_id: Optional[str] = None,
    ident: Identity = Depends(require_identity),
):
    """List the user's conversations for this document. When
    conversation_id is supplied, also returns that conversation's
    full message thread."""
    tenant = ident.tenant_id
    user_id = require_user(ident)
    convs = await list_conversations(
        tenant_id=tenant, user_id=user_id, scope="document", document_id=document_id,
    )
    out: dict = {"conversations": convs}
    if conversation_id:
        out["messages"] = await list_messages(
            tenant_id=tenant, conversation_id=conversation_id,
        )
    return out


def _next_or_none(gen):
    try:
        return next(gen)
    except StopIteration:
        return None


def _sse(obj: dict) -> str:
    return f"data: {json.dumps(obj)}\n\n"


class TranslateRequest(BaseModel):
    document_id: str
    version_id: str
    target_language: str
    model: Optional[str] = None


@router.post("/translate")
async def translate_endpoint(
    body: TranslateRequest,
    ident: Identity = Depends(require_identity),
):
    """ADR 0056 — request a translation. Idempotent on
    (version_id, target_language) — returns the existing row when
    one is already pending/processing/completed."""
    tenant = ident.tenant_id
    user_id = require_user(ident)
    if not body.document_id or not body.version_id or not body.target_language:
        raise HTTPException(400, "document_id, version_id, target_language required")
    # M-20: the translation exposes the version's full text — the caller
    # must be able to read the document, and the version must belong to it.
    from app import rag_persist
    if not await rag_persist.document_readable(
        tenant_id=tenant, user_id=user_id,
        document_id=body.document_id, version_id=body.version_id,
    ):
        raise HTTPException(404, "document not found")

    from app.db.pool import get_pool
    import uuid as _uuid
    pool = await get_pool()

    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant
            )
            existing = await conn.fetchrow(
                """
                SELECT id::text, status, target_language
                  FROM document_translations
                 WHERE tenant_id = $1 AND version_id = $2 AND target_language = $3
                """,
                tenant, body.version_id, body.target_language,
            )
            if existing:
                return {
                    "translation_id": existing["id"],
                    "status": existing["status"],
                    "target_language": existing["target_language"],
                    "deduplicated": True,
                }
            new_id = _uuid.uuid4()
            await conn.execute(
                """
                INSERT INTO document_translations
                    (tenant_id, id, document_id, version_id,
                     source_language, target_language, status, requested_by)
                VALUES ($1, $2, $3, $4, 'auto', $5, 'pending', $6)
                """,
                tenant, new_id, body.document_id, body.version_id,
                body.target_language, user_id,
            )

    translate_task.apply_async(
        kwargs={
            "tenant_id": tenant,
            "translation_id": str(new_id),
            "document_id": body.document_id,
            "version_id": body.version_id,
            "target_language": body.target_language,
            "requested_by": user_id,
            "event_id": str(new_id),
        },
        queue="intelligence",
    )
    return {
        "translation_id": str(new_id),
        "status": "pending",
        "target_language": body.target_language,
        "deduplicated": False,
    }


@router.get("/translations/{document_id}")
async def list_translations_endpoint(
    document_id: str,
    ident: Identity = Depends(require_identity),
):
    tenant = ident.tenant_id
    from app import rag_persist
    if not await rag_persist.document_readable(
        tenant_id=tenant, user_id=ident.user_id, document_id=document_id,
    ):
        raise HTTPException(404, "document not found")
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant
            )
            rows = await conn.fetch(
                """
                SELECT id::text, version_id::text, source_language, target_language,
                       status, COALESCE(model_used, '') AS model_used,
                       COALESCE(word_count, 0) AS word_count,
                       COALESCE(tokens_used, 0) AS tokens_used,
                       COALESCE(error_message, '') AS error_message,
                       created_at, completed_at
                  FROM document_translations
                 WHERE tenant_id = $1 AND document_id = $2
                 ORDER BY created_at DESC
                """,
                tenant, document_id,
            )
    return {
        "translations": [
            {
                "id": r["id"],
                "version_id": r["version_id"],
                "source_language": r["source_language"],
                "target_language": r["target_language"],
                "status": r["status"],
                "model_used": r["model_used"],
                "word_count": r["word_count"],
                "tokens_used": r["tokens_used"],
                "error_message": r["error_message"],
                "created_at": r["created_at"].isoformat(),
                "completed_at": r["completed_at"].isoformat() if r["completed_at"] else None,
            }
            for r in rows
        ],
    }


@router.get("/translations/{translation_id}/text")
async def get_translation_text_endpoint(
    translation_id: str,
    ident: Identity = Depends(require_identity),
):
    tenant = ident.tenant_id
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant
            )
            row = await conn.fetchrow(
                """
                SELECT document_id::text AS document_id,
                       status, source_language, target_language,
                       COALESCE(translated_text, '') AS translated_text,
                       COALESCE(model_used, '') AS model_used,
                       COALESCE(word_count, 0) AS word_count,
                       COALESCE(error_message, '') AS error_message
                  FROM document_translations
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant, translation_id,
            )
    if not row:
        raise HTTPException(404, "translation not found")
    from app import rag_persist
    if not await rag_persist.document_readable(
        tenant_id=tenant, user_id=ident.user_id, document_id=row["document_id"],
    ):
        raise HTTPException(404, "translation not found")
    return {
        "status": row["status"],
        "source_language": row["source_language"],
        "target_language": row["target_language"],
        "translated_text": row["translated_text"],
        "model_used": row["model_used"],
        "word_count": row["word_count"],
        "error_message": row["error_message"],
    }


@router.get("/language/{document_id}")
async def get_document_language_endpoint(
    document_id: str,
    ident: Identity = Depends(require_identity),
):
    tenant = ident.tenant_id
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant
            )
            row = await conn.fetchrow(
                """
                SELECT detected_language, confidence,
                       COALESCE(secondary_languages, '[]'::jsonb) AS secondary,
                       detected_at, version_id::text
                  FROM document_languages
                 WHERE tenant_id = $1 AND document_id = $2
                 ORDER BY detected_at DESC LIMIT 1
                """,
                tenant, document_id,
            )
    if not row:
        return {"detected_language": None}
    sec = row["secondary"]
    if isinstance(sec, str):
        sec = json.loads(sec)
    return {
        "detected_language": row["detected_language"],
        "confidence": float(row["confidence"]),
        "secondary_languages": sec,
        "version_id": row["version_id"],
        "detected_at": row["detected_at"].isoformat(),
    }


class AnomalyRunRequest(BaseModel):
    workspace_id: Optional[str] = None
    analysis_type: str = "combined"   # 'metadata'|'content'|'behavioral'|'combined'


@router.post("/anomaly/run")
async def anomaly_run_endpoint(
    body: AnomalyRunRequest,
    ident: Identity = Depends(require_identity),
):
    """ADR 0058 — kick off a workspace-level outlier scan.

    Creates the anomaly_reports row (status='pending') in the same
    transaction so the row exists before the Celery task picks it up;
    apply_async then drives status forward to processing/completed.
    """
    tenant = ident.tenant_id
    user_id = require_user(ident)
    if body.analysis_type not in ("metadata", "content", "behavioral", "combined"):
        raise HTTPException(400, "analysis_type invalid")

    from app.db.pool import get_pool
    import uuid as _uuid
    pool = await get_pool()
    new_id = _uuid.uuid4()

    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant
            )
            await conn.execute(
                """
                INSERT INTO anomaly_reports
                    (tenant_id, id, workspace_id, analysis_type,
                     status, triggered_by, requested_by)
                VALUES ($1, $2, $3, $4, 'pending', 'manual', $5)
                """,
                tenant, new_id,
                body.workspace_id if body.workspace_id else None,
                body.analysis_type, user_id,
            )

    anomaly_detect_run.apply_async(
        kwargs={
            "tenant_id": tenant,
            "report_id": str(new_id),
            "workspace_id": body.workspace_id,
            "analysis_type": body.analysis_type,
            "event_id": str(new_id),
        },
        queue="intelligence",
    )
    return {
        "report_id": str(new_id),
        "status": "pending",
        "analysis_type": body.analysis_type,
        "workspace_id": body.workspace_id,
    }


# M-18: POST /redact/apply was removed. It took storage_bucket/storage_key
# from the request body and the worker read and wrote that S3 location with
# the service's global credentials. Redaction runs only through the
# legal-hold (dms.document.redacted.v1) and candidate-review flows, which
# resolve the source blob server-side.


# ---- ADR 0080 — workspace-scoped /rag/query --------------------------

class RAGQueryRequest(BaseModel):
    question: str
    workspace_id: Optional[str] = None
    model: Optional[str] = None


class RAGFeedbackRequest(BaseModel):
    feedback: str  # 'up'|'down'|'flag'
    note: Optional[str] = None


@router.post("/rag/query")
async def rag_query_endpoint(
    body: RAGQueryRequest,
    ident: Identity = Depends(require_identity),
):
    """ADR 0080 — workspace-scoped RAG. Different from /qa in that
    retrieval spans many docs (the user's workspace[s] rather than a
    single doc), there's no conversation history, and each call is
    audited + rate-limited via rag_query_log.

    Permission flow: derive allowed_doc_ids from workspace_members +
    documents, pass to workspace_query so retrieval is filtered both
    by readable_by groups (existing) and by the explicit doc-id set
    (defense-in-depth)."""
    tenant, user_id, groups = _resolve_caller(ident)
    question = (body.question or "").strip()
    if not question:
        raise HTTPException(400, "question required")
    if len(question) > 4000:
        raise HTTPException(400, "question too long (max 4000 chars)")

    from app import rag_persist

    settings_row = await rag_persist.get_workspace_ai_settings(
        tenant_id=tenant, workspace_id=body.workspace_id,
    )
    if not settings_row["rag_enabled"]:
        raise HTTPException(403, "rag is disabled for this workspace")

    quota = settings_row["rag_queries_per_day"]
    if quota > 0:
        used = await rag_persist.count_user_queries_24h(
            tenant_id=tenant, user_id=user_id,
        )
        if used >= quota:
            # 429 so the UI can render a "you've hit today's limit"
            # state without falling back to the generic 5xx path.
            raise HTTPException(429, f"daily rag query limit reached ({quota})")

    allowed = await rag_persist.list_allowed_doc_ids(
        tenant_id=tenant, user_id=user_id, workspace_id=body.workspace_id,
    )

    # Resolve titles for the accessible docs so workspace_query can put
    # them into the LLM context + citations. Retrieval is filtered to
    # `allowed`, so retrieved chunks are always a subset — this covers
    # every doc that can appear in a citation. Done here (on the event
    # loop) because workspace_query runs in a thread executor where the
    # asyncpg pool isn't safe to touch.
    doc_titles = await rag_persist.get_document_titles(
        tenant_id=tenant, doc_ids=allowed,
    )

    chosen_model = body.model or settings_row["answer_model"]

    loop = asyncio.get_running_loop()
    result = await loop.run_in_executor(
        None,
        lambda: workspace_query(
            tenant_id=tenant,
            user_id=user_id,
            user_groups=groups,
            question=question,
            workspace_id=body.workspace_id,
            allowed_doc_ids=allowed,
            doc_titles=doc_titles,
            model=chosen_model,
        ),
    )

    query_id = await rag_persist.insert_query_log(
        tenant_id=tenant, user_id=user_id,
        workspace_id=body.workspace_id,
        question=question, result=result,
    )

    return {"query_id": query_id, **result}


@router.post("/rag/query/{query_id}/feedback")
async def rag_feedback_endpoint(
    query_id: str,
    body: RAGFeedbackRequest,
    ident: Identity = Depends(require_identity),
):
    """Record thumbs-up/-down/flag on a prior /rag/query result.
    Scoped to the user who issued the original query (no editing
    someone else's feedback)."""
    tenant = ident.tenant_id
    user_id = require_user(ident)
    if body.feedback not in ("up", "down", "flag"):
        raise HTTPException(400, "feedback must be up|down|flag")
    note = (body.note or "").strip()
    if len(note) > 1000:
        raise HTTPException(400, "note too long (max 1000 chars)")

    from app import rag_persist
    ok = await rag_persist.record_feedback(
        tenant_id=tenant, query_id=query_id, user_id=user_id,
        feedback=body.feedback, note=note or None,
    )
    if not ok:
        raise HTTPException(404, "query not found")
    return {"status": "recorded"}


# ---- ADR 0081 — POST /llm/completions + admin tenant config ----------

class LLMCompletionsRequest(BaseModel):
    messages: list[dict]
    model: Optional[str] = None
    temperature: Optional[float] = 0.1
    max_tokens: Optional[int] = 2000


@router.post("/llm/completions")
def llm_completions_endpoint(
    body: LLMCompletionsRequest,
    ident: Identity = Depends(require_identity),
):
    """First-class completions endpoint that the admin "Test" button
    calls. Routes through the same llm_gateway.completion() path as
    every other internal LLM call so the admin's test result reflects
    real production behavior (provider, fallback, breaker, budget,
    air-gapped) — not a sanitized happy path."""
    tenant = ident.tenant_id
    user_id = require_user(ident)
    if not body.messages:
        raise HTTPException(400, "messages required")

    from app import llm_gateway
    from app.llm_routing import (
        AirGappedError, BudgetExceededError, AllProvidersDownError,
    )
    from app.url_guard import UnsafeBaseURLError
    try:
        result = llm_gateway.completion(
            tenant_id=tenant,
            messages=body.messages,
            model=body.model,
            temperature=body.temperature or 0.1,
            max_tokens=body.max_tokens or 2000,
        )
    except AirGappedError as exc:
        raise HTTPException(403, str(exc))
    except BudgetExceededError as exc:
        raise HTTPException(402, str(exc))
    except (AllProvidersDownError, UnsafeBaseURLError):
        # M-23: generic. The exception text carries the upstream error
        # (and, for a hostile base_url, part of an internal response).
        log.warning("llm/completions failed for tenant %s", tenant, exc_info=True)
        raise HTTPException(503, "LLM provider unavailable")
    return result


class TenantLLMConfigBody(BaseModel):
    provider: Optional[str] = None
    model: Optional[str] = None
    fallback_model: Optional[str] = None
    # Empty string explicitly clears; None preserves the existing key.
    api_key: Optional[str] = None
    base_url: Optional[str] = None
    rate_limit_rpm: Optional[int] = None
    daily_budget_usd: Optional[float] = None
    air_gapped: Optional[bool] = None


@admin_router.get("/tenant/llm-config")
async def get_tenant_llm_config_endpoint(
    ident: Identity = Depends(require_identity),
):
    """Return the per-tenant LLM routing config — provider, model,
    fallback, base_url, limits, air_gapped flag — plus the boolean
    `key_set` and `key_set_at`. Never returns the api_key in any
    form (even encrypted)."""
    tenant = ident.tenant_id
    require_admin(ident)
    from app import tenant_llm_config_repo
    return await tenant_llm_config_repo.get_full_config(tenant)


@admin_router.put("/tenant/llm-config")
async def put_tenant_llm_config_endpoint(
    body: TenantLLMConfigBody,
    ident: Identity = Depends(require_identity),
):
    """Patch any subset of fields. api_key is write-only — once set,
    only `key_set` and `key_set_at` come back from GET. Pass empty
    string to clear; pass None (omit the field) to preserve the
    existing ciphertext while changing other fields."""
    tenant = ident.tenant_id
    require_admin(ident)
    if body.provider is not None and body.provider not in (
        "openai", "anthropic", "bedrock", "vllm_local", "custom", "gemini",
    ):
        raise HTTPException(400, "invalid provider")
    if body.rate_limit_rpm is not None and body.rate_limit_rpm < 0:
        raise HTTPException(400, "rate_limit_rpm must be >= 0")
    if body.daily_budget_usd is not None and body.daily_budget_usd < 0:
        raise HTTPException(400, "daily_budget_usd must be >= 0")
    # base_url must look like a URL — without this guard a typo (an
    # email, an API key paste in the wrong field, free text) lands in
    # the DB and surfaces on the next page load as corrupted config.
    # Empty string clears the field; None preserves the existing value.
    from app import tenant_llm_config_repo
    if body.base_url not in (None, ""):
        bu = body.base_url.strip()
        if not (bu.startswith("http://") or bu.startswith("https://")):
            raise HTTPException(400, "base_url must start with http:// or https://")
        # Reject obvious non-URL strings (containing '@' but no path
        # — looks like an email address).
        if "@" in bu and "/" not in bu.split("://", 1)[1]:
            raise HTTPException(400, "base_url looks like an email; expected an http(s) URL")
        # M-23: the URL becomes litellm's api_base — refuse hosts that
        # resolve to private/link-local/loopback space (SSRF) unless the
        # provider is an allow-listed vllm_local host.
        from app.url_guard import UnsafeBaseURLError, check_base_url
        provider = body.provider
        if provider is None:
            provider = (await tenant_llm_config_repo.get_full_config(tenant)).get("provider") or ""
        try:
            check_base_url(bu, provider=provider)
        except UnsafeBaseURLError as exc:
            raise HTTPException(400, str(exc))

    try:
        return await tenant_llm_config_repo.upsert_config(
            tenant_id=tenant,
            provider=body.provider,
            model=body.model,
            fallback_model=body.fallback_model,
            api_key_plaintext=body.api_key,
            base_url=body.base_url,
            rate_limit_rpm=body.rate_limit_rpm,
            daily_budget_usd=body.daily_budget_usd,
            air_gapped=body.air_gapped,
        )
    except RuntimeError as exc:
        # KEK not configured — refuse to write rather than silently
        # storing plaintext. 503 prompts the admin to fix the deploy
        # config; the call doesn't degrade silently.
        raise HTTPException(503, str(exc))


class WorkspaceAISettingsBody(BaseModel):
    rag_enabled: Optional[bool] = None
    answer_model: Optional[str] = None
    embedding_model: Optional[str] = None
    rag_queries_per_day: Optional[int] = None


@router.get("/workspaces/{workspace_id}/ai-settings")
async def get_workspace_ai_settings_endpoint(
    workspace_id: str,
    ident: Identity = Depends(require_identity),
):
    """Return the per-workspace AI/RAG settings. Falls back to schema
    defaults when no row exists yet (workspace was created before the
    settings panel shipped, or the admin hasn't saved anything)."""
    tenant = ident.tenant_id
    from app import rag_persist
    return await rag_persist.get_workspace_ai_settings_full(
        tenant_id=tenant, workspace_id=workspace_id,
    )


@router.put("/workspaces/{workspace_id}/ai-settings")
async def update_workspace_ai_settings_endpoint(
    workspace_id: str,
    body: WorkspaceAISettingsBody,
    ident: Identity = Depends(require_identity),
):
    """Admin-only — patch the per-workspace AI settings. Workspace
    admins are scoped via the policy service in production; here we
    require tenant role owner|admin since intelligence doesn't speak
    OPA. Workspace-scoped ACL refinement is a follow-up."""
    tenant = ident.tenant_id
    require_admin(ident)
    if body.rag_queries_per_day is not None and body.rag_queries_per_day < 0:
        raise HTTPException(400, "rag_queries_per_day must be >= 0")
    from app import rag_persist
    return await rag_persist.upsert_workspace_ai_settings(
        tenant_id=tenant, workspace_id=workspace_id,
        rag_enabled=body.rag_enabled,
        answer_model=body.answer_model,
        embedding_model=body.embedding_model,
        rag_queries_per_day=body.rag_queries_per_day,
    )


# ---- OCR engine config (per tenant / doc-type) ------------------------

class OcrEngineConfigBody(BaseModel):
    # engine: auto | printed | handwriting. doc_type_overrides maps a
    # document type to an engine, e.g. {"contract": "printed", "form": "handwriting"}.
    engine: str = "auto"
    doc_type_overrides: dict[str, str] = {}


def _engine_for_config(value: str) -> str:
    """Normalize to a stored-config engine: auto | printed | handwriting.
    The 'surya' force alias collapses to 'printed' (it's a per-document
    layout override, not a meaningful default)."""
    from app.tasks.ocr import _normalize_engine
    norm = _normalize_engine(value)
    return "printed" if norm == "surya" else norm


@router.get("/ocr/engine-config")
async def get_ocr_engine_config(ident: Identity = Depends(require_identity)):
    """Return the tenant's default OCR engine + per-doc-type overrides. This is
    the same Redis key (ocr_config:{tenant_id}) the OCR worker reads when no
    explicit force_engine is set, so saving here changes future OCR routing."""
    tenant = ident.tenant_id
    from app.llm_gateway import _get_redis
    raw = _get_redis().get(f"ocr_config:{tenant}")
    if raw:
        cfg = json.loads(raw)
        return {
            "engine": _engine_for_config(cfg.get("engine", "auto")),
            "doc_type_overrides": {
                k: _engine_for_config(v) for k, v in (cfg.get("doc_type_overrides") or {}).items()
            },
        }
    return {"engine": "auto", "doc_type_overrides": {}}


@router.put("/ocr/engine-config")
async def put_ocr_engine_config(
    body: OcrEngineConfigBody,
    ident: Identity = Depends(require_identity),
):
    """Admin-only — set the tenant default OCR engine + per-doc-type overrides.
    Values are normalized to auto|printed|handwriting before persisting."""
    tenant = ident.tenant_id
    require_admin(ident)
    cfg = {
        "engine": _engine_for_config(body.engine),
        "doc_type_overrides": {
            k: _engine_for_config(v) for k, v in (body.doc_type_overrides or {}).items() if k
        },
    }
    from app.llm_gateway import _get_redis
    _get_redis().set(f"ocr_config:{tenant}", json.dumps(cfg))
    return cfg


# ---- LLM usage admin --------------------------------------------------

@admin_router.get("/llm-usage")
async def llm_usage(
    ident: Identity = Depends(require_identity),
):
    """Per-tenant LLM usage tally aggregated by model. Reads the
    `llm_usage:{tenant_id}:{model}` Redis hashes that llm_gateway
    increments on every completion. 30-day TTL — the entries refresh
    on each call so an active tenant always shows a rolling window."""
    tenant = ident.tenant_id
    require_admin(ident)
    from app.llm_gateway import _get_redis
    r = _get_redis()
    pattern = f"llm_usage:{tenant}:*"
    out = []
    grand_calls = 0
    grand_input = 0
    grand_output = 0
    grand_cost = 0.0
    for key in r.scan_iter(match=pattern, count=100):
        key_str = key.decode() if isinstance(key, (bytes, bytearray)) else str(key)
        model = key_str.split(":", 2)[2] if key_str.count(":") >= 2 else "unknown"
        h = r.hgetall(key)
        # h may be dict[bytes, bytes] or dict[str, str] depending on the
        # Redis client decode_responses setting; coerce both.
        def _i(k):
            v = h.get(k.encode()) if isinstance(next(iter(h), b""), (bytes, bytearray)) else h.get(k)
            try:
                return int(v) if v is not None else 0
            except (TypeError, ValueError):
                return 0
        def _f(k):
            v = h.get(k.encode()) if isinstance(next(iter(h), b""), (bytes, bytearray)) else h.get(k)
            try:
                return float(v) if v is not None else 0.0
            except (TypeError, ValueError):
                return 0.0
        calls = _i("calls")
        input_t = _i("input_tokens")
        output_t = _i("output_tokens")
        cost = _f("cost_usd")
        out.append({
            "model": model,
            "calls": calls,
            "input_tokens": input_t,
            "output_tokens": output_t,
            "cost_usd": round(cost, 6),
        })
        grand_calls += calls
        grand_input += input_t
        grand_output += output_t
        grand_cost += cost
    out.sort(key=lambda x: -x["cost_usd"])
    return {
        "tenant_id": tenant,
        "by_model": out,
        "totals": {
            "calls": grand_calls,
            "input_tokens": grand_input,
            "output_tokens": grand_output,
            "cost_usd": round(grand_cost, 6),
        },
    }


# ---- Scan capture: barcode/patch-code separation (capture pipeline) -------
# Service-to-service (called by the Go connector's capture orchestrator). The
# bundle rides as base64 to keep the JSON contract simple; for very large
# scans the connector can switch to multipart later without changing the
# segmentation core.

class CaptureAnalyzeRequest(BaseModel):
    bundle_b64: str
    mime: str = "application/pdf"
    mode: str = "separator_sheet"          # separator_sheet | zonal
    separator_pattern: Optional[str] = None
    zone: Optional[list[float]] = None     # [x0,y0,x1,y1] fractions; zonal mode


class CaptureSplitRequest(BaseModel):
    bundle_b64: str
    mime: str = "application/pdf"
    page_groups: list[list[int]]           # user-corrected grouping from analyze


@internal_router.post("/capture/analyze")
def capture_analyze_endpoint(body: CaptureAnalyzeRequest):
    import base64 as _b64
    from app.capture import pipeline

    try:
        data = _b64.b64decode(body.bundle_b64)
    except Exception:  # noqa: BLE001
        raise HTTPException(status_code=400, detail="bundle_b64 not valid base64")
    zone = tuple(body.zone) if body.zone and len(body.zone) == 4 else None
    try:
        res = pipeline.analyze(data, body.mime, body.mode, body.separator_pattern, zone)
    except ValueError as e:
        # Bundle over a safety limit (CaptureLimitError) or an invalid
        # separator_pattern — caller error, not a server fault.
        raise HTTPException(status_code=400, detail=str(e)) from e
    return {
        "page_count": res.page_count,
        "page_barcodes": res.page_barcodes,
        "page_thumbnails": res.page_thumbnails,
        "segments": [s.to_dict() for s in res.segments],
    }


@internal_router.post("/capture/split")
def capture_split_endpoint(body: CaptureSplitRequest):
    import base64 as _b64
    from app.capture import pipeline

    try:
        data = _b64.b64decode(body.bundle_b64)
    except Exception:  # noqa: BLE001
        raise HTTPException(status_code=400, detail="bundle_b64 not valid base64")
    try:
        parts = pipeline.split(data, body.mime, body.page_groups)
    except ValueError as e:
        # Invalid page grouping (out-of-range/empty) or an over-limit bundle.
        raise HTTPException(status_code=400, detail=str(e)) from e
    # split() guarantees len(parts) == len(page_groups), so the zip pairs each
    # output PDF with its own group.
    return {"documents": [{"pdf_b64": _b64.b64encode(p).decode("ascii"), "page_count": len(g)}
                          for p, g in zip(parts, body.page_groups)]}
