"""ADR 0081 — provider abstraction, circuit breaker, fallback,
air-gapped enforcement for the LiteLLM gateway.

Sits between llm_gateway.completion() and litellm.completion(). The
gateway loads the per-tenant config (cached); this module decides
whether the call is allowed, which model to use, whether to fall
back, and whether the breaker is open. Returns a normalized result
or raises a typed RoutingError.

Air-gapped is the load-bearing test — when a tenant is air-gapped,
NO outbound litellm call may be made for any non-vllm_local provider.
The check fires before the litellm call, not after.
"""
from __future__ import annotations

import asyncio
import logging
import os
import threading
import time
from dataclasses import dataclass
from typing import Any

import litellm

from app.config import settings

log = logging.getLogger(__name__)

# Per-(tenant, provider) breaker — closed → open after this many
# consecutive failures, then half-open after _BREAKER_OPEN_SECONDS.
# 5 + 60s mirrors the runbook + ADR 0081.
_BREAKER_THRESHOLD = 5
_BREAKER_OPEN_SECONDS = 60.0


# ---- exceptions -----------------------------------------------------

class RoutingError(Exception):
    """Base for everything llm_routing raises. Distinct from the
    underlying litellm errors so callers can react uniformly without
    importing litellm types."""
    kind: str = "routing_error"


class AirGappedError(RoutingError):
    kind = "air_gapped"


class BudgetExceededError(RoutingError):
    kind = "quota_exceeded"


class BreakerOpenError(RoutingError):
    kind = "breaker_open"


class AllProvidersDownError(RoutingError):
    """Raised when both primary and fallback failed (or fallback isn't
    configured). The gateway should surface this as 503 — every retry
    has already been attempted."""
    kind = "all_providers_down"


# ---- provider inference ---------------------------------------------

# Model ids in litellm format start with the provider namespace
# (e.g. `anthropic/claude-...`, `openai/gpt-...`, `bedrock/...`).
# We normalize to a small enum so the breaker key is stable across
# model bumps within the same provider.
_PROVIDER_PREFIXES = {
    "anthropic":   "anthropic",
    "claude":      "anthropic",     # bare claude- model ids
    "openai":      "openai",
    "gpt":         "openai",        # bare gpt-4o, gpt-3.5
    "azure":       "openai",        # azure/openai routes through OpenAI billing
    "bedrock":     "bedrock",
    "vllm":        "vllm_local",
    "huggingface": "vllm_local",
}


def resolve_provider(model: str) -> str:
    """Map a litellm model id to one of the §6.9 provider buckets.
    Falls back to 'custom' for unknowns — used as a sentinel that
    air-gapped mode rejects."""
    if not model:
        return "custom"
    prefix = model.split("/", 1)[0].split("-", 1)[0].lower()
    return _PROVIDER_PREFIXES.get(prefix, "custom")


def is_external(provider: str) -> bool:
    """True for providers that egress the on-prem perimeter."""
    return provider not in ("vllm_local",)


# ---- circuit breaker ------------------------------------------------

@dataclass
class _BreakerState:
    state: str = "closed"          # 'closed' | 'open' | 'half_open'
    failure_count: int = 0
    open_until: float = 0.0        # monotonic seconds; 0 means n/a
    last_error_kind: str = ""


_breaker_lock = threading.Lock()
_breaker_state: dict[tuple[str, str], _BreakerState] = {}


def _key(tenant_id: str, provider: str) -> tuple[str, str]:
    return (tenant_id, provider)


def _get_state(tenant_id: str, provider: str) -> _BreakerState:
    key = _key(tenant_id, provider)
    with _breaker_lock:
        if key not in _breaker_state:
            _breaker_state[key] = _BreakerState()
        return _breaker_state[key]


def _check_breaker(tenant_id: str, provider: str) -> None:
    """Raise BreakerOpenError if the breaker for (tenant, provider) is
    open and hasn't reached its half-open window yet. Side effect:
    transitions open→half_open when the window has expired so the
    next call probes."""
    s = _get_state(tenant_id, provider)
    with _breaker_lock:
        if s.state == "open":
            if time.monotonic() >= s.open_until:
                s.state = "half_open"
                log.info("breaker half_open for %s/%s after %ds cool-off",
                         tenant_id, provider, int(_BREAKER_OPEN_SECONDS))
            else:
                raise BreakerOpenError(
                    f"breaker open for {provider} (last error: {s.last_error_kind})"
                )


def _record_success(tenant_id: str, provider: str) -> None:
    s = _get_state(tenant_id, provider)
    with _breaker_lock:
        if s.state != "closed":
            log.info("breaker closing for %s/%s after success", tenant_id, provider)
        s.state = "closed"
        s.failure_count = 0
        s.open_until = 0.0
        s.last_error_kind = ""
    asyncio.run(_persist_breaker(tenant_id, provider, s))


def _record_failure(tenant_id: str, provider: str, error_kind: str) -> None:
    s = _get_state(tenant_id, provider)
    with _breaker_lock:
        s.failure_count += 1
        s.last_error_kind = error_kind
        if s.state == "half_open":
            # Probe failed — re-open immediately for another window.
            s.state = "open"
            s.open_until = time.monotonic() + _BREAKER_OPEN_SECONDS
            log.warning("breaker re-opened for %s/%s (probe failed: %s)",
                        tenant_id, provider, error_kind)
        elif s.failure_count >= _BREAKER_THRESHOLD:
            s.state = "open"
            s.open_until = time.monotonic() + _BREAKER_OPEN_SECONDS
            log.warning("breaker opened for %s/%s after %d failures (last: %s)",
                        tenant_id, provider, s.failure_count, error_kind)
    asyncio.run(_persist_breaker(tenant_id, provider, s))


async def _persist_breaker(tenant_id: str, provider: str, s: _BreakerState) -> None:
    """Mirror the in-process breaker into llm_circuit_breaker_state so
    a replica restart picks up where we left off. Best-effort —
    breaker still works in-memory if this fails."""
    try:
        from app.db.pool import get_pool
        pool = await get_pool()
        async with pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_tenant', $1, true)", tenant_id
                )
                await conn.execute(
                    """
                    INSERT INTO llm_circuit_breaker_state
                        (tenant_id, provider, state, failure_count,
                         open_until, last_error_kind, updated_at)
                    VALUES ($1, $2, $3, $4,
                            CASE WHEN $5 > 0 THEN now() + ($6 * interval '1 second')
                                 ELSE NULL END,
                            $7, now())
                    ON CONFLICT (tenant_id, provider) DO UPDATE
                       SET state           = EXCLUDED.state,
                           failure_count   = EXCLUDED.failure_count,
                           open_until      = EXCLUDED.open_until,
                           last_error_kind = EXCLUDED.last_error_kind,
                           updated_at      = now()
                    """,
                    tenant_id, provider, s.state, s.failure_count,
                    s.open_until,
                    int(_BREAKER_OPEN_SECONDS) if s.open_until > 0 else 0,
                    s.last_error_kind,
                )
    except Exception as e:
        log.warning("breaker persist failed for %s/%s: %s", tenant_id, provider, e)


# ---- budget gate ----------------------------------------------------

def _today_spend_usd(tenant_id: str) -> float:
    """Sum the day's cost across all models from the existing Redis
    meter (`llm_usage:{tenant}:{model}` hashes). The cost field is a
    rolling total, not a per-day reset — so for a hard daily budget
    we'd need a separate counter. This first iteration treats the
    rolling total as the "yesterday + today" estimate — sufficient
    for soft-cap warning, replaced by per-day buckets in a later PR."""
    try:
        from app.llm_gateway import _get_redis
        r = _get_redis()
        total = 0.0
        for key in r.scan_iter(match=f"llm_usage:{tenant_id}:*", count=100):
            h = r.hgetall(key)
            v = h.get(b"cost_usd") if h else None
            if v is None and h:
                v = h.get("cost_usd")
            try:
                total += float(v) if v is not None else 0.0
            except (TypeError, ValueError):
                pass
        return total
    except Exception as e:
        log.warning("today-spend lookup failed: %s", e)
        return 0.0


def _check_budget(tenant_id: str, daily_budget_usd: float) -> None:
    if daily_budget_usd <= 0:
        return
    spend = _today_spend_usd(tenant_id)
    if spend >= daily_budget_usd:
        raise BudgetExceededError(
            f"daily budget {daily_budget_usd:.2f} USD reached (spend={spend:.4f})"
        )


# ---- air-gapped gate ------------------------------------------------

def _air_gapped_globally() -> bool:
    return os.environ.get("VAULTDMS_AIR_GAPPED", "").strip().lower() in (
        "1", "true", "yes", "on",
    )


def _check_air_gapped(config: dict[str, Any], provider: str) -> None:
    if not (_air_gapped_globally() or config.get("air_gapped")):
        return
    if is_external(provider):
        raise AirGappedError(
            f"air-gapped tenant cannot use external provider {provider!r}"
        )


# ---- public entry point ---------------------------------------------

@dataclass
class CompletionResult:
    content: str
    model: str
    provider: str
    input_tokens: int
    output_tokens: int
    cost_usd: float
    elapsed_ms: int
    fallback_used: bool = False


def _classify_error(exc: BaseException) -> str:
    if isinstance(exc, litellm.RateLimitError):
        return "rate_limit"
    if isinstance(exc, litellm.Timeout):
        return "timeout"
    if isinstance(exc, litellm.AuthenticationError):
        return "auth_failed"
    if isinstance(exc, (litellm.APIConnectionError, litellm.APIError)):
        return "upstream_error"
    return "unknown"


def _try_call(
    *,
    tenant_id: str,
    model: str,
    api_key: str | None,
    api_base: str | None,
    messages: list[dict[str, str]],
    temperature: float,
    max_tokens: int,
) -> tuple[Any, str]:
    """Single-attempt litellm call with breaker accounting. Raises the
    underlying litellm exception on failure; returns (response, provider)
    on success."""
    provider = resolve_provider(model)
    _check_breaker(tenant_id, provider)
    try:
        resp = litellm.completion(
            model=model,
            messages=messages,
            temperature=temperature,
            max_tokens=max_tokens,
            api_key=api_key,
            api_base=api_base,
            timeout=settings.llm_timeout_seconds,
        )
    except Exception as exc:
        kind = _classify_error(exc)
        _record_failure(tenant_id, provider, kind)
        raise
    _record_success(tenant_id, provider)
    return resp, provider


def route_completion(
    *,
    tenant_id: str,
    messages: list[dict[str, str]],
    model_override: str | None = None,
    temperature: float = 0.1,
    max_tokens: int = 2000,
) -> CompletionResult:
    """Resolve config → primary call → fallback on retryable error.

    Order of operations is significant for the air-gapped guarantee:
      1. Resolve provider for the chosen model FIRST.
      2. Check air-gapped — fails closed before any litellm import-side
         effects could try to hit a network endpoint.
      3. Check budget.
      4. Check breaker.
      5. Call litellm. On rate_limit/timeout/upstream_error, try
         fallback if configured. Otherwise raise.
    """
    from app.tenant_llm_config_repo import load_for_gateway

    config = asyncio.run(load_for_gateway(tenant_id))

    primary_model = model_override or config.get("model") or settings.default_llm_model
    fallback_model = config.get("fallback_model") or ""
    api_key = config.get("api_key")
    api_base = config.get("base_url")

    primary_provider = resolve_provider(primary_model)
    _check_air_gapped(config, primary_provider)
    _check_budget(tenant_id, config.get("daily_budget_usd") or 0.0)

    start = time.monotonic()
    last_error: BaseException | None = None
    fallback_used = False

    for attempt_model in [primary_model] + ([fallback_model] if fallback_model else []):
        if not attempt_model:
            continue
        attempt_provider = resolve_provider(attempt_model)
        # Defense in depth — fallback to a different provider must
        # also pass the air-gapped check.
        try:
            _check_air_gapped(config, attempt_provider)
        except AirGappedError:
            log.info("skipping fallback %s — provider %s blocked by air-gapped",
                     attempt_model, attempt_provider)
            continue
        try:
            resp, provider = _try_call(
                tenant_id=tenant_id,
                model=attempt_model,
                api_key=api_key,
                api_base=api_base,
                messages=messages,
                temperature=temperature,
                max_tokens=max_tokens,
            )
            elapsed_ms = int((time.monotonic() - start) * 1000)
            usage = getattr(resp, "usage", None)
            input_tokens = getattr(usage, "prompt_tokens", 0) if usage else 0
            output_tokens = getattr(usage, "completion_tokens", 0) if usage else 0
            try:
                cost = float(litellm.completion_cost(completion_response=resp) or 0.0)
            except Exception:
                cost = 0.0
            content = resp.choices[0].message.content if resp.choices else ""
            return CompletionResult(
                content=content,
                model=attempt_model,
                provider=provider,
                input_tokens=input_tokens,
                output_tokens=output_tokens,
                cost_usd=cost,
                elapsed_ms=elapsed_ms,
                fallback_used=fallback_used,
            )
        except BreakerOpenError as exc:
            log.info("breaker open on %s; trying fallback", attempt_model)
            last_error = exc
            fallback_used = True
            continue
        except (litellm.RateLimitError, litellm.Timeout,
                litellm.APIConnectionError, litellm.APIError) as exc:
            log.warning("retryable error on %s: %s; trying fallback if any",
                        attempt_model, _classify_error(exc))
            last_error = exc
            fallback_used = True
            continue
        except litellm.AuthenticationError:
            # Auth errors don't get a fallback — the admin needs to
            # rotate the key. Surface immediately.
            raise

    raise AllProvidersDownError(
        f"primary={primary_model!r} fallback={fallback_model!r}; last={last_error!r}"
    )
