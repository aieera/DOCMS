"""LiteLLM wrapper with per-tenant routing, rate limiting, and billing."""
from __future__ import annotations

import asyncio
import json
import logging
import time
from typing import Any, Optional

import litellm
import redis

from app.config import settings

log = logging.getLogger(__name__)

_tenant_semaphores: dict[str, asyncio.Semaphore] = {}
_redis: Optional[redis.Redis] = None


def _get_redis() -> redis.Redis:
    global _redis
    if _redis is None:
        _redis = redis.Redis.from_url(settings.redis_cache_url)
    return _redis


def _get_semaphore(tenant_id: str) -> asyncio.Semaphore:
    if tenant_id not in _tenant_semaphores:
        _tenant_semaphores[tenant_id] = asyncio.Semaphore(settings.llm_max_concurrent_per_tenant)
    return _tenant_semaphores[tenant_id]


def _load_tenant_config(tenant_id: str) -> dict | None:
    r = _get_redis()
    raw = r.get(f"llm_config:{tenant_id}")
    if raw:
        return json.loads(raw)
    return None


def completion(
    tenant_id: str,
    messages: list[dict[str, str]],
    model: str | None = None,
    temperature: float = 0.1,
    max_tokens: int = 2000,
) -> dict[str, Any]:
    """Synchronous LLM completion with per-tenant config + metering."""
    config = _load_tenant_config(tenant_id)
    llm_model = model or (config or {}).get("model") or settings.default_llm_model
    api_key = (config or {}).get("api_key")
    api_base = (config or {}).get("base_url")

    start = time.monotonic()
    try:
        resp = litellm.completion(
            model=llm_model,
            messages=messages,
            temperature=temperature,
            max_tokens=max_tokens,
            api_key=api_key,
            api_base=api_base,
            timeout=settings.llm_timeout_seconds,
        )
    except litellm.RateLimitError:
        log.warning("rate limited on %s for tenant %s, retrying once", llm_model, tenant_id)
        time.sleep(2)
        resp = litellm.completion(
            model=llm_model, messages=messages,
            temperature=temperature, max_tokens=max_tokens,
            api_key=api_key, api_base=api_base,
            timeout=settings.llm_timeout_seconds,
        )

    elapsed_ms = int((time.monotonic() - start) * 1000)
    usage = resp.usage if hasattr(resp, "usage") else None
    input_tokens = getattr(usage, "prompt_tokens", 0) if usage else 0
    output_tokens = getattr(usage, "completion_tokens", 0) if usage else 0
    cost = litellm.completion_cost(completion_response=resp) if resp else 0.0

    _meter_usage(tenant_id, llm_model, input_tokens, output_tokens, cost, elapsed_ms)

    content = resp.choices[0].message.content if resp.choices else ""
    return {
        "content": content,
        "model": llm_model,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "cost_usd": cost,
        "elapsed_ms": elapsed_ms,
    }


def stream_completion(
    tenant_id: str,
    messages: list[dict[str, str]],
    model: str | None = None,
    temperature: float = 0.1,
    max_tokens: int = 2000,
):
    """Yield (text_chunk, is_final, metadata) tuples as the LLM streams.

    metadata is None for chunk events; on the final yield it carries
    {"model", "input_tokens", "output_tokens", "cost_usd", "elapsed_ms"}.

    Token counts are approximate during the stream (litellm doesn't always
    fill usage on intermediate chunks); the final yield is the source of
    truth and drives the meter exactly once.
    """
    config = _load_tenant_config(tenant_id)
    llm_model = model or (config or {}).get("model") or settings.default_llm_model
    api_key = (config or {}).get("api_key")
    api_base = (config or {}).get("base_url")

    start = time.monotonic()
    full_text_parts: list[str] = []
    last_chunk_obj = None
    try:
        stream = litellm.completion(
            model=llm_model, messages=messages,
            temperature=temperature, max_tokens=max_tokens,
            api_key=api_key, api_base=api_base,
            timeout=settings.llm_timeout_seconds,
            stream=True,
        )
        for chunk in stream:
            last_chunk_obj = chunk
            try:
                delta = chunk.choices[0].delta
                text = getattr(delta, "content", "") or ""
            except Exception:
                text = ""
            if text:
                full_text_parts.append(text)
                yield text, False, None
    except litellm.RateLimitError:
        log.warning("rate limited mid-stream on %s for tenant %s", llm_model, tenant_id)
        raise

    elapsed_ms = int((time.monotonic() - start) * 1000)
    full_text = "".join(full_text_parts)
    # Streaming responses from Anthropic don't carry prompt_tokens or
    # completion_tokens on intermediate chunks; the final chunk's
    # `usage` field is set with stream_options={"include_usage": True}
    # in newer providers but isn't guaranteed. Fall through three
    # tiers so we always end up with non-zero counts:
    #   1. last_chunk.usage  — the truth when the provider sends it
    #   2. litellm.token_counter on prompt + the streamed text — exact
    #      token count using the model's tokenizer
    #   3. character / 4 heuristic — final fallback when the tokenizer
    #      isn't available for that model id
    input_tokens = 0
    output_tokens = 0
    cost = 0.0
    if last_chunk_obj and hasattr(last_chunk_obj, "usage") and last_chunk_obj.usage:
        try:
            input_tokens = getattr(last_chunk_obj.usage, "prompt_tokens", 0) or 0
            output_tokens = getattr(last_chunk_obj.usage, "completion_tokens", 0) or 0
        except Exception:
            pass
    if not input_tokens:
        try:
            input_tokens = litellm.token_counter(model=llm_model, messages=messages)
        except Exception:
            input_tokens = max(1, sum(len(m.get("content", "")) for m in messages) // 4)
    if not output_tokens and full_text:
        try:
            output_tokens = litellm.token_counter(
                model=llm_model,
                messages=[{"role": "assistant", "content": full_text}],
            )
        except Exception:
            output_tokens = max(1, len(full_text) // 4)
    try:
        cost = litellm.cost_per_token(
            model=llm_model,
            prompt_tokens=input_tokens,
            completion_tokens=output_tokens,
        )
        # cost_per_token returns (prompt_cost, completion_cost) in newer
        # litellm; older returned a single float. Sum either way.
        if isinstance(cost, tuple):
            cost = float(sum(cost))
        else:
            cost = float(cost or 0)
    except Exception:
        cost = 0.0

    _meter_usage(tenant_id, llm_model, input_tokens, output_tokens, cost, elapsed_ms)
    yield "", True, {
        "model": llm_model,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "cost_usd": cost,
        "elapsed_ms": elapsed_ms,
        "full_text": full_text,
    }


def _meter_usage(tenant_id: str, model: str, input_t: int, output_t: int, cost: float, elapsed_ms: int):
    try:
        r = _get_redis()
        key = f"llm_usage:{tenant_id}:{model}"
        pipe = r.pipeline()
        pipe.hincrby(key, "input_tokens", input_t)
        pipe.hincrby(key, "output_tokens", output_t)
        pipe.hincrbyfloat(key, "cost_usd", cost)
        pipe.hincrby(key, "calls", 1)
        pipe.expire(key, 30 * 86400)
        pipe.execute()
    except Exception as e:
        log.warning("meter usage failed: %s", e)
