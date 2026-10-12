"""C-8 / DEC-4 — the intelligence API authenticates every request.

Before: tenant, user, groups and role came straight from X-Auth-Tenant-ID
/ X-User-ID / X-Group-IDs / X-User-Role, so anything that reached the
published port (or the host-mode Vite proxy) was an anonymous
cross-tenant RAG / conversation-read / LLM-config-rewrite surface.

Now `require_identity` demands a valid X-Gateway-Signature and takes the
identity from the dms_session cookie (via auth /me) — or from headers
only when the X-Internal-Service-Key matches.
"""
from __future__ import annotations

import hashlib
import json

import httpx
import pytest
from fastapi import Depends, FastAPI
from fastapi.testclient import TestClient

from app import identity
from app.config import settings

SIG = "gw-secret-123"
PREV_SIG = "gw-secret-old"
INTERNAL = "internal-key-456"
TENANT = "11111111-1111-1111-1111-111111111111"
USER = "22222222-2222-2222-2222-222222222222"
VICTIM = "99999999-9999-9999-9999-999999999999"


class _FakeRedis:
    def __init__(self):
        self.store: dict[str, bytes] = {}
        self.ttls: dict[str, int] = {}

    def get(self, k):
        return self.store.get(k)

    def setex(self, k, ttl, v):
        self.store[k] = v.encode() if isinstance(v, str) else v
        self.ttls[k] = ttl


@pytest.fixture()
def env(monkeypatch):
    monkeypatch.setattr(settings, "gateway_secret", SIG)
    monkeypatch.setattr(settings, "gateway_secret_prev", PREV_SIG)
    monkeypatch.setattr(settings, "internal_api_key", INTERNAL)
    monkeypatch.setattr(settings, "auth_url", "http://auth.test:8080")
    fake = _FakeRedis()
    monkeypatch.setattr(identity, "_redis", lambda: fake)
    calls: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append(request)
        if request.url.path != "/api/v1/auth/me":
            return httpx.Response(404)
        if "dms_session=good-cookie" not in request.headers.get("cookie", ""):
            return httpx.Response(401, json={"error": "unauthenticated"})
        return httpx.Response(200, json={
            "id": USER, "tenant_id": TENANT, "role": "member",
            "status": "active", "email": "a@example.com",
        })

    monkeypatch.setattr(
        identity, "_http_client",
        lambda: httpx.Client(transport=httpx.MockTransport(handler)),
    )
    return {"redis": fake, "calls": calls}


def _probe_app() -> TestClient:
    app = FastAPI()

    @app.get("/probe")
    def probe(ident: identity.Identity = Depends(identity.require_identity)):
        return {"tenant": ident.tenant_id, "user": ident.user_id,
                "role": ident.role, "source": ident.source}

    return TestClient(app)


FORGED = {
    "X-Auth-Tenant-ID": VICTIM, "X-Tenant-ID": VICTIM,
    "X-User-ID": VICTIM, "X-User-Role": "owner", "X-Group-IDs": "admins",
}


def test_require_identity_rejects_forged_headers(env):
    c = _probe_app()
    # Forged identity headers, no cookie, no gateway signature.
    assert c.get("/probe", headers=FORGED).status_code == 401
    # With a valid gateway signature but no session and no internal key.
    assert c.get("/probe", headers={**FORGED, "X-Gateway-Signature": SIG}).status_code == 401
    # A wrong internal key does not unlock the headers either.
    r = c.get("/probe", headers={**FORGED, "X-Gateway-Signature": SIG,
                                 "X-Internal-Service-Key": "nope"})
    assert r.status_code == 401
    # Wrong signature with an otherwise valid session.
    r = c.get("/probe", headers={"X-Gateway-Signature": "forged"},
              cookies={"dms_session": "good-cookie"})
    assert r.status_code == 401
    # An unset server-side gateway secret accepts nothing.
    settings.gateway_secret = ""
    r = c.get("/probe", headers={"X-Gateway-Signature": ""},
              cookies={"dms_session": "good-cookie"})
    assert r.status_code == 401


def test_real_routes_reject_forged_headers(env):
    """The actual routers — not just the dependency — refuse the forged
    request that used to read/rewrite another tenant's LLM config."""
    from app.api.routes import admin_router, router

    app = FastAPI()
    app.include_router(router)
    app.include_router(admin_router)
    c = TestClient(app)
    hdrs = {**FORGED, "X-Gateway-Signature": SIG}
    assert c.get("/api/v1/admin/tenant/llm-config", headers=hdrs).status_code == 401
    assert c.put("/api/v1/admin/tenant/llm-config", headers=hdrs,
                 json={"base_url": "http://evil"}).status_code == 401
    assert c.get("/api/v1/intelligence/qa/history", headers=hdrs).status_code == 401
    assert c.post("/api/v1/intelligence/ask", headers=hdrs,
                  json={"question": "q"}).status_code == 401


def test_require_identity_accepts_session(env):
    c = _probe_app()
    r = c.get("/probe", headers={**FORGED, "X-Gateway-Signature": SIG},
              cookies={"dms_session": "good-cookie"})
    assert r.status_code == 200, r.text
    # Identity comes from auth /me; the forged headers are ignored.
    assert r.json() == {"tenant": TENANT, "user": USER, "role": "member", "source": "session"}
    me = env["calls"][0]
    assert me.headers["X-Gateway-Signature"] == SIG
    assert "dms_session=good-cookie" in me.headers["cookie"]

    # Cached for 30 s under sha256(cookie) — the raw cookie is never a key.
    key = "intel:identity:" + hashlib.sha256(b"good-cookie").hexdigest()
    assert key in env["redis"].store
    assert env["redis"].ttls[key] == 30
    assert all("good-cookie" not in k for k in env["redis"].store)
    assert json.loads(env["redis"].store[key])["tenant_id"] == TENANT

    # Second request is served from the cache.
    r2 = c.get("/probe", headers={"X-Gateway-Signature": SIG},
               cookies={"dms_session": "good-cookie"})
    assert r2.status_code == 200
    assert len(env["calls"]) == 1

    # The previous (rotation-overlap) signature is accepted too.
    r3 = c.get("/probe", headers={"X-Gateway-Signature": PREV_SIG},
               cookies={"dms_session": "good-cookie"})
    assert r3.status_code == 200

    # A cookie auth rejects is a 401, not a fallback to headers.
    r4 = c.get("/probe", headers={**FORGED, "X-Gateway-Signature": SIG,
                                  "X-Internal-Service-Key": INTERNAL},
               cookies={"dms_session": "stale"})
    assert r4.status_code == 401


def test_internal_key_allows_headers(env):
    c = _probe_app()
    r = c.get("/probe", headers={
        "X-Gateway-Signature": SIG, "X-Internal-Service-Key": INTERNAL,
        "X-Auth-Tenant-ID": TENANT, "X-User-ID": USER, "X-User-Role": "admin",
    })
    assert r.status_code == 200, r.text
    assert r.json() == {"tenant": TENANT, "user": USER, "role": "admin", "source": "internal"}
    assert env["calls"] == []  # no auth round-trip for service callers

    # The internal key alone is not enough: the gateway signature still gates.
    r = c.get("/probe", headers={
        "X-Internal-Service-Key": INTERNAL, "X-Auth-Tenant-ID": TENANT,
    })
    assert r.status_code == 401
