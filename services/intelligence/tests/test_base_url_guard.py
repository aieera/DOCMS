"""M-23 — a tenant admin's LLM base_url cannot point the intelligence pod
at internal addresses.

base_url became litellm's api_base after only a scheme check: a POST
SSRF from the intelligence pod (cloud metadata, in-cluster services) with
part of the upstream error echoed back through the 503 detail.
"""
from __future__ import annotations

import socket
from unittest import mock

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app import url_guard
from app.config import settings
from app.identity import Identity, require_identity


def _resolves_to(*ips):
    def fake(host, port, *a, **k):
        return [(socket.AF_INET6 if ":" in ip else socket.AF_INET, socket.SOCK_STREAM, 6, "",
                 (ip, port or 0)) for ip in ips]
    return mock.patch.object(url_guard.socket, "getaddrinfo", side_effect=fake)


@pytest.mark.parametrize("url,ips", [
    ("http://169.254.169.254/latest/meta-data/", ["169.254.169.254"]),
    ("http://127.0.0.1:8000/v1", ["127.0.0.1"]),
    ("http://localhost:11434", ["127.0.0.1", "::1"]),
    ("https://internal.corp/v1", ["10.1.2.3"]),
    ("https://cgnat.example/v1", ["100.64.0.7"]),
    ("https://v6.example/v1", ["fd00::1"]),
    ("https://mapped.example/v1", ["::ffff:192.168.1.1"]),
    ("https://rebind.example/v1", ["93.184.216.34", "192.168.0.10"]),  # any private answer
    ("http://redis:6379", ["172.18.0.5"]),
])
def test_base_url_private_rejected(url, ips):
    with _resolves_to(*ips):
        with pytest.raises(url_guard.UnsafeBaseURLError):
            url_guard.check_base_url(url, provider="openai")


def test_base_url_public_allowed():
    with _resolves_to("93.184.216.34"):
        url_guard.check_base_url("https://api.example.com/v1", provider="openai")


def test_base_url_unresolvable_or_malformed_rejected():
    with mock.patch.object(url_guard.socket, "getaddrinfo", side_effect=socket.gaierror("nx")):
        with pytest.raises(url_guard.UnsafeBaseURLError):
            url_guard.check_base_url("https://nx.example/v1", provider="openai")
    for bad in ("ftp://x/", "http://", "file:///etc/passwd", "https://user@/x"):
        with pytest.raises(url_guard.UnsafeBaseURLError):
            url_guard.check_base_url(bad, provider="openai")


def test_vllm_local_private_host_needs_allow_list(monkeypatch):
    with _resolves_to("10.0.0.5"):
        monkeypatch.setattr(settings, "llm_private_base_url_allowlist", "")
        with pytest.raises(url_guard.UnsafeBaseURLError):
            url_guard.check_base_url("http://vllm.internal:8000/v1", provider="vllm_local")
        monkeypatch.setattr(settings, "llm_private_base_url_allowlist", "vllm.internal, other")
        url_guard.check_base_url("http://vllm.internal:8000/v1", provider="vllm_local")
        # The allow-list is for the on-prem provider only.
        with pytest.raises(url_guard.UnsafeBaseURLError):
            url_guard.check_base_url("http://vllm.internal:8000/v1", provider="openai")


@pytest.fixture()
def admin_client():
    from app.api.routes import admin_router, router
    app = FastAPI()
    app.include_router(router)
    app.include_router(admin_router)
    app.dependency_overrides[require_identity] = lambda: Identity(
        tenant_id="t1", user_id="u1", role="admin")
    return TestClient(app)


def test_put_llm_config_rejects_private_base_url(admin_client):
    async def full(_t):
        return {"provider": "openai"}

    with _resolves_to("169.254.169.254"), \
         mock.patch("app.tenant_llm_config_repo.get_full_config", new=full), \
         mock.patch("app.tenant_llm_config_repo.upsert_config") as upsert:
        r = admin_client.put("/api/v1/admin/tenant/llm-config",
                             json={"base_url": "http://metadata.internal/"})
    assert r.status_code == 400
    assert "169.254" not in r.text
    upsert.assert_not_called()


def test_call_time_guard_blocks_stored_private_base_url():
    """A base_url stored before this fix (or a DNS answer that changed)
    is re-checked before every provider call."""
    from app import llm_routing

    async def cfg(_t):
        return {"provider": "openai", "model": "openai/gpt-4o", "fallback_model": "",
                "api_key": "sk-tenant", "base_url": "http://169.254.169.254/",
                "rate_limit_rpm": 60, "daily_budget_usd": 0.0, "air_gapped": False}

    provider = mock.Mock(side_effect=AssertionError("provider call must not happen"))
    with _resolves_to("169.254.169.254"), \
         mock.patch("app.tenant_llm_config_repo.load_for_gateway", new=cfg), \
         mock.patch.object(llm_routing.litellm, "completion", provider):
        with pytest.raises(url_guard.UnsafeBaseURLError):
            llm_routing.route_completion(tenant_id="t1",
                                         messages=[{"role": "user", "content": "hi"}])
    provider.assert_not_called()


def test_completions_503_is_generic(admin_client):
    from app.llm_routing import AllProvidersDownError

    leak = "primary='x'; last=APIError('<html>internal admin console token=abc</html>')"
    with mock.patch("app.llm_gateway.completion", side_effect=AllProvidersDownError(leak)):
        r = admin_client.post("/api/v1/intelligence/llm/completions",
                              json={"messages": [{"role": "user", "content": "hi"}]})
    assert r.status_code == 503
    assert "internal admin console" not in r.text and "token" not in r.text

    with mock.patch("app.llm_gateway.completion",
                    side_effect=url_guard.UnsafeBaseURLError("10.0.0.1 is private")):
        r = admin_client.post("/api/v1/intelligence/llm/completions",
                              json={"messages": [{"role": "user", "content": "hi"}]})
    assert r.status_code == 503
    assert "10.0.0.1" not in r.text
