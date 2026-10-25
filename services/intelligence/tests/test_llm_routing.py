"""ADR 0081 — provider routing, circuit breaker, fallback, air-gapped.

Tests that don't require live LLM calls — every litellm interaction is
stubbed via mock.patch. Focus is on the routing policy itself: which
model gets called, when fallback fires, when the breaker opens, and
the load-bearing air-gapped fail-closed invariant.
"""
from __future__ import annotations

import logging
import socket
from unittest import mock

import litellm
import pytest

from app import llm_routing, url_guard
from app.config import settings
from app.llm_key_guard import MissingTenantLLMKeyError
from app.llm_routing import (
    AirGappedError,
    AllProvidersDownError,
    BreakerOpenError,
    BudgetExceededError,
    _BREAKER_THRESHOLD,
    _breaker_state,
    resolve_provider,
    is_external,
    route_completion,
)


@pytest.fixture(autouse=True)
def _reset_breaker_state():
    """Each test gets a clean breaker — otherwise a failure in one
    test bleeds breaker state into the next and we can't reason about
    threshold counts."""
    _breaker_state.clear()
    yield
    _breaker_state.clear()


# ---- resolve_provider --------------------------------------------------

@pytest.mark.parametrize("model,expected", [
    ("anthropic/claude-haiku-4-5", "anthropic"),
    ("openai/gpt-4o-mini",         "openai"),
    ("azure/gpt-4o",               "openai"),
    ("bedrock/anthropic.claude-3", "bedrock"),
    ("vllm/meta-llama/Llama-3.1",  "vllm_local"),
    ("huggingface/mistral",        "vllm_local"),
    ("claude-3-haiku",             "anthropic"),
    ("gpt-4o",                     "openai"),
    ("",                           "custom"),
    ("some-random-model",          "custom"),
])
def test_resolve_provider_maps_known_namespaces(model, expected):
    assert resolve_provider(model) == expected


def test_is_external_classifies_local_provider():
    assert is_external("openai") is True
    assert is_external("anthropic") is True
    assert is_external("bedrock") is True
    assert is_external("vllm_local") is False


# ---- routing: tenant A vs tenant B ------------------------------------

def _stub_response(content="ok"):
    """Litellm returns an object with .choices[0].message.content +
    .usage.prompt_tokens / .completion_tokens. Build the smallest
    duck-type that route_completion will accept."""
    msg = mock.Mock()
    msg.content = content
    choice = mock.Mock()
    choice.message = msg
    resp = mock.Mock()
    resp.choices = [choice]
    resp.usage = mock.Mock(prompt_tokens=5, completion_tokens=3)
    return resp


def test_routing_calls_correct_model_per_tenant():
    """The §6.9 acceptance test: tenant A on OpenAI calls openai/...;
    tenant B on Anthropic calls anthropic/...; both work
    independently in the same process."""
    configs = {
        "tenant-A": {
            "provider": "openai", "model": "openai/gpt-4o-mini",
            "fallback_model": "", "api_key": "sk-A",
            "base_url": None, "rate_limit_rpm": 60,
            "daily_budget_usd": 0.0, "air_gapped": False,
        },
        "tenant-B": {
            "provider": "anthropic", "model": "anthropic/claude-haiku-4-5",
            "fallback_model": "", "api_key": "sk-B",
            "base_url": None, "rate_limit_rpm": 60,
            "daily_budget_usd": 0.0, "air_gapped": False,
        },
    }

    async def _fake_load(tid):
        return configs[tid]

    completion_calls: list[dict] = []

    def _fake_completion(**kwargs):
        completion_calls.append(kwargs)
        return _stub_response()

    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", side_effect=_fake_completion):
        rA = route_completion(
            tenant_id="tenant-A",
            messages=[{"role": "user", "content": "hi"}],
        )
        rB = route_completion(
            tenant_id="tenant-B",
            messages=[{"role": "user", "content": "hi"}],
        )

    assert rA.model == "openai/gpt-4o-mini" and rA.provider == "openai"
    assert rB.model == "anthropic/claude-haiku-4-5" and rB.provider == "anthropic"
    # Each tenant got its own api_key passed through to litellm.
    assert completion_calls[0]["api_key"] == "sk-A"
    assert completion_calls[1]["api_key"] == "sk-B"


# ---- air-gapped enforcement -------------------------------------------

def test_air_gapped_blocks_external_provider_before_litellm_call():
    """Load-bearing privacy invariant: when a tenant is air-gapped,
    no litellm call may be made for any non-vllm_local provider.
    The check fires before route_completion reaches litellm."""
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": True,
    }

    async def _fake_load(_):
        return config

    completion_mock = mock.Mock(return_value=_stub_response())
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", completion_mock):
        with pytest.raises(AirGappedError):
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
    # The litellm call MUST NOT have happened.
    completion_mock.assert_not_called()


def test_air_gapped_allows_vllm_local(monkeypatch):
    """The on-prem shape: a keyless vLLM server on an operator
    allow-listed private host. No tenant key is needed (nothing to bill,
    no deploy credential to substitute) even with deploy-credential
    fallback off, and the M-23 base_url guard still runs."""
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    monkeypatch.setattr(settings, "llm_private_base_url_allowlist", "vllm.local")
    config = {
        "provider": "vllm_local", "model": "vllm/llama-3.1",
        "fallback_model": "", "api_key": None,
        "base_url": "http://vllm.local/v1", "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": True,
    }

    async def _fake_load(_):
        return config

    def _resolve_private(host, port, *a, **k):
        assert host == "vllm.local"
        return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", ("10.0.0.5", port))]

    completion_mock = mock.Mock(return_value=_stub_response())
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch.object(url_guard.socket, "getaddrinfo", side_effect=_resolve_private), \
         mock.patch("app.llm_routing._persist_breaker", new_callable=mock.AsyncMock), \
         mock.patch("app.llm_routing.litellm.completion", completion_mock):
        out = route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
    assert out.provider == "vllm_local"
    kwargs = completion_mock.call_args.kwargs
    assert kwargs["api_key"] is None
    assert kwargs["api_base"] == "http://vllm.local/v1"


def test_air_gapped_global_env_overrides_per_tenant_false(monkeypatch):
    """SEDOC_AIR_GAPPED=1 forces air-gapped for every tenant
    regardless of the per-tenant flag — the on-prem default."""
    monkeypatch.setenv("SEDOC_AIR_GAPPED", "1")
    config = {
        "provider": "anthropic", "model": "anthropic/claude-haiku-4-5",
        "fallback_model": "", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,  # tenant says no
    }

    async def _fake_load(_):
        return config

    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", return_value=_stub_response()):
        with pytest.raises(AirGappedError):
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])


# ---- fallback ---------------------------------------------------------

def _rate_limited_primary(calls: list[dict], primary="openai/gpt-4o-mini"):
    """litellm stub: the primary model is rate limited, anything else
    answers "fallback-answer"."""
    rate_err = litellm.RateLimitError(message="rl", model=primary, llm_provider="openai")

    def _fake_completion(**kwargs):
        calls.append(kwargs)
        if kwargs["model"] == primary:
            raise rate_err
        return _stub_response("fallback-answer")
    return _fake_completion


def test_fallback_fires_on_rate_limit_error(monkeypatch):
    """Primary returns RateLimitError → route_completion falls back
    to fallback_model. fallback_used flag set in the result. The
    fallback is on the tenant's own provider, so the tenant key covers
    it (no deploy credential involved)."""
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "openai/gpt-4o", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    calls: list[dict] = []
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing._persist_breaker", new_callable=mock.AsyncMock), \
         mock.patch("app.llm_routing.litellm.completion", side_effect=_rate_limited_primary(calls)):
        out = route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])

    assert [c["model"] for c in calls] == ["openai/gpt-4o-mini", "openai/gpt-4o"]
    assert [c["api_key"] for c in calls] == ["sk-x", "sk-x"]
    assert out.model == "openai/gpt-4o"
    assert out.provider == "openai"
    assert out.fallback_used is True
    assert out.content == "fallback-answer"


def test_cross_provider_fallback_without_tenant_key_is_skipped(monkeypatch, caplog):
    """Pre-sale item 10 vs ADR 0081 fallback: the tenant key belongs to
    openai, so an anthropic fallback could only run on the deploy's env
    key. It is skipped with a logged reason (never called), and the
    primary's rate limit surfaces as AllProvidersDownError — exactly as
    if no fallback were configured."""
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-deploy-should-never-be-used")
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "anthropic/claude-haiku-4-5", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    calls: list[dict] = []
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing._persist_breaker", new_callable=mock.AsyncMock), \
         mock.patch("app.llm_routing.litellm.completion", side_effect=_rate_limited_primary(calls)), \
         caplog.at_level(logging.WARNING, logger="app.llm_routing"):
        with pytest.raises(AllProvidersDownError) as excinfo:
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])

    assert [c["model"] for c in calls] == ["openai/gpt-4o-mini"]   # fallback never called
    assert "RateLimitError" in str(excinfo.value)                   # primary error surfaces
    assert any("skipping fallback anthropic/claude-haiku-4-5" in r.getMessage()
               and "no tenant API key for provider anthropic" in r.getMessage()
               for r in caplog.records)


def test_cross_provider_fallback_runs_on_operator_opt_in(monkeypatch):
    """With SEDOC_LLM_ALLOW_DEPLOY_CREDENTIALS=true the operator accepted
    deploy-credential calls, so the ADR 0081 cross-provider fallback
    runs — without the tenant's openai key leaking to anthropic."""
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", True)
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "anthropic/claude-haiku-4-5", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    calls: list[dict] = []
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing._persist_breaker", new_callable=mock.AsyncMock), \
         mock.patch("app.llm_routing.litellm.completion", side_effect=_rate_limited_primary(calls)):
        out = route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])

    assert [c["model"] for c in calls] == ["openai/gpt-4o-mini", "anthropic/claude-haiku-4-5"]
    assert calls[1]["api_key"] is None          # never the openai key
    assert out.provider == "anthropic"
    assert out.fallback_used is True


def test_keyless_primary_raises_without_trying_fallback(monkeypatch):
    """The fallback skip is for the fallback only: a primary with no
    tenant key is a config error and surfaces as MissingTenantLLMKeyError,
    with no provider call at all."""
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "openai/gpt-4o", "api_key": None,
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    completion_mock = mock.Mock(return_value=_stub_response())
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", completion_mock):
        with pytest.raises(MissingTenantLLMKeyError):
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
    completion_mock.assert_not_called()


def test_fallback_does_not_fire_on_auth_error():
    """Auth errors should surface to the admin (rotate the key) — not
    silently roll over to fallback. Re-raised as AuthenticationError."""
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "anthropic/claude-haiku-4-5", "api_key": "sk-bad",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    auth_err = litellm.AuthenticationError(
        message="bad key", model="openai/gpt-4o-mini", llm_provider="openai",
    )

    completion_mock = mock.Mock(side_effect=auth_err)
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", completion_mock):
        with pytest.raises(litellm.AuthenticationError):
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
    # Only one call — fallback never reached.
    assert completion_mock.call_count == 1


def test_no_fallback_raises_all_providers_down():
    """Primary fails with retryable error and there's no
    fallback_model — surface AllProvidersDownError."""
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    timeout_err = litellm.Timeout(
        message="timeout", model="openai/gpt-4o-mini", llm_provider="openai",
    )

    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", side_effect=timeout_err):
        with pytest.raises(AllProvidersDownError):
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])


# ---- circuit breaker --------------------------------------------------

def test_breaker_opens_after_threshold_failures():
    """After _BREAKER_THRESHOLD consecutive retryable failures, the
    next call short-circuits with BreakerOpenError without hitting
    litellm again."""
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    timeout_err = litellm.Timeout(
        message="timeout", model="openai/gpt-4o-mini", llm_provider="openai",
    )

    completion_mock = mock.Mock(side_effect=timeout_err)
    # Persist is best-effort; bypass it so the test doesn't need a DB.
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", completion_mock), \
         mock.patch("app.llm_routing._persist_breaker", new_callable=mock.AsyncMock):
        # Trip the breaker — each iteration raises AllProvidersDownError,
        # but the per-(tenant, provider) failure count climbs.
        for _ in range(_BREAKER_THRESHOLD):
            with pytest.raises(AllProvidersDownError):
                route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
        # Breaker is now open — next call raises AllProvidersDownError
        # because no fallback. But importantly: no further litellm
        # calls are made.
        calls_before = completion_mock.call_count
        with pytest.raises(AllProvidersDownError):
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
        assert completion_mock.call_count == calls_before, \
            "breaker-open call must not invoke litellm"


def test_breaker_opens_for_one_provider_only():
    """The breaker is keyed on (tenant, provider). Tripping openai
    must NOT block calls to anthropic for the same tenant."""
    # Trip the openai breaker manually (cheaper than 5 fake failures).
    s = llm_routing._get_state("t", "openai")
    s.state = "open"
    import time as _t
    s.open_until = _t.monotonic() + 60.0
    s.last_error_kind = "timeout"

    config = {
        "provider": "anthropic", "model": "anthropic/claude-haiku-4-5",
        "fallback_model": "", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", return_value=_stub_response()), \
         mock.patch("app.llm_routing._persist_breaker", new_callable=mock.AsyncMock):
        out = route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
    assert out.provider == "anthropic"


def test_breaker_success_closes_after_recovery():
    """A successful call clears failure_count + sets state=closed."""
    s = llm_routing._get_state("t", "openai")
    s.state = "half_open"
    s.failure_count = 4

    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 0.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing.litellm.completion", return_value=_stub_response()), \
         mock.patch("app.llm_routing._persist_breaker", new_callable=mock.AsyncMock):
        route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])

    assert s.state == "closed"
    assert s.failure_count == 0


# ---- budget gate ------------------------------------------------------

def test_daily_budget_short_circuits_when_exceeded():
    """Budget gate fires before any litellm call — the spec needs a
    hard cap, not 'one more call past the limit'."""
    config = {
        "provider": "openai", "model": "openai/gpt-4o-mini",
        "fallback_model": "", "api_key": "sk-x",
        "base_url": None, "rate_limit_rpm": 60,
        "daily_budget_usd": 1.0, "air_gapped": False,
    }

    async def _fake_load(_):
        return config

    completion_mock = mock.Mock(return_value=_stub_response())
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", side_effect=_fake_load), \
         mock.patch("app.llm_routing._today_spend_usd", return_value=1.5), \
         mock.patch("app.llm_routing.litellm.completion", completion_mock):
        with pytest.raises(BudgetExceededError):
            route_completion(tenant_id="t", messages=[{"role": "user", "content": "x"}])
    completion_mock.assert_not_called()
