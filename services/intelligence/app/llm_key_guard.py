"""Tenant-key guard for every litellm call site.

Pre-sale item 10: litellm reads the deploy environment's provider keys
when api_key is None, so a tenant that configured nothing had its
document content sent to a third-party model on the OPERATOR's account.
Every call site must pass the key it is about to use through
require_tenant_key; the deploy-credential fallback is an explicit
operator opt-in (SEDOC_LLM_ALLOW_DEPLOY_CREDENTIALS), never a default.

The one keyless exception is a self-hosted model server the tenant
reaches through its own base_url (on-prem vLLM): there is no vendor
account behind it, so there is no deploy credential to substitute and
nothing for the guard to protect. See keyless_local_call.
"""
from __future__ import annotations

from app.config import settings

# Provider buckets (llm_routing.resolve_provider) that are self-hosted by
# design: no vendor account, no vendor key. Deliberately an explicit
# allow-list — a hosted vendor (openai, anthropic, gemini, bedrock, and
# the catch-all `custom`) is never keyless, with or without a base_url,
# because litellm fills a missing key from the deploy env for all of them.
KEYLESS_LOCAL_PROVIDERS: frozenset[str] = frozenset({"vllm_local"})

# litellm routes inside a keyless bucket that still read a vendor
# credential from the deploy env when api_key is None. `huggingface/` is
# bucketed as vllm_local (a self-hosted TGI endpoint is local for the
# air-gapped check) but litellm sends HF_TOKEN / HUGGINGFACE_API_KEY with
# it — to the HF Inference API, or to the tenant's base_url — so it needs
# a tenant key like any hosted vendor.
_DEPLOY_CREDENTIAL_ROUTES: frozenset[str] = frozenset({"huggingface"})


class MissingTenantLLMKeyError(RuntimeError):
    """No tenant API key and deploy-credential fallback is disabled."""


def keyless_local_call(provider: str, *, api_base: str | None, model: str | None) -> bool:
    """True when a call needs no tenant key: a KEYLESS_LOCAL_PROVIDERS
    provider, bound to an explicit tenant base_url (without one litellm
    picks its own default endpoint), on a route that does not read a
    vendor credential from the environment. Fails closed when the model
    is unknown."""
    if provider not in KEYLESS_LOCAL_PROVIDERS or not api_base or not model:
        return False
    route = model.split("/", 1)[0].lower()
    return route not in _DEPLOY_CREDENTIAL_ROUTES


def require_tenant_key(
    api_key: str | None,
    *,
    tenant_id: str,
    provider: str,
    api_base: str | None = None,
    model: str | None = None,
) -> None:
    if api_key:
        return
    if keyless_local_call(provider, api_base=api_base, model=model):
        return
    if settings.llm_allow_deploy_credentials:
        return
    raise MissingTenantLLMKeyError(
        f"tenant {tenant_id} has no API key for provider {provider!r} and "
        "deploy-credential fallback is disabled "
        "(SEDOC_LLM_ALLOW_DEPLOY_CREDENTIALS=false); configure a tenant key "
        "in Admin → AI & Models"
    )
