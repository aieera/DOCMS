"""DEC-4 — request identity for the intelligence REST API.

Every user-facing route depends on `require_identity`. A request is
accepted only when:

1. `X-Gateway-Signature` equals SEDOC_GATEWAY_SECRET (or, during a
   rotation, SEDOC_GATEWAY_SECRET_PREV), compared in constant time — the
   same check pkg/middleware/gatewaysig.go runs in every Go backend; and
2. the caller's identity resolves from one of:
   * the `dms_session` cookie, via GET {auth_url}/api/v1/auth/me
     (cookie and signature forwarded; the result is cached in Redis for
     30 s under sha256(cookie), never under the raw cookie), or
   * the X-Auth-Tenant-ID / X-Tenant-ID / X-User-* / X-Group-IDs headers,
     but ONLY when `X-Internal-Service-Key` equals SEDOC_INTERNAL_API_KEY
     (constant time) and no session cookie is present (DEC-3).

Identity headers on any other request are ignored, so a browser (or
anything that reaches the published port or the host-mode Vite proxy)
cannot assert a tenant, user or role.
"""
from __future__ import annotations

import hashlib
import hmac
import json
import logging
from dataclasses import asdict, dataclass, field
from typing import Optional

import httpx
from fastapi import HTTPException, Request

from app.config import settings

log = logging.getLogger(__name__)

GATEWAY_SIGNATURE_HEADER = "X-Gateway-Signature"
INTERNAL_KEY_HEADER = "X-Internal-Service-Key"
SESSION_COOKIE = "dms_session"
_CACHE_PREFIX = "intel:identity:"


@dataclass(frozen=True)
class Identity:
    tenant_id: str
    user_id: str = ""
    role: str = ""
    groups: tuple[str, ...] = field(default_factory=tuple)
    # "session" (dms_session cookie) or "internal" (service key + headers).
    source: str = "session"


def _ct_equal(got: str, want: str) -> bool:
    if not got or not want:
        return False
    return hmac.compare_digest(got.encode("utf-8"), want.encode("utf-8"))


def gateway_signature_ok(value: Optional[str]) -> bool:
    """Constant-time match against the active or the previous secret.
    An unset active secret accepts nothing (fail closed)."""
    if not settings.gateway_secret:
        return False
    got = value or ""
    ok = _ct_equal(got, settings.gateway_secret)
    prev = settings.gateway_secret_prev
    if prev and prev != settings.gateway_secret:
        # Evaluate both so the timing does not reveal which one matched.
        ok = _ct_equal(got, prev) or ok
    return ok


def internal_key_ok(value: Optional[str]) -> bool:
    return _ct_equal(value or "", settings.internal_api_key)


# ---- collaborators (patched in tests) ------------------------------------

_redis_client = None


def _redis():
    global _redis_client
    if _redis_client is None:
        import redis
        _redis_client = redis.Redis.from_url(settings.redis_cache_url)
    return _redis_client


def _http_client() -> httpx.Client:
    return httpx.Client(timeout=5.0)


def _cache_key(cookie: str) -> str:
    return _CACHE_PREFIX + hashlib.sha256(cookie.encode("utf-8")).hexdigest()


def _cache_get(key: str) -> Optional[Identity]:
    try:
        raw = _redis().get(key)
    except Exception:  # noqa: BLE001 — cache is an optimisation only
        log.warning("identity cache read failed", exc_info=True)
        return None
    if not raw:
        return None
    try:
        d = json.loads(raw)
        return Identity(
            tenant_id=d["tenant_id"], user_id=d.get("user_id", ""),
            role=d.get("role", ""), groups=tuple(d.get("groups") or ()),
            source="session",
        )
    except (TypeError, ValueError, KeyError):
        return None


def _cache_put(key: str, ident: Identity) -> None:
    try:
        payload = asdict(ident)
        payload["groups"] = list(ident.groups)
        _redis().setex(key, settings.identity_cache_ttl_seconds, json.dumps(payload))
    except Exception:  # noqa: BLE001
        log.warning("identity cache write failed", exc_info=True)


def _identity_from_session(cookie: str, signature: str) -> Identity:
    key = _cache_key(cookie)
    cached = _cache_get(key)
    if cached is not None:
        return cached

    url = settings.auth_url.rstrip("/") + "/api/v1/auth/me"
    try:
        with _http_client() as client:
            resp = client.get(url, headers={
                "Cookie": f"{SESSION_COOKIE}={cookie}",
                GATEWAY_SIGNATURE_HEADER: signature,
            })
    except httpx.HTTPError:
        log.warning("auth /me unreachable", exc_info=True)
        raise HTTPException(503, "identity service unavailable")
    if resp.status_code in (401, 403):
        raise HTTPException(401, "authentication required")
    if resp.status_code != 200:
        log.warning("auth /me returned %s", resp.status_code)
        raise HTTPException(503, "identity service unavailable")
    try:
        me = resp.json()
    except ValueError:
        raise HTTPException(503, "identity service unavailable")
    tenant = str(me.get("tenant_id") or "")
    user = str(me.get("id") or me.get("user_id") or "")
    status = str(me.get("status") or "active")
    if not tenant or not user or status != "active":
        raise HTTPException(401, "authentication required")
    groups = me.get("groups") or me.get("group_ids") or []
    ident = Identity(
        tenant_id=tenant, user_id=user, role=str(me.get("role") or ""),
        groups=tuple(str(g) for g in groups if g), source="session",
    )
    _cache_put(key, ident)
    return ident


def _identity_from_headers(request: Request) -> Identity:
    h = request.headers
    tenant = h.get("X-Auth-Tenant-ID") or h.get("X-Tenant-ID") or ""
    if not tenant:
        raise HTTPException(400, "X-Auth-Tenant-ID (or X-Tenant-ID) required")
    groups = tuple(g.strip() for g in (h.get("X-Group-IDs") or "").split(",") if g.strip())
    return Identity(
        tenant_id=tenant, user_id=h.get("X-User-ID") or "",
        role=h.get("X-User-Role") or "", groups=groups, source="internal",
    )


def require_identity(request: Request) -> Identity:
    """FastAPI dependency — see the module docstring for the rules."""
    signature = request.headers.get(GATEWAY_SIGNATURE_HEADER, "")
    if not gateway_signature_ok(signature):
        raise HTTPException(401, "invalid gateway signature")
    cookie = request.cookies.get(SESSION_COOKIE)
    if cookie:
        return _identity_from_session(cookie, signature)
    if internal_key_ok(request.headers.get(INTERNAL_KEY_HEADER)):
        return _identity_from_headers(request)
    raise HTTPException(401, "authentication required")


def require_user(ident: Identity) -> str:
    if not ident.user_id:
        raise HTTPException(400, "X-User-ID required")
    return ident.user_id


def require_admin(ident: Identity) -> None:
    if ident.role not in {"owner", "admin"}:
        raise HTTPException(403, "owner|admin required")
