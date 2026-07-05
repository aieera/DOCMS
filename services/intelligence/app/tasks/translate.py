"""On-demand document translation via the LLM gateway (ADR 0056).

Triggered by: REST POST /api/v1/intelligence/translate (the API
              creates a pending row + enqueues this task).
Persists to:  document_translations.status='completed' or 'failed'
Emits:        dms.translation.completed.v1

Translates the OCR full_text of a version into the target language.
Long documents are chunked at paragraph boundaries; each chunk is
translated with a deterministic prompt (temperature 0). A failure
on any chunk fails the whole translation atomically — no partial
emission.

Cost guard: max_chars_per_doc from translation_config rejects
oversize jobs before any LLM call.
"""
from __future__ import annotations

import asyncio
import json
import logging
import re
import time
import uuid
from datetime import datetime, timezone
from typing import Any

from app.error_classifier import classify_error_reason
from app.intel_dedupe import (
    already_completed,
    mark_completed,
    mark_enqueued,
    mark_failed,
    publish_dlq,
)
from app.worker import celery_app
from app.db.pool import get_pool
from app.events.subjects import TRANSLATION_COMPLETED_SUBJECT

log = logging.getLogger(__name__)

CONSUMER = "translate"

CHUNK_SIZE_CHARS = 12_000   # ~3000 tokens; well under any modern context
DEFAULT_AVAILABLE = ["en", "ar", "fr", "es", "de", "zh", "ja", "ko", "hi", "pt"]
DEFAULT_MAX_CHARS = 100_000

SYSTEM_PROMPT = (
    "You are a professional translator. Translate the following text from "
    "{source} to {target}. Preserve formatting, paragraphs, lists, and "
    "structure. Do not add explanations, notes, or commentary. Output "
    "ONLY the translation."
)


@celery_app.task(
    name="app.tasks.translate.translate",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 2},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=600,
    time_limit=900,
)
def translate(
    self,
    tenant_id: str,
    translation_id: str,
    document_id: str,
    version_id: str,
    target_language: str,
    requested_by: str,
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id, translation_id=translation_id,
        document_id=document_id, version_id=version_id,
        target_language=target_language, requested_by=requested_by,
        event_id=event_id, correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 2,
    ))


async def _run_async(
    *, tenant_id, translation_id, document_id, version_id,
    target_language, requested_by, event_id, correlation_id,
    attempt, is_terminal,
) -> dict:
    start = time.monotonic()
    if not (tenant_id and document_id and version_id and target_language and translation_id):
        return {"status": "skipped", "reason": "missing args"}

    try:
        if event_id and await already_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ):
            return {"status": "duplicate", "event_id": event_id}
        await mark_enqueued(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
            document_id=document_id, version_id=version_id,
        )

        cfg = await _load_config(tenant_id)
        if not cfg["enabled"]:
            await _mark_translation_failed(tenant_id, translation_id, "translation disabled")
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "disabled"}
        if target_language not in cfg["available_languages"]:
            await _mark_translation_failed(tenant_id, translation_id,
                f"target language '{target_language}' not in tenant's available_languages")
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "rejected", "reason": "language not available"}

        text = await _fetch_ocr_text(tenant_id, version_id)
        if not text or len(text.strip()) < 5:
            await _mark_translation_failed(tenant_id, translation_id, "no OCR text on this version")
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "skipped", "reason": "no text"}
        if len(text) > cfg["max_chars_per_doc"]:
            await _mark_translation_failed(
                tenant_id, translation_id,
                f"text length {len(text)} exceeds max_chars_per_doc {cfg['max_chars_per_doc']}",
            )
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "rejected", "reason": "too long"}

        source_language = await _fetch_source_language(tenant_id, version_id) or "auto"

        await _set_translation_status(tenant_id, translation_id, "processing")

        chunks = _split_chunks(text, CHUNK_SIZE_CHARS)
        translated_parts: list[str] = []
        total_input = 0
        total_output = 0
        model_used = cfg.get("model_override") or ""
        from app import llm_gateway
        for i, chunk in enumerate(chunks):
            messages = [
                {"role": "system", "content": SYSTEM_PROMPT.format(
                    source=source_language, target=target_language,
                )},
                {"role": "user", "content": chunk},
            ]
            # Offload the blocking sync LLM call to a worker thread:
            # llm_gateway.completion() → route_completion() calls asyncio.run()
            # internally, which raises "asyncio.run() cannot be called from a
            # running event loop" when invoked directly from this async task.
            # A thread gives it a clean, loop-free context (and avoids blocking
            # the worker's event loop) — same effect as the RAG path.
            resp = await asyncio.to_thread(
                lambda: llm_gateway.completion(
                    tenant_id=tenant_id, messages=messages,
                    model=cfg.get("model_override") or None,
                    temperature=0.0,
                    max_tokens=4000,
                )
            )
            translated_parts.append(resp.get("content") or "")
            total_input += int(resp.get("input_tokens", 0) or 0)
            total_output += int(resp.get("output_tokens", 0) or 0)
            model_used = resp.get("model") or model_used

        full_translation = "\n\n".join(translated_parts).strip()
        word_count = len(full_translation.split())
        elapsed_ms = int((time.monotonic() - start) * 1000)

        await _persist_completion(
            tenant_id=tenant_id, translation_id=translation_id,
            document_id=document_id, version_id=version_id,
            target_language=target_language, source_language=source_language,
            translated_text=full_translation, model_used=model_used,
            tokens_used=total_input + total_output, word_count=word_count,
            translation_time_ms=elapsed_ms,
            requested_by=requested_by, correlation_id=correlation_id,
        )

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
        log.info(
            "translate.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "target_language": target_language,
                "chunks": len(chunks),
                "word_count": word_count,
                "elapsed_ms": elapsed_ms,
            },
        )
        return {
            "status": "completed",
            "translation_id": translation_id,
            "target_language": target_language,
            "chunks": len(chunks),
            "word_count": word_count,
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        # Log the raw error for operators — it must NEVER reach the user panel.
        log.exception(
            "translate.failed",
            extra={
                "tenant_id": tenant_id, "translation_id": translation_id,
                "version_id": version_id, "target_language": target_language,
                "attempt": attempt, "terminal": is_terminal,
            },
        )
        # Mark the user-facing translation Failed on EVERY attempt (not only the
        # terminal one) so the panel converges instead of hanging at "Pending"
        # through the retry window; a successful retry overwrites it back to
        # "completed". The message is friendly — the raw exception/DB text stays
        # in the logs + DLQ only (was leaking "UndefinedColumnError: ..." to UI).
        try:
            await _mark_translation_failed(
                tenant_id, translation_id,
                "Translation failed — please retry.",
            )
        except Exception:
            log.exception("mark translation failed")
        if is_terminal:
            reason = classify_error_reason(exc)
            try:
                await publish_dlq(
                    consumer=CONSUMER, reason=reason, tenant_id=tenant_id,
                    document_id=document_id, version_id=version_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=attempt, correlation_id=correlation_id,
                )
                await mark_failed(
                    tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                )
            except Exception:
                log.exception("translate terminal DLQ publish failed")
        raise


# ---- chunking ----------------------------------------------------------

def _split_chunks(text: str, max_chars: int) -> list[str]:
    """Split at blank-line paragraph boundaries; pack as many paragraphs
    as fit per chunk. A single paragraph exceeding max_chars is kept
    intact (translation quality wins over strict size limits)."""
    paragraphs = re.split(r"\n\s*\n", text)
    chunks: list[str] = []
    current: list[str] = []
    current_len = 0
    for p in paragraphs:
        p = p.strip()
        if not p:
            continue
        plen = len(p) + 2  # +2 for separator
        if current and current_len + plen > max_chars:
            chunks.append("\n\n".join(current))
            current = [p]
            current_len = plen
        else:
            current.append(p)
            current_len += plen
    if current:
        chunks.append("\n\n".join(current))
    return chunks or [text]


# ---- DB helpers --------------------------------------------------------

DEFAULT_CONFIG = {
    "enabled": True,
    "available_languages": list(DEFAULT_AVAILABLE),
    "default_target": "en",
    "max_chars_per_doc": DEFAULT_MAX_CHARS,
    "model_override": None,
}


async def _load_config(tenant_id: str) -> dict:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT enabled, available_languages, default_target,
                       max_chars_per_doc, model_override
                  FROM translation_config WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return dict(DEFAULT_CONFIG)
    return {
        "enabled": row["enabled"],
        "available_languages": list(row["available_languages"] or DEFAULT_AVAILABLE),
        "default_target": row["default_target"] or "en",
        "max_chars_per_doc": int(row["max_chars_per_doc"] or DEFAULT_MAX_CHARS),
        "model_override": row["model_override"],
    }


async def _fetch_ocr_text(tenant_id: str, version_id: str) -> str:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            # ocr_results is per-PAGE (columns: page_number, text_content);
            # there is no full_text column. Concatenate the pages in order so
            # translation sees the whole document, not one arbitrary page.
            # (Same fix already applied to lang_detect.)
            row = await conn.fetchrow(
                """
                SELECT COALESCE(
                         string_agg(text_content, E'\n' ORDER BY page_number),
                         ''
                       ) AS full_text
                  FROM ocr_results
                 WHERE tenant_id = $1 AND version_id = $2
                """,
                tenant_id, version_id,
            )
    return (row["full_text"] if row else "") or ""


async def _fetch_source_language(tenant_id: str, version_id: str) -> str | None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT detected_language FROM document_languages
                 WHERE tenant_id = $1 AND version_id = $2
                """,
                tenant_id, version_id,
            )
    return row["detected_language"] if row else None


async def _set_translation_status(tenant_id: str, translation_id: str, status: str) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE document_translations
                   SET status = $3, updated_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, translation_id, status,
            )


async def _mark_translation_failed(tenant_id: str, translation_id: str, error: str) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE document_translations
                   SET status = 'failed', error_message = $3,
                       completed_at = NOW(), updated_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, translation_id, error[:1000],
            )


async def _persist_completion(
    *, tenant_id, translation_id, document_id, version_id,
    target_language, source_language, translated_text, model_used,
    tokens_used, word_count, translation_time_ms,
    requested_by, correlation_id,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE document_translations
                   SET status              = 'completed',
                       source_language     = $3,
                       translated_text     = $4,
                       model_used          = $5,
                       tokens_used         = $6,
                       word_count          = $7,
                       translation_time_ms = $8,
                       completed_at        = NOW(),
                       updated_at          = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, translation_id, source_language, translated_text,
                model_used, tokens_used, word_count, translation_time_ms,
            )

            payload = {
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "translation_id": translation_id,
                "source_language": source_language,
                "target_language": target_language,
                "model_used": model_used,
                "tokens_used": tokens_used,
                "word_count": word_count,
                "translation_time_ms": translation_time_ms,
                "requested_by": requested_by,
                "correlation_id": correlation_id,
                "emitted_at": datetime.now(timezone.utc).isoformat(),
            }
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'document', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                TRANSLATION_COMPLETED_SUBJECT, document_id,
                json.dumps(payload),
            )
