"""REST endpoints for on-demand intelligence operations."""
from __future__ import annotations

import logging
from typing import Optional

from fastapi import APIRouter, Header, HTTPException
from pydantic import BaseModel

from app.tasks.rag import ask
from app.tasks.redact import apply_redactions, detect_redaction_candidates
from app.tasks.summarize import summarize_document

log = logging.getLogger(__name__)
router = APIRouter(prefix="/api/v1/intelligence", tags=["intelligence"])


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
