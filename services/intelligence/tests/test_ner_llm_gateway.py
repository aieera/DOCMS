"""M-19 — LLM NER goes through llm_gateway.completion.

It used to call litellm.acompletion directly: no tenant-key guard, no
air-gap check, no budget, no breaker, no billing, and with no per-tenant
NER key litellm fell back to the deployment's ANTHROPIC/OPENAI env keys,
so the first 8,000 characters of every document left the perimeter on
the operator's account.
"""
from __future__ import annotations

from unittest import mock

import pytest

from app.tasks import ner_llm
from app.tasks.ner_llm import DEFAULT_NER_CONFIG, NERConfig

TEXT = "Governing law shall be the State of Delaware."
GOOD = ('{"entities": [{"type": "governing_law", "value": "Delaware",'
        ' "char_start": 36, "char_end": 44, "confidence": 0.9}]}')


def _cfg(**kw) -> NERConfig:
    return NERConfig.from_dict({**DEFAULT_NER_CONFIG, **kw, "llm_enabled": True})


class _NoDirectLitellm:
    """Any direct provider call fails the test."""

    @staticmethod
    async def acompletion(**_kw):
        raise AssertionError("ner_llm must not call litellm directly")

    @staticmethod
    def completion(**_kw):
        raise AssertionError("provider call must not happen")


@pytest.mark.asyncio
async def test_ner_llm_uses_gateway_guards(monkeypatch):
    calls = []

    def fake_completion(**kw):
        calls.append(kw)
        return {"content": GOOD, "model": kw.get("model")}

    with mock.patch.dict("sys.modules", {"litellm": _NoDirectLitellm}), \
         mock.patch("app.llm_gateway.completion", side_effect=fake_completion):
        out = await ner_llm.extract_via_llm(TEXT, _cfg(llm_model="anthropic/claude-haiku-4-5"),
                                            tenant_id="tenant-1")

    assert [e["entity_value"] for e in out] == ["Delaware"]
    assert len(calls) == 1
    assert calls[0]["tenant_id"] == "tenant-1"           # tenant-scoped routing
    assert calls[0]["model"] == "anthropic/claude-haiku-4-5"
    assert "api_key" not in calls[0]                      # no key smuggled around the guard


@pytest.mark.asyncio
async def test_ner_llm_no_tenant_key_no_env_fallback(monkeypatch):
    """No tenant key + deploy-credential fallback off => the gateway's key
    guard refuses and NO provider call is made (previously litellm read
    ANTHROPIC_API_KEY from the environment)."""
    from app import llm_gateway, llm_routing
    from app.config import settings

    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-deploy-should-never-be-used")

    async def no_key_config(_tenant):
        return {"provider": "anthropic", "model": "", "fallback_model": "",
                "api_key": None, "base_url": None, "rate_limit_rpm": 60,
                "daily_budget_usd": 0.0, "air_gapped": False}

    provider = mock.Mock(side_effect=AssertionError("provider call must not happen"))
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", new=no_key_config), \
         mock.patch.object(llm_routing.litellm, "completion", provider), \
         mock.patch.object(llm_gateway, "_meter_usage"), \
         mock.patch.object(llm_gateway, "_emit_billing_usage"):
        out = await ner_llm.extract_via_llm(TEXT, _cfg(), tenant_id="tenant-1")

    assert out == []
    provider.assert_not_called()


@pytest.mark.asyncio
async def test_ner_llm_air_gapped_tenant_blocked(monkeypatch):
    from app import llm_routing

    async def air_gapped(_tenant):
        return {"provider": "anthropic", "model": "", "fallback_model": "",
                "api_key": "sk-tenant", "base_url": None, "rate_limit_rpm": 60,
                "daily_budget_usd": 0.0, "air_gapped": True}

    provider = mock.Mock(side_effect=AssertionError("provider call must not happen"))
    with mock.patch("app.tenant_llm_config_repo.load_for_gateway", new=air_gapped), \
         mock.patch.object(llm_routing.litellm, "completion", provider):
        out = await ner_llm.extract_via_llm(TEXT, _cfg(), tenant_id="tenant-1")
    assert out == []
    provider.assert_not_called()


@pytest.mark.asyncio
async def test_ner_llm_without_tenant_is_skipped():
    with mock.patch("app.llm_gateway.completion") as gw:
        assert await ner_llm.extract_via_llm(TEXT, _cfg(), tenant_id="") == []
    gw.assert_not_called()
