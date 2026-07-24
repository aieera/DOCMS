"""Regression tests for the /rag/query provider mismatch (500).

Two bugs compounded: (1) the workspace answer_model fell back to a
hard-coded anthropic model instead of deferring to the tenant's ADR-0081
routing, and (2) route_completion handed the tenant's api_key to
WHATEVER provider the model resolved to — so an OpenAI-configured
tenant's key was sent to Anthropic ("invalid x-api-key").
"""
import asyncio

from app.llm_routing import _credentials_for
from app.rag_persist import get_workspace_ai_settings

OPENAI_CFG = {
    "provider": "openai",
    "model": "openai/gpt-4-turbo",
    "api_key": "sk-openai-secret",
    "base_url": "https://api.openai.example",
}


def test_tenant_wide_settings_defer_model_to_tenant_routing():
    # workspace_id=None (the "All workspaces" ask) must NOT force a
    # model — None lets route_completion resolve the tenant config.
    row = asyncio.run(get_workspace_ai_settings(tenant_id="t-1", workspace_id=None))
    assert row["rag_enabled"] is True
    assert row["answer_model"] is None


def test_credentials_only_flow_to_their_own_provider():
    # Matching provider → tenant key + base_url.
    key, base = _credentials_for(OPENAI_CFG, "openai")
    assert key == "sk-openai-secret"
    assert base == "https://api.openai.example"
    # Mismatched provider → neither; litellm falls back to env keys.
    key, base = _credentials_for(OPENAI_CFG, "anthropic")
    assert key is None
    assert base is None


def test_credentials_none_config_is_safe():
    key, base = _credentials_for({}, "openai")
    assert key is None
    assert base is None
