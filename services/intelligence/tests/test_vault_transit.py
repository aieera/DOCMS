"""ADR 0081 — Vault Transit backend for tenant-secret envelope encryption.

The encrypt/decrypt code paths must:
  - Route by ciphertext prefix (`vault:` → Vault, else → local AES-GCM)
  - Be backwards-compatible: existing local-AES-GCM ciphertexts still
    decrypt after a Vault rollout
  - Never silently fall back to plaintext on any failure path
  - Time out gracefully so a Vault hiccup can't hang the LLM hot path

httpx is mocked so this runs without a real Vault container.
"""
from __future__ import annotations

import base64
from unittest import mock

import pytest

from app import secrets, vault_transit


@pytest.fixture
def vault_env(monkeypatch):
    """Pretend Vault is fully configured."""
    monkeypatch.setenv("VAULT_ADDR", "http://vault.test:8200")
    monkeypatch.setenv("VAULT_TOKEN", "s.deadbeef")
    monkeypatch.setenv("VAULT_TRANSIT_KEY", "vaultdms-tenant-secrets")


@pytest.fixture
def local_kek_env(monkeypatch):
    """A 32-byte zero KEK encoded in base64 — only used to make the
    in-process decrypt path runnable for round-trip tests."""
    monkeypatch.setenv("SEDOC_LOCAL_KEK", base64.b64encode(b"\0" * 32).decode())


# ---- vault_transit.is_enabled --------------------------------------

def test_vault_disabled_when_any_var_missing(monkeypatch):
    monkeypatch.delenv("VAULT_ADDR", raising=False)
    monkeypatch.delenv("VAULT_TOKEN", raising=False)
    monkeypatch.delenv("VAULT_TRANSIT_KEY", raising=False)
    assert vault_transit.is_enabled() is False

    monkeypatch.setenv("VAULT_ADDR", "http://vault.test:8200")
    assert vault_transit.is_enabled() is False  # token + key still missing

    monkeypatch.setenv("VAULT_TOKEN", "s.deadbeef")
    assert vault_transit.is_enabled() is False  # key still missing


def test_vault_enabled_when_all_vars_set(vault_env):
    assert vault_transit.is_enabled() is True


# ---- vault_transit.encrypt -----------------------------------------

class _FakeResp:
    def __init__(self, json_body, status=200):
        self._body = json_body
        self.status_code = status
        self.text = str(json_body)

    def raise_for_status(self):
        if self.status_code >= 400:
            import httpx
            req = httpx.Request("POST", "http://vault.test")
            raise httpx.HTTPStatusError("err", request=req, response=mock.MagicMock(status_code=self.status_code, text=self.text))

    def json(self):
        return self._body


def test_vault_encrypt_round_trip(vault_env):
    """Successful encrypt returns the `vault:v1:...` string Vault sent."""
    expected_ct = "vault:v1:abcdef==="
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    fake_client.post.return_value = _FakeResp(
        {"data": {"ciphertext": expected_ct}}
    )
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        got = vault_transit.encrypt("sk-anthropic-secret")
    assert got == expected_ct
    # Verify the request carried base64(plaintext) — the load-bearing
    # contract that Vault Transit expects.
    posted = fake_client.post.call_args
    body = posted.kwargs["json"]
    assert base64.b64decode(body["plaintext"]).decode() == "sk-anthropic-secret"


def test_vault_encrypt_returns_none_on_http_error(vault_env):
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    fake_client.post.return_value = _FakeResp({}, status=403)
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        assert vault_transit.encrypt("anything") is None


def test_vault_encrypt_returns_none_on_unexpected_response_shape(vault_env):
    """Vault changed its API or returned an empty body — fail closed
    rather than store something we can't decrypt later."""
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    fake_client.post.return_value = _FakeResp({"data": {}})
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        assert vault_transit.encrypt("anything") is None


def test_vault_encrypt_disabled_returns_none(monkeypatch):
    monkeypatch.delenv("VAULT_ADDR", raising=False)
    assert vault_transit.encrypt("anything") is None


# ---- vault_transit.decrypt -----------------------------------------

def test_vault_decrypt_round_trip(vault_env):
    plaintext = "sk-real-secret-12345"
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    fake_client.post.return_value = _FakeResp({
        "data": {"plaintext": base64.b64encode(plaintext.encode()).decode()}
    })
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        got = vault_transit.decrypt("vault:v1:abcdef===")
    assert got == plaintext


def test_vault_decrypt_rejects_blobs_without_prefix(vault_env):
    """Defense in depth — if a caller passes a non-Vault ciphertext to
    the Vault decrypt helper, it must refuse rather than send
    arbitrary garbage to Vault and get a confusing 4xx back."""
    assert vault_transit.decrypt("not-prefixed-blob") is None


# ---- secrets.encrypt_tenant_secret routing -------------------------

def test_encrypt_prefers_vault_when_enabled(vault_env):
    """When Vault is configured, encrypt routes through Transit and
    returns the `vault:` prefix."""
    expected_ct = "vault:v1:zzz"
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    fake_client.post.return_value = _FakeResp(
        {"data": {"ciphertext": expected_ct}}
    )
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        ct = secrets.encrypt_tenant_secret("sk-x")
    assert ct == expected_ct
    assert ct.startswith("vault:")


def test_encrypt_falls_back_to_local_when_vault_unreachable(vault_env, local_kek_env):
    """Vault timeout / network failure with a local KEK present — fall
    through to the in-process AES-GCM path so admin writes still
    work during a Vault outage. The ciphertext loses the `vault:`
    prefix; decrypt routes correctly afterward."""
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    import httpx
    fake_client.post.side_effect = httpx.RequestError("conn refused")
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        ct = secrets.encrypt_tenant_secret("sk-x")
    assert ct is not None
    assert not ct.startswith("vault:")  # local AES-GCM blob


def test_encrypt_refuses_when_vault_unreachable_and_no_local_kek(vault_env, monkeypatch):
    """Vault outage AND no local KEK — refuse to write rather than
    silently storing plaintext. The endpoint will 503 the admin."""
    monkeypatch.delenv("SEDOC_LOCAL_KEK", raising=False)
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    import httpx
    fake_client.post.side_effect = httpx.RequestError("conn refused")
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        ct = secrets.encrypt_tenant_secret("sk-x")
    assert ct is None


def test_encrypt_local_path_when_vault_disabled(local_kek_env, monkeypatch):
    """Dev / on-prem mode — Vault env vars absent, local KEK present."""
    monkeypatch.delenv("VAULT_ADDR", raising=False)
    ct = secrets.encrypt_tenant_secret("sk-x")
    assert ct is not None
    assert not ct.startswith("vault:")


# ---- secrets.decrypt_tenant_secret routing -------------------------

def test_decrypt_routes_vault_prefix_to_transit(vault_env):
    plaintext = "sk-real-secret"
    fake_client = mock.MagicMock()
    fake_client.__enter__.return_value = fake_client
    fake_client.__exit__.return_value = False
    fake_client.post.return_value = _FakeResp({
        "data": {"plaintext": base64.b64encode(plaintext.encode()).decode()}
    })
    with mock.patch("app.vault_transit.httpx.Client", return_value=fake_client):
        got = secrets.decrypt_tenant_secret("vault:v1:abcdef")
    assert got == plaintext


def test_decrypt_legacy_blob_still_works_after_vault_rollout(vault_env, local_kek_env):
    """The migration story — a row written with the LOCAL KEK before
    Vault rolled out must keep decrypting via the in-process path
    even when Vault is now configured. Without this, every existing
    NER api key would break the day Vault is enabled."""
    # Force the local path for encrypt by temporarily disabling Vault,
    # then re-enable and decrypt.
    import os as _os
    saved = {k: _os.environ.pop(k, None) for k in ("VAULT_ADDR", "VAULT_TOKEN", "VAULT_TRANSIT_KEY")}
    try:
        ct = secrets.encrypt_tenant_secret("legacy-key-from-before-vault")
    finally:
        for k, v in saved.items():
            if v is not None:
                _os.environ[k] = v
    assert ct is not None and not ct.startswith("vault:")

    # Vault is now enabled, but decrypt should still route this row
    # to the local path because it lacks the prefix.
    pt = secrets.decrypt_tenant_secret(ct)
    assert pt == "legacy-key-from-before-vault"


def test_decrypt_returns_none_on_missing_prefix_and_no_local_kek(monkeypatch, vault_env):
    """A non-vault: blob with no local KEK — the row was written with
    a KEK we no longer have. Fail closed; LLM tier loses the key
    rather than crashing."""
    monkeypatch.delenv("SEDOC_LOCAL_KEK", raising=False)
    assert secrets.decrypt_tenant_secret("not-a-vault-blob") is None
