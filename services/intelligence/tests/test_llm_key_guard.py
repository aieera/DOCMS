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


# ---- keyless self-hosted providers ------------------------------------

def test_local_provider_on_tenant_base_url_is_keyless(monkeypatch):
    # On-prem vLLM has no vendor key and no operator account to bill, so
    # there is no deploy credential for litellm to substitute.
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    require_tenant_key(None, tenant_id="t1", provider="vllm_local",
                       api_base="http://vllm.internal:8000/v1",
                       model="vllm/meta-llama/Llama-3.1")


@pytest.mark.parametrize("provider,model", [
    ("openai",    "openai/gpt-4o-mini"),
    ("anthropic", "anthropic/claude-haiku-4-5"),
    ("gemini",    "gemini/gemini-2.0-flash"),
    ("bedrock",   "bedrock/anthropic.claude-3"),
    ("custom",    "some-random-model"),
])
def test_hosted_provider_without_key_still_raises_even_with_base_url(monkeypatch, provider, model):
    # A base_url (Azure, a gateway, a proxy) does not make a hosted vendor
    # keyless: litellm still fills the missing key from the deploy env.
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    with pytest.raises(MissingTenantLLMKeyError):
        require_tenant_key(None, tenant_id="t1", provider=provider,
                           api_base="https://llm-proxy.example.com/v1", model=model)


def test_local_provider_without_base_url_still_raises(monkeypatch):
    # No explicit self-hosted endpoint means litellm picks its own default.
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    with pytest.raises(MissingTenantLLMKeyError):
        require_tenant_key(None, tenant_id="t1", provider="vllm_local",
                           api_base=None, model="vllm/meta-llama/Llama-3.1")
    with pytest.raises(MissingTenantLLMKeyError):
        require_tenant_key(None, tenant_id="t1", provider="vllm_local",
                           api_base="http://vllm.internal:8000/v1", model=None)


def test_huggingface_route_is_never_keyless(monkeypatch):
    # huggingface/ is bucketed vllm_local, but litellm sends the deploy's
    # HF_TOKEN / HUGGINGFACE_API_KEY with it — to the tenant's base_url.
    monkeypatch.setattr(settings, "llm_allow_deploy_credentials", False)
    with pytest.raises(MissingTenantLLMKeyError):
        require_tenant_key(None, tenant_id="t1", provider="vllm_local",
                           api_base="https://tgi.example.com", model="huggingface/mistral")
