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
