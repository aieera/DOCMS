"""Tenant-key guard for every litellm call site.

Pre-sale item 10: litellm reads the deploy environment's provider keys
when api_key is None, so a tenant that configured nothing had its
document content sent to a third-party model on the OPERATOR's account.
Every call site must pass the key it is about to use through
require_tenant_key; the deploy-credential fallback is an explicit
operator opt-in (SEDOC_LLM_ALLOW_DEPLOY_CREDENTIALS), never a default.
"""
from __future__ import annotations

from app.config import settings


class MissingTenantLLMKeyError(RuntimeError):
    """No tenant API key and deploy-credential fallback is disabled."""


def require_tenant_key(api_key: str | None, *, tenant_id: str, provider: str) -> None:
    if api_key:
        return
    if settings.llm_allow_deploy_credentials:
        return
    raise MissingTenantLLMKeyError(
        f"tenant {tenant_id} has no API key for provider {provider!r} and "
        "deploy-credential fallback is disabled "
        "(SEDOC_LLM_ALLOW_DEPLOY_CREDENTIALS=false); configure a tenant key "
        "in Admin → AI & Models"
    )
