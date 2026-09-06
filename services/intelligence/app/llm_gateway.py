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
from app.llm_key_guard import require_tenant_key
from app.events.subjects import BILLING_LLM_USAGE_SUBJECT

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
    """Synchronous LLM completion with per-tenant routing.

    ADR 0081: actual routing (provider resolution, air-gapped gate,
    budget gate, circuit breaker, fallback model) lives in
    llm_routing.route_completion. We keep the dict-shaped return
    here for backward compatibility with every existing caller (Doc
    Q&A, summarize, NER LLM, anomaly, classify, RAG)."""
    from app.llm_routing import route_completion

    result = route_completion(
        tenant_id=tenant_id,
        messages=messages,
        model_override=model,
        temperature=temperature,
        max_tokens=max_tokens,
    )
    _meter_usage(
        tenant_id, result.model,
        result.input_tokens, result.output_tokens,
        result.cost_usd, result.elapsed_ms,
    )
    _emit_billing_usage(
        tenant_id=tenant_id,
        model=result.model,
        provider=result.provider,
        input_tokens=result.input_tokens,
        output_tokens=result.output_tokens,
        cost_usd=result.cost_usd,
        elapsed_ms=result.elapsed_ms,
        fallback_used=result.fallback_used,
    )
    return {
        "content": result.content,
        "model": result.model,
        "provider": result.provider,
        "input_tokens": result.input_tokens,
        "output_tokens": result.output_tokens,
        "cost_usd": result.cost_usd,
        "elapsed_ms": result.elapsed_ms,
        "fallback_used": result.fallback_used,
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
    # ADR 0081: pull tenant config through the new repo so streaming
    # and non-streaming paths see the same provider/model/key/limits.
    # Air-gapped enforcement also runs here — streaming has no
    # fallback, but it must still fail closed against external
    # providers when air_gapped is true.
    from app.llm_routing import (
        AirGappedError, BreakerOpenError, BudgetExceededError,
        _check_air_gapped, _check_breaker, _check_budget,
        _record_failure, _record_success,
        _classify_error, resolve_provider,
    )
    from app.tenant_llm_config_repo import load_for_gateway
    config = asyncio.run(load_for_gateway(tenant_id))

    llm_model = model or config.get("model") or settings.default_llm_model
    api_key = config.get("api_key")
    api_base = config.get("base_url")

    provider = resolve_provider(llm_model)
    # Pre-sale item 10: streaming path must fail closed too — no tenant
    # key means no call, unless the operator explicitly opted in to
    # deploy-credential fallback.
    require_tenant_key(api_key, tenant_id=tenant_id, provider=provider)
    _check_air_gapped(config, provider)
    _check_budget(tenant_id, config.get("daily_budget_usd") or 0.0)
    _check_breaker(tenant_id, provider)

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
        _record_failure(tenant_id, provider, _classify_error(litellm.RateLimitError("rl")))
        raise
    except (litellm.Timeout, litellm.APIConnectionError, litellm.APIError) as exc:
        _record_failure(tenant_id, provider, _classify_error(exc))
        raise
    else:
        _record_success(tenant_id, provider)

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
    _emit_billing_usage(
        tenant_id=tenant_id,
        model=llm_model,
        provider=provider,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        cost_usd=cost,
        elapsed_ms=elapsed_ms,
        fallback_used=False,  # streaming has no fallback
    )
    yield "", True, {
        "model": llm_model,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "cost_usd": cost,
        "elapsed_ms": elapsed_ms,
        "full_text": full_text,
    }


def _emit_billing_usage(
    *,
    tenant_id: str,
    model: str,
    provider: str,
    input_tokens: int,
    output_tokens: int,
    cost_usd: float,
    elapsed_ms: int,
    fallback_used: bool = False,
    user_id: str | None = None,
) -> None:
    """ADR 0081 — fire-and-forget publish of dms.billing.llm.usage.v1
    to the BILLING_EVENTS JetStream stream. The billing service rolls
    these into per-tenant invoice lines (see services/billing).

    Best-effort: a NATS hiccup must not fail the LLM call. The Redis
    counter is the source of truth for the admin dashboard's
    rolling-30-day view; the billing event is the source of truth
    for the invoice.

    cost_usd is converted to integer cents to avoid float drift in
    the billing aggregation (the billing service stores everything
    as bigint cents)."""
    import uuid as _uuid
    from datetime import datetime, timezone

    cost_cents = int(round(float(cost_usd or 0.0) * 100))
    envelope = {
        "specversion": "1.0",
        "id": str(_uuid.uuid4()),
        "source": "dms.intelligence",
        "type": BILLING_LLM_USAGE_SUBJECT,
        "subject": f"tenant/{tenant_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "data": {
            "tenant_id":       tenant_id,
            "user_id":         user_id,
            "model":           model,
            "provider":        provider,
            "input_tokens":    int(input_tokens or 0),
            "output_tokens":   int(output_tokens or 0),
            "cost_usd_cents":  cost_cents,
            "elapsed_ms":      int(elapsed_ms or 0),
            "fallback_used":   bool(fallback_used),
        },
    }
    try:
        from app.events.publisher import publish_cloudevent
        asyncio.run(publish_cloudevent(BILLING_LLM_USAGE_SUBJECT, envelope))
    except Exception as e:  # noqa: BLE001 — explicitly fire-and-forget
        log.warning("billing.llm.usage publish failed: %s", e)


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
