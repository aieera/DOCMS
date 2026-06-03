"""Tenant-secret envelope encryption with two backends.

ADR 0081 — for the LLM API key in tenant_llm_config and the legacy
NER api key in ner_config, we store ciphertext in the DB column
and decrypt only at LLM-call time. Two backends, picked at runtime:

  Vault Transit (VAULT_ADDR + VAULT_TOKEN + VAULT_TRANSIT_KEY set):
      KEK material lives entirely inside Vault. Every encrypt /
      decrypt round-trips to /v1/transit. Ciphertext starts with
      `vault:v1:...` so decrypt routes back to Vault by prefix.
      Production-cloud default.

  Local AES-256-GCM (SEDOC_LOCAL_KEK set, Vault not):
      In-process encryption with a 32-byte AES key from env.
      Wire-compatible with services/document's ner_service.go and
      auth's mfa.go — same nonce-prepended layout, same base64.
      Dev / on-prem / air-gapped default.

Decrypt detects which scheme produced a row by prefix and dispatches
accordingly. That's the migration story: existing rows written with
the local KEK keep decrypting after a Vault rollout; new rows
written under Vault carry the `vault:` prefix and decrypt against
Vault. No big-bang re-encryption needed.

Both backends return None on any failure (missing/garbage KEK, bad
ciphertext, network hiccup, auth-tag mismatch). Never raises — the
LLM call site needs to keep going without plaintext.
"""
from __future__ import annotations

import base64
import logging
import os
from typing import Optional

log = logging.getLogger(__name__)

NONCE_SIZE = 12  # GCM nonce
KEY_SIZE = 32    # AES-256


def _load_kek() -> Optional[bytes]:
    raw = os.environ.get("SEDOC_LOCAL_KEK", "").strip()
    if not raw:
        return None
    try:
        kek = base64.b64decode(raw)
    except (ValueError, TypeError) as e:
        log.warning("secrets: SEDOC_LOCAL_KEK base64 decode failed: %s", e)
        return None
    if len(kek) != KEY_SIZE:
        log.warning("secrets: KEK is %d bytes, expected %d", len(kek), KEY_SIZE)
        return None
    return kek


def encrypt_tenant_secret(plaintext: str) -> Optional[str]:
    """Encrypt and return a stable ciphertext string.

    Backend pick:
      - Vault Transit when VAULT_ADDR/_TOKEN/_TRANSIT_KEY are set;
        result is `vault:v1:...` (Vault's native format).
      - Local AES-256-GCM otherwise; result is base64 of
        `nonce || AES-256-GCM(plaintext)`.

    Returns None when neither backend can satisfy the call — the
    upsert endpoint MUST 503 in that case, since silently dropping
    back to plaintext defeats the at-rest guarantee.
    """
    if not plaintext:
        return None
    # Vault preferred when fully configured. A partial config falls
    # through to local — half-configured Vault is a deploy bug, not
    # a "use both" signal.
    from app import vault_transit
    if vault_transit.is_enabled():
        ct = vault_transit.encrypt(plaintext)
        if ct:
            return ct
        # Fall through if Vault is configured but unreachable — only
        # if local KEK is ALSO present. This keeps a Vault outage
        # from bricking admin writes when there's a fallback path.
        if _load_kek() is None:
            return None
    return _encrypt_local(plaintext)


def _encrypt_local(plaintext: str) -> Optional[str]:
    """In-process AES-256-GCM. Wire-compatible with
    services/document/internal/service/ner_service.go::encryptTenantSecret
    + auth/internal/service/mfa.go::encryptMFASecret."""
    kek = _load_kek()
    if kek is None:
        return None
    try:
        from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    except ImportError:
        log.warning("secrets: cryptography not installed; cannot encrypt")
        return None
    import os as _os
    nonce = _os.urandom(NONCE_SIZE)
    try:
        ct = AESGCM(kek).encrypt(nonce, plaintext.encode("utf-8"), None)
    except Exception as e:  # noqa: BLE001
        log.warning("secrets: AES-GCM encrypt failed: %s", e)
        return None
    return base64.b64encode(nonce + ct).decode("ascii")


def decrypt_tenant_secret(encoded: str) -> Optional[str]:
    """Returns plaintext or None on any failure. Routes by ciphertext
    prefix: `vault:` → Vault Transit; anything else → local AES-GCM.

    The prefix-based dispatch is what makes the local→Vault migration
    incremental — existing rows written with the local KEK keep
    decrypting via the in-process path, while new rows written under
    Vault decrypt through the Transit API. Never raises — the LLM
    call site needs to keep going regardless."""
    if not encoded:
        return None
    if encoded.startswith("vault:"):
        from app import vault_transit
        return vault_transit.decrypt(encoded)
    return _decrypt_local(encoded)


def _decrypt_local(encoded: str) -> Optional[str]:
    """In-process AES-GCM decrypt for the legacy ciphertext layout."""
    kek = _load_kek()
    if kek is None:
        return None
    try:
        from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    except ImportError:
        log.warning("secrets: cryptography not installed; LLM key cannot be decrypted")
        return None
    try:
        buf = base64.b64decode(encoded)
    except (ValueError, TypeError) as e:
        log.warning("secrets: ciphertext base64 decode failed: %s", e)
        return None
    if len(buf) < NONCE_SIZE + 1:
        log.warning("secrets: ciphertext too short")
        return None
    nonce, ct = buf[:NONCE_SIZE], buf[NONCE_SIZE:]
    try:
        return AESGCM(kek).decrypt(nonce, ct, None).decode("utf-8")
    except Exception as e:  # noqa: BLE001 — InvalidTag, etc.
        log.warning("secrets: AES-GCM decrypt failed: %s", e)
        return None
