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
from app.tasks.rag import ask, stream_ask
from app.tasks.redact import apply_redactions, detect_redaction_candidates
from app.tasks.summarize import summarize_document

log = logging.getLogger(__name__)
router = APIRouter(prefix="/api/v1/intelligence", tags=["intelligence"])

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
