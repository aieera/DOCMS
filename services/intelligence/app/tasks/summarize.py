"""Document summarization — single-call or map-reduce for long docs."""
from __future__ import annotations

import logging
import time

import tiktoken

from app.worker import celery_app

log = logging.getLogger(__name__)

LENGTH_TOKENS = {"short": 200, "medium": 500, "detailed": 1200}


def _count_tokens(text: str) -> int:
    enc = tiktoken.get_encoding("cl100k_base")
    return len(enc.encode(text))


def _single_summarize(text: str, tenant_id: str, length: str) -> dict:
    from app import llm_gateway
    max_out = LENGTH_TOKENS.get(length, 500)
    return llm_gateway.completion(
        tenant_id=tenant_id,
        messages=[
            {"role": "system", "content": "Summarize the following document concisely."},
            {"role": "user", "content": text[:15000]},
        ],
        max_tokens=max_out,
    )


def _map_reduce_summarize(text: str, tenant_id: str, length: str) -> dict:
    from app import llm_gateway
    enc = tiktoken.get_encoding("cl100k_base")
    tokens = enc.encode(text)
    chunk_size = 3000
    chunks = []
    for i in range(0, len(tokens), chunk_size):
        chunks.append(enc.decode(tokens[i:i + chunk_size]))

    summaries = []
    for i, chunk in enumerate(chunks):
        resp = llm_gateway.completion(
            tenant_id=tenant_id,
            messages=[
                {"role": "system", "content": f"Summarize this section (part {i + 1}/{len(chunks)}) concisely."},
                {"role": "user", "content": chunk},
            ],
            max_tokens=300,
        )
        summaries.append(resp["content"])

    combined = "\n\n".join(summaries)
    max_out = LENGTH_TOKENS.get(length, 500)
    return llm_gateway.completion(
        tenant_id=tenant_id,
        messages=[
            {"role": "system", "content": "Combine these section summaries into one coherent summary."},
            {"role": "user", "content": combined},
        ],
        max_tokens=max_out,
    )


@celery_app.task(name="app.tasks.summarize.summarize_document", bind=True)
def summarize_document(self, tenant_id: str, document_id: str, text: str, length: str = "medium"):
    start = time.monotonic()
    token_count = _count_tokens(text)
    if token_count < 4000:
        resp = _single_summarize(text, tenant_id, length)
    else:
        resp = _map_reduce_summarize(text, tenant_id, length)
    elapsed_ms = int((time.monotonic() - start) * 1000)
    return {
        "status": "completed",
        "tenant_id": tenant_id,
        "document_id": document_id,
        "summary": resp["content"],
        "model": resp["model"],
        "cost_usd": resp["cost_usd"],
        "elapsed_ms": elapsed_ms,
    }
