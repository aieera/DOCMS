"""AES-256-GCM helpers for small per-tenant secrets stored in the
document DB (currently the LLM API key in ner_config). Wire-compatible
with services/document/internal/service/ner_service.go::encryptTenantSecret
and services/auth/internal/service/mfa.go::encryptMFASecret — same
key, same nonce-prepended layout, same base64.

Key source: VAULTDMS_LOCAL_KEK env (base64 of a 32-byte AES-256 key).
Empty / wrong-size key disables decryption — callers should treat that
as "no plaintext available" rather than a hard error so the LLM tier
silently no-ops in dev environments without the KEK plumbed through.
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
    raw = os.environ.get("VAULTDMS_LOCAL_KEK", "").strip()
    if not raw:
        return None
    try:
        kek = base64.b64decode(raw)
    except (ValueError, TypeError) as e:
        log.warning("secrets: VAULTDMS_LOCAL_KEK base64 decode failed: %s", e)
        return None
    if len(kek) != KEY_SIZE:
        log.warning("secrets: KEK is %d bytes, expected %d", len(kek), KEY_SIZE)
        return None
    return kek


def encrypt_tenant_secret(plaintext: str) -> Optional[str]:
    """Symmetric counterpart of decrypt_tenant_secret — produces a
    base64 string of `nonce || AES-256-GCM(plaintext)` using the
    deploy's VAULTDMS_LOCAL_KEK. Returns None when KEK is missing or
    malformed; callers MUST treat None as "refuse to write" (the
    endpoint should 503) since silently storing plaintext defeats
    the whole at-rest guarantee.

    Wire-compatible with services/document/internal/service/
    ner_service.go::encryptTenantSecret + auth/internal/service/
    mfa.go::encryptMFASecret.
    """
    if not plaintext:
        return None
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
    """Returns plaintext or None on any failure (missing/garbage KEK,
    bad ciphertext, auth-tag mismatch). Never raises — the LLM call
    site needs to keep going regardless."""
    if not encoded:
        return None
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
