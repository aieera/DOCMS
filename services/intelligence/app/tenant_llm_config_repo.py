"""ADR 0081 — persistence for the per-tenant LLM routing config.

The intelligence service was previously loading per-tenant LLM
config out of a Redis hash with no admin write path. This module
replaces that with a DB-backed read/write through tenant_llm_config,
keeping a 30s Redis cache in front for hot-path latency.

The api_key column never round-trips through the API in plaintext —
encrypt_tenant_secret runs at write time, decrypt_tenant_secret runs
inside the gateway only. GET endpoints surface key_set + key_set_at,
not the ciphertext or plaintext.
"""
from __future__ import annotations

import json
import logging
import time
from typing import Any, Optional

from app.db.pool import get_pool
from app.secrets import decrypt_tenant_secret, encrypt_tenant_secret

log = logging.getLogger(__name__)

# Cache key + TTL. 30s keeps reads off Postgres on every completion
# without making admin config changes feel sticky in the UI.
_CACHE_KEY = "llm_config:{tenant_id}"
_CACHE_TTL_SECONDS = 30


def _safe_view(row: dict[str, Any]) -> dict[str, Any]:
    """Strip the ciphertext from a row before any caller-facing
    return. Even internal callers should use decrypt_api_key() rather
    than reach into the ciphertext directly."""
    out = dict(row)
    out.pop("api_key_encrypted", None)
    return out


async def get_full_config(tenant_id: str) -> dict[str, Any]:
    """Returns provider/model/etc. + the boolean `key_set` + `key_set_at`.
    Does NOT return the api_key in any form. Falls back to the schema
    defaults when no row exists yet so the admin UI renders a
    consistent form."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT provider, model, fallback_model,
                       COALESCE(api_key_encrypted, '') AS api_key_encrypted,
                       api_key_set_at, base_url,
                       rate_limit_rpm, daily_budget_usd, air_gapped,
                       updated_at
                  FROM tenant_llm_config
                 WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return {
            "provider": "anthropic",
            "model": "",
            "fallback_model": "",
            "key_set": False,
            "key_set_at": None,
            "base_url": None,
            "rate_limit_rpm": 60,
            "daily_budget_usd": 0.0,
            "air_gapped": False,
            "updated_at": None,
        }
    return {
        "provider": row["provider"],
        "model": row["model"],
        "fallback_model": row["fallback_model"],
        "key_set": bool(row["api_key_encrypted"]),
        "key_set_at": row["api_key_set_at"].isoformat() if row["api_key_set_at"] else None,
        "base_url": row["base_url"],
        "rate_limit_rpm": int(row["rate_limit_rpm"]),
        "daily_budget_usd": float(row["daily_budget_usd"]),
        "air_gapped": bool(row["air_gapped"]),
        "updated_at": row["updated_at"].isoformat() if row["updated_at"] else None,
    }


async def upsert_config(
    *,
    tenant_id: str,
    provider: Optional[str] = None,
    model: Optional[str] = None,
    fallback_model: Optional[str] = None,
    api_key_plaintext: Optional[str] = None,
    base_url: Optional[str] = None,
    rate_limit_rpm: Optional[int] = None,
    daily_budget_usd: Optional[float] = None,
    air_gapped: Optional[bool] = None,
) -> dict[str, Any]:
    """Patch any subset of fields. api_key_plaintext is encrypted via
    AES-256-GCM at write time; passing None leaves the existing key
    untouched (so admins can change provider without re-pasting).
    Empty string explicitly clears the stored key.

    Raises RuntimeError when api_key is supplied but the KEK isn't
    configured — silently storing plaintext would defeat the at-rest
    guarantee, so we'd rather 5xx and let the admin notice."""
    api_key_encrypted: Optional[str] = None
    api_key_set_at_clear = False
    if api_key_plaintext is None:
        api_key_encrypted_param = None  # COALESCE keeps the existing
    elif api_key_plaintext == "":
        api_key_encrypted_param = ""    # explicit clear
        api_key_set_at_clear = True
    else:
        encrypted = encrypt_tenant_secret(api_key_plaintext)
        if encrypted is None:
            raise RuntimeError("SEDOC_LOCAL_KEK not configured; refusing to store plaintext")
        api_key_encrypted_param = encrypted

    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO tenant_llm_config
                    (tenant_id, provider, model, fallback_model,
                     api_key_encrypted, api_key_set_at,
                     base_url, rate_limit_rpm, daily_budget_usd, air_gapped,
                     updated_at)
                VALUES ($1,
                        COALESCE($2, 'anthropic'),
                        COALESCE($3, ''),
                        COALESCE($4, ''),
                        NULLIF($5, ''),
                        CASE
                            WHEN $5 IS NULL THEN NULL
                            WHEN $5 = ''     THEN NULL
                            ELSE now()
                        END,
                        $6,
                        COALESCE($7, 60),
                        COALESCE($8, 0),
                        COALESCE($9, FALSE),
                        now())
                ON CONFLICT (tenant_id) DO UPDATE
                   SET provider          = COALESCE($2, tenant_llm_config.provider),
                       model             = COALESCE($3, tenant_llm_config.model),
                       fallback_model    = COALESCE($4, tenant_llm_config.fallback_model),
                       api_key_encrypted = CASE
                           WHEN $5 IS NULL THEN tenant_llm_config.api_key_encrypted
                           WHEN $5 = ''    THEN NULL
                           ELSE $5
                       END,
                       api_key_set_at    = CASE
                           WHEN $5 IS NULL THEN tenant_llm_config.api_key_set_at
                           WHEN $5 = ''    THEN NULL
                           ELSE now()
                       END,
                       base_url          = COALESCE($6, tenant_llm_config.base_url),
                       rate_limit_rpm    = COALESCE($7, tenant_llm_config.rate_limit_rpm),
                       daily_budget_usd  = COALESCE($8, tenant_llm_config.daily_budget_usd),
                       air_gapped        = COALESCE($9, tenant_llm_config.air_gapped),
                       updated_at        = now()
                """,
                tenant_id, provider, model, fallback_model,
                api_key_encrypted_param,
                base_url, rate_limit_rpm, daily_budget_usd, air_gapped,
            )

    # Bust the cache so the next /completions call reads fresh.
    _bust_cache(tenant_id)
    return await get_full_config(tenant_id)


def _bust_cache(tenant_id: str) -> None:
    try:
        from app.llm_gateway import _get_redis
        _get_redis().delete(_CACHE_KEY.format(tenant_id=tenant_id))
    except Exception as e:
        log.warning("config cache bust failed: %s", e)


async def load_for_gateway(tenant_id: str) -> dict[str, Any]:
    """Used by the gateway on the completion hot path. Returns the
    decrypted view: provider/model/fallback_model/api_key_plaintext/
    base_url/rate_limit_rpm/daily_budget_usd/air_gapped. Reads through
    the 30 s Redis cache; on miss, hits the DB."""
    try:
        from app.llm_gateway import _get_redis
        r = _get_redis()
        raw = r.get(_CACHE_KEY.format(tenant_id=tenant_id))
        if raw:
            return json.loads(raw)
    except Exception:
        r = None  # noqa: F841 — keep going on cache failure

    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT provider, model, fallback_model,
                       COALESCE(api_key_encrypted, '') AS api_key_encrypted,
                       base_url, rate_limit_rpm, daily_budget_usd, air_gapped
                  FROM tenant_llm_config
                 WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        view = {
            "provider": "anthropic",
            "model": "",
            "fallback_model": "",
            "api_key": None,
            "base_url": None,
            "rate_limit_rpm": 60,
            "daily_budget_usd": 0.0,
            "air_gapped": False,
        }
    else:
        api_key = decrypt_tenant_secret(row["api_key_encrypted"]) if row["api_key_encrypted"] else None
        view = {
            "provider": row["provider"],
            "model": row["model"],
            "fallback_model": row["fallback_model"],
            "api_key": api_key,
            "base_url": row["base_url"],
            "rate_limit_rpm": int(row["rate_limit_rpm"]),
            "daily_budget_usd": float(row["daily_budget_usd"]),
            "air_gapped": bool(row["air_gapped"]),
        }
    # Cache the gateway view (with plaintext key — only ever stored
    # in our own Redis, which the same code wrote to). The 30 s TTL
    # bounds blast radius; admin writes also call _bust_cache.
    try:
        from app.llm_gateway import _get_redis
        _get_redis().setex(
            _CACHE_KEY.format(tenant_id=tenant_id),
            _CACHE_TTL_SECONDS,
            json.dumps(view),
        )
    except Exception as e:
        log.warning("config cache write failed: %s", e)
    return view
