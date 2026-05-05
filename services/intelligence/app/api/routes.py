"""REST endpoints for on-demand intelligence operations."""
from __future__ import annotations

import asyncio
import json
import logging
from typing import Optional

from fastapi import APIRouter, Header, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel

from app.qa_persist import (
    append_message,
    ensure_conversation,
    list_conversations,
    list_messages,
)
from app.tasks.rag import ask, stream_ask, workspace_query
from app.tasks.redact import apply_redactions, detect_redaction_candidates
from app.tasks.summarize import summarize_document
from app.tasks.anomaly_detect import run as anomaly_detect_run
from app.tasks.translate import translate as translate_task

log = logging.getLogger(__name__)
router = APIRouter(prefix="/api/v1/intelligence", tags=["intelligence"])
admin_router = APIRouter(prefix="/api/v1/admin", tags=["intelligence-admin"])

# §7.1 / D6 part 2 — internal embed-query endpoint.
# Not publicly routable through the gateway (path `/internal/` is not
# in deploy/gateway/routes.yaml); the search service calls it
# service-to-service.
internal_router = APIRouter(prefix="/internal/v1", tags=["internal"])


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

    vec = embed_single(q)
    return EmbedQueryResponse(
        embedding=list(vec),
        dimension=len(vec),
        model="bge-m3",
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


class RedactApplyRequest(BaseModel):
    document_id: str
    version_id: str
    storage_bucket: str
    storage_key: str
    entities: list[dict]


def _require_tenant(tenant_id: Optional[str]) -> str:
    if not tenant_id:
        raise HTTPException(400, "X-Tenant-ID required")
    return tenant_id


@router.post("/ask")
def ask_endpoint(
    body: AskRequest,
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
    x_group_ids: Optional[str] = Header(None, alias="X-Group-IDs"),
):
    tenant = _require_tenant(x_tenant_id)
    if not x_user_id:
        raise HTTPException(400, "X-User-ID required")
    groups = [g.strip() for g in (x_group_ids or "").split(",") if g.strip()]
    result = ask(
        tenant_id=tenant,
        user_id=x_user_id,
        user_groups=groups,
        question=body.question,
        scope=body.scope,
        scope_id=body.scope_id,
    )
    return result


@router.post("/summarize")
def summarize_endpoint(
    body: SummarizeRequest,
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
):
    tenant = _require_tenant(x_tenant_id)
    result = summarize_document.apply(
        args=[tenant, body.document_id, body.text, body.length]
    ).get(timeout=120)
    return result


@router.post("/redact/detect")
def redact_detect_endpoint(
    body: RedactDetectRequest,
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
):
    tenant = _require_tenant(x_tenant_id)
    result = detect_redaction_candidates.apply(
        args=[tenant, body.document_id, body.version_id, body.text],
        kwargs={"entity_types": body.entity_types},
    ).get(timeout=120)
    return result


class QARequest(BaseModel):
    document_id: str
    question: str
    conversation_id: Optional[str] = None
    model: Optional[str] = None


def _resolve_caller(x_tenant_id, x_user_id, x_group_ids) -> tuple[str, str, list[str]]:
    tenant = _require_tenant(x_tenant_id)
    if not x_user_id:
        raise HTTPException(400, "X-User-ID required")
    groups = [g.strip() for g in (x_group_ids or "").split(",") if g.strip()]
    return tenant, x_user_id, groups


@router.post("/qa")
async def qa_stream_endpoint(
    body: QARequest,
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
    x_group_ids: Optional[str] = Header(None, alias="X-Group-IDs"),
):
    """ADR 0055 — SSE streaming Q&A. Each `data:` line is a JSON
    object: {type, ...}. type ∈ {citations, chunk, done, error}."""
    tenant, user_id, groups = _resolve_caller(x_tenant_id, x_user_id, x_group_ids)
    if not body.document_id or not body.question:
        raise HTTPException(400, "document_id and question required")

    conv_id = await ensure_conversation(
        tenant_id=tenant, user_id=user_id, document_id=body.document_id,
        conversation_id=body.conversation_id, first_question=body.question,
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
            question=body.question, document_id=body.document_id,
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
        except Exception as exc:
            log.exception("qa stream failed")
            yield _sse({"type": "error", "message": f"{type(exc).__name__}: {exc}"})
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
            citations=last_done.get("citations") or last_citations,
            model_used=last_done.get("model"),
            tokens_used=last_done.get("output_tokens"),
        )

    return StreamingResponse(event_stream(), media_type="text/event-stream",
                             headers={"Cache-Control": "no-cache",
                                      "X-Accel-Buffering": "no"})


@router.post("/qa/sync")
async def qa_sync_endpoint(
    body: QARequest,
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
    x_group_ids: Optional[str] = Header(None, alias="X-Group-IDs"),
):
    """Non-streaming variant — runs the same pipeline, collects all
    chunks, returns the full response at once."""
    tenant, user_id, groups = _resolve_caller(x_tenant_id, x_user_id, x_group_ids)
    if not body.document_id or not body.question:
        raise HTTPException(400, "document_id and question required")

    conv_id = await ensure_conversation(
        tenant_id=tenant, user_id=user_id, document_id=body.document_id,
        conversation_id=body.conversation_id, first_question=body.question,
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
            question=body.question, document_id=body.document_id,
            conversation_history=history_for_llm, model=body.model,
        ):
            if etype == "citations":
                out["citations"] = payload.get("citations") or []
            elif etype == "chunk":
                out["full_text"] += payload.get("text") or ""
            elif etype == "done":
                out["full_text"] = payload.get("full_text") or out["full_text"]
                out["citations"] = payload.get("citations") or out["citations"]
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


@router.get("/qa/history/{document_id}")
async def qa_history_endpoint(
    document_id: str,
    conversation_id: Optional[str] = None,
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
):
    """List the user's conversations for this document. When
    conversation_id is supplied, also returns that conversation's
    full message thread."""
    tenant = _require_tenant(x_tenant_id)
    if not x_user_id:
        raise HTTPException(400, "X-User-ID required")
    convs = await list_conversations(
        tenant_id=tenant, user_id=x_user_id, document_id=document_id,
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
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
):
    """ADR 0056 — request a translation. Idempotent on
    (version_id, target_language) — returns the existing row when
    one is already pending/processing/completed."""
    tenant = _require_tenant(x_tenant_id)
    if not x_user_id:
        raise HTTPException(400, "X-User-ID required")
    if not body.document_id or not body.version_id or not body.target_language:
        raise HTTPException(400, "document_id, version_id, target_language required")

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
                body.target_language, x_user_id,
            )

    translate_task.apply_async(
        kwargs={
            "tenant_id": tenant,
            "translation_id": str(new_id),
            "document_id": body.document_id,
            "version_id": body.version_id,
            "target_language": body.target_language,
            "requested_by": x_user_id,
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
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
):
    tenant = _require_tenant(x_tenant_id)
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
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
):
    tenant = _require_tenant(x_tenant_id)
    from app.db.pool import get_pool
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant
            )
            row = await conn.fetchrow(
                """
                SELECT status, source_language, target_language,
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
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
):
    tenant = _require_tenant(x_tenant_id)
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
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
):
    """ADR 0058 — kick off a workspace-level outlier scan.

    Creates the anomaly_reports row (status='pending') in the same
    transaction so the row exists before the Celery task picks it up;
    apply_async then drives status forward to processing/completed.
    """
    tenant = _require_tenant(x_tenant_id)
    if not x_user_id:
        raise HTTPException(400, "X-User-ID required")
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
                body.analysis_type, x_user_id,
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


@router.post("/redact/apply")
def redact_apply_endpoint(
    body: RedactApplyRequest,
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
):
    tenant = _require_tenant(x_tenant_id)
    result = apply_redactions.apply_async(kwargs={
        "tenant_id": tenant,
        "document_id": body.document_id,
        "version_id": body.version_id,
        "storage_bucket": body.storage_bucket,
        "storage_key": body.storage_key,
        "entities": body.entities,
    })
    return {"task_id": result.id, "status": "queued"}


# ---- ADR 0063 — workspace-scoped /rag/query --------------------------

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
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
    x_group_ids: Optional[str] = Header(None, alias="X-Group-IDs"),
):
    """ADR 0063 — workspace-scoped RAG. Different from /qa in that
    retrieval spans many docs (the user's workspace[s] rather than a
    single doc), there's no conversation history, and each call is
    audited + rate-limited via rag_query_log.

    Permission flow: derive allowed_doc_ids from workspace_members +
    documents, pass to workspace_query so retrieval is filtered both
    by readable_by groups (existing) and by the explicit doc-id set
    (defense-in-depth)."""
    tenant, user_id, groups = _resolve_caller(x_tenant_id, x_user_id, x_group_ids)
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
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_id: Optional[str] = Header(None, alias="X-User-ID"),
):
    """Record thumbs-up/-down/flag on a prior /rag/query result.
    Scoped to the user who issued the original query (no editing
    someone else's feedback)."""
    tenant = _require_tenant(x_tenant_id)
    if not x_user_id:
        raise HTTPException(400, "X-User-ID required")
    if body.feedback not in ("up", "down", "flag"):
        raise HTTPException(400, "feedback must be up|down|flag")
    note = (body.note or "").strip()
    if len(note) > 1000:
        raise HTTPException(400, "note too long (max 1000 chars)")

    from app import rag_persist
    ok = await rag_persist.record_feedback(
        tenant_id=tenant, query_id=query_id, user_id=x_user_id,
        feedback=body.feedback, note=note or None,
    )
    if not ok:
        raise HTTPException(404, "query not found")
    return {"status": "recorded"}


# ---- LLM usage admin --------------------------------------------------

@admin_router.get("/llm-usage")
async def llm_usage(
    x_tenant_id: Optional[str] = Header(None, alias="X-Tenant-ID"),
    x_user_role: Optional[str] = Header(None, alias="X-User-Role"),
):
    """Per-tenant LLM usage tally aggregated by model. Reads the
    `llm_usage:{tenant_id}:{model}` Redis hashes that llm_gateway
    increments on every completion. 30-day TTL — the entries refresh
    on each call so an active tenant always shows a rolling window."""
    tenant = _require_tenant(x_tenant_id)
    if x_user_role not in {"owner", "admin"}:
        raise HTTPException(403, "owner|admin required")
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
