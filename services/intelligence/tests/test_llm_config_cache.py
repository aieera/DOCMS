"""M-22 — the tenant LLM config cache never holds a plaintext API key.

load_for_gateway cached the DECRYPTED tenant key in the shared Redis
(unauthenticated and host-published in compose). The cache now holds
only non-secret config plus the ciphertext; the key is decrypted per
call.
"""
from __future__ import annotations

import asyncio
import json
from unittest import mock

from app import tenant_llm_config_repo as repo

PLAINTEXT = "sk-live-tenant-key-123"
CIPHERTEXT = "vault:v1:opaque-ciphertext"


class _FakeRedis:
    def __init__(self):
        self.store: dict[str, bytes] = {}

    def get(self, k):
        return self.store.get(k)

    def setex(self, k, ttl, v):
        self.store[k] = v.encode() if isinstance(v, str) else v

    def delete(self, k):
        self.store.pop(k, None)


class _Conn:
    def __init__(self, counter):
        self.counter = counter

    async def execute(self, *a):
        return None

    async def fetchrow(self, *a):
        self.counter.append(1)
        return {
            "provider": "openai", "model": "gpt-4o", "fallback_model": "",
            "api_key_encrypted": CIPHERTEXT, "base_url": None,
            "rate_limit_rpm": 60, "daily_budget_usd": 5.0, "air_gapped": False,
        }

    def transaction(self):
        return _Ctx(self)


class _Ctx:
    def __init__(self, v):
        self.v = v

    async def __aenter__(self):
        return self.v

    async def __aexit__(self, *a):
        return False


class _Pool:
    def __init__(self, counter):
        self.counter = counter

    def acquire(self):
        return _Ctx(_Conn(self.counter))


def test_llm_config_cache_has_no_key():
    fake = _FakeRedis()
    db_reads: list[int] = []

    async def _get_pool():
        return _Pool(db_reads)

    decrypt = mock.Mock(return_value=PLAINTEXT)
    with mock.patch("app.llm_gateway._get_redis", return_value=fake), \
         mock.patch.object(repo, "get_pool", new=_get_pool), \
         mock.patch.object(repo, "decrypt_tenant_secret", new=decrypt):
        first = asyncio.run(repo.load_for_gateway("t1"))
        second = asyncio.run(repo.load_for_gateway("t1"))

    # Callers still get a usable key on both the miss and the hit path.
    assert first["api_key"] == PLAINTEXT
    assert second["api_key"] == PLAINTEXT
    assert second["model"] == "gpt-4o" and second["daily_budget_usd"] == 5.0
    assert len(db_reads) == 1               # the second call was a cache hit
    assert decrypt.call_count == 2          # decrypted per call, not cached

    raw = fake.store["llm_config:t1"].decode()
    assert PLAINTEXT not in raw
    cached = json.loads(raw)
    assert "api_key" not in cached
    assert cached.get("api_key_encrypted") == CIPHERTEXT


def test_legacy_plaintext_cache_entry_is_not_trusted():
    """An entry written by the old code (with a plaintext api_key) is
    treated as a miss and overwritten."""
    fake = _FakeRedis()
    fake.store["llm_config:t1"] = json.dumps({
        "provider": "openai", "model": "old", "api_key": "sk-stale-plaintext",
    }).encode()
    db_reads: list[int] = []

    async def _get_pool():
        return _Pool(db_reads)

    with mock.patch("app.llm_gateway._get_redis", return_value=fake), \
         mock.patch.object(repo, "get_pool", new=_get_pool), \
         mock.patch.object(repo, "decrypt_tenant_secret", return_value=PLAINTEXT):
        view = asyncio.run(repo.load_for_gateway("t1"))
    assert view["api_key"] == PLAINTEXT and view["model"] == "gpt-4o"
    assert len(db_reads) == 1
    assert "sk-stale-plaintext" not in fake.store["llm_config:t1"].decode()
