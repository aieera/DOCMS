"""AES-256-GCM tenant-secret decrypt — wire-format compat with
services/document/internal/service/ner_service.go::encryptTenantSecret
(base64(nonce || ciphertext+tag) under SEDOC_LOCAL_KEK)."""
from __future__ import annotations

import base64
import os

import pytest


def _set_kek(monkeypatch, key_bytes: bytes) -> None:
    monkeypatch.setenv("SEDOC_LOCAL_KEK", base64.b64encode(key_bytes).decode())


def _encrypt_like_go(plain: str, kek: bytes) -> str:
    """Mirror the Go-side wire format so we test the decoder against
    something we know matches what the Go encryptor would emit."""
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    nonce = os.urandom(12)
    ct = AESGCM(kek).encrypt(nonce, plain.encode(), None)
    return base64.b64encode(nonce + ct).decode()


def test_decrypt_roundtrip(monkeypatch):
    kek = b"\x42" * 32
    _set_kek(monkeypatch, kek)
    from app.secrets import decrypt_tenant_secret
    enc = _encrypt_like_go("sk-ant-api03-real-looking-key", kek)
    assert decrypt_tenant_secret(enc) == "sk-ant-api03-real-looking-key"


def test_decrypt_missing_kek_returns_none(monkeypatch):
    monkeypatch.delenv("SEDOC_LOCAL_KEK", raising=False)
    from app.secrets import decrypt_tenant_secret
    # Even with valid-looking input, no KEK = no decrypt.
    assert decrypt_tenant_secret(base64.b64encode(b"x" * 50).decode()) is None


def test_decrypt_garbage_kek_returns_none(monkeypatch):
    monkeypatch.setenv("SEDOC_LOCAL_KEK", "not-base64!!!")
    from app.secrets import decrypt_tenant_secret
    assert decrypt_tenant_secret(_encrypt_like_go("x", b"\x00" * 32)) is None


def test_decrypt_wrong_size_kek_returns_none(monkeypatch):
    monkeypatch.setenv("SEDOC_LOCAL_KEK", base64.b64encode(b"\x00" * 16).decode())
    from app.secrets import decrypt_tenant_secret
    assert decrypt_tenant_secret(_encrypt_like_go("x", b"\x00" * 32)) is None


def test_decrypt_empty_input_returns_none(monkeypatch):
    _set_kek(monkeypatch, b"\x42" * 32)
    from app.secrets import decrypt_tenant_secret
    assert decrypt_tenant_secret("") is None


def test_decrypt_short_ciphertext_returns_none(monkeypatch):
    _set_kek(monkeypatch, b"\x42" * 32)
    from app.secrets import decrypt_tenant_secret
    # Just a nonce (12 bytes) with no ciphertext at all.
    assert decrypt_tenant_secret(base64.b64encode(b"\x00" * 12).decode()) is None


def test_decrypt_wrong_kek_fails_silently(monkeypatch):
    enc = _encrypt_like_go("secret", b"\x42" * 32)
    # Now switch to a different KEK and try to decrypt.
    _set_kek(monkeypatch, b"\xAA" * 32)
    from app.secrets import decrypt_tenant_secret
    assert decrypt_tenant_secret(enc) is None


def test_unicode_payload(monkeypatch):
    kek = b"\x99" * 32
    _set_kek(monkeypatch, kek)
    from app.secrets import decrypt_tenant_secret
    enc = _encrypt_like_go("ключ-🔑-clé", kek)
    assert decrypt_tenant_secret(enc) == "ключ-🔑-clé"
