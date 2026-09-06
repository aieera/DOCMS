"""Pre-sale item 10: with no tenant API key configured, LLM calls fell
back to the deploy-default credentials (litellm reads the deploy's env
vars when api_key is None) — tenant document content silently left on
the operator's account. The default must be fail-closed; the fallback is
an explicit operator opt-in (SEDOC_LLM_ALLOW_DEPLOY_CREDENTIALS=true)."""
import pytest

from app.config import settings
from app.llm_key_guard import MissingTenantLLMKeyError, require_tenant_key


def test_missing_key_raises_by_default(monkeypatch):
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    with pytest.raises(MissingTenantLLMKeyError):
        require_tenant_key(None, tenant_id="t1", provider="openai")
    with pytest.raises(MissingTenantLLMKeyError):
        require_tenant_key("", tenant_id="t1", provider="openai")


def test_tenant_key_passes(monkeypatch):
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    require_tenant_key("sk-tenant-key", tenant_id="t1", provider="openai")


def test_operator_opt_in_allows_fallback(monkeypatch):
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", True)
    require_tenant_key(None, tenant_id="t1", provider="openai")


def test_default_is_disabled():
    # The shipped default must be fail-closed.
    assert settings.model_fields["llm_allow_deploy_credentials"].default is False
