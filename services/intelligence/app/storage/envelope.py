"""Python port of pkg/crypto/envelope.go (local KEK path only).

Mirrors the Go implementation exactly so a blob written by the Go
side decrypts byte-for-byte over here, and a blob written here
(encrypt_blob — the redaction output, M-24) decrypts in the storage
service. Used by the OCR worker and the redaction tasks.

Format invariants we replicate:
  * DEK is 32 bytes (AES-256)
  * Nonce is 12 bytes
  * wrapped_dek layout: nonce(12) || gcm_ciphertext(48) = 60 bytes total
    (32-byte DEK + 16-byte GCM tag, AES-GCM with KEK)
  * KEK derivation: HKDF-SHA256(
        ikm  = base64-decoded SEDOC_LOCAL_KEK,
        salt = b"vaultdms/kek/" + kek_id.encode(),
        info = b"vaultdms/kek/v1",
        length=32,
    )
  * Body encrypted under DEK with AES-GCM using `dek_nonce` from content_blobs.

Vault transit path NOT implemented — local KEK is dev/single-tenant only.
Prod with Vault should call services/document's decrypt-stream endpoint
instead of unwrapping DEKs in the worker.
"""
from __future__ import annotations

import base64
import os

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

DEK_SIZE = 32
NONCE_SIZE = 12
TAG_SIZE = 16
WRAPPED_DEK_SIZE = NONCE_SIZE + DEK_SIZE + TAG_SIZE  # 60 bytes


class EnvelopeError(Exception):
    """Raised when decrypt fails for any reason — corrupt blob, wrong KEK,
    missing master key, malformed wrapped DEK. The OCR worker catches
    this and surfaces it as TerminalOCRError(reason='decrypt_error')."""


def kek_configured() -> bool:
    """True when SEDOC_LOCAL_KEK is set (validity is checked on use)."""
    return bool(os.environ.get("SEDOC_LOCAL_KEK", "").strip())


def _master_key() -> bytes:
    """SEDOC_LOCAL_KEK, base64-decoded; must yield 32 bytes. Read on every
    use (cheap) so a rotated/injected value is never shadowed by a cache."""
    raw = os.environ.get("SEDOC_LOCAL_KEK", "").strip()
    if not raw:
        raise EnvelopeError("SEDOC_LOCAL_KEK not set on intelligence-worker")
    try:
        decoded = base64.b64decode(raw, validate=True)
    except (base64.binascii.Error, ValueError) as exc:
        raise EnvelopeError(f"SEDOC_LOCAL_KEK not valid base64: {exc}") from exc
    if len(decoded) != 32:
        raise EnvelopeError(
            f"SEDOC_LOCAL_KEK must decode to 32 bytes, got {len(decoded)}"
        )
    return decoded


def _derive_kek(kek_id: str) -> bytes:
    """HKDF-SHA256 derivation matching pkg/crypto/kms.go:deriveKEK."""
    if not kek_id:
        raise EnvelopeError("kek_id required for derivation")
    hkdf = HKDF(
        algorithm=hashes.SHA256(),
        length=DEK_SIZE,
        salt=b"vaultdms/kek/" + kek_id.encode("utf-8"),
        info=b"vaultdms/kek/v1",
    )
    return hkdf.derive(_master_key())


def unwrap_dek(wrapped_dek: bytes, kek_id: str) -> bytes:
    """Recover the plaintext DEK from its wrapped form (60-byte buffer)."""
    if len(wrapped_dek) < NONCE_SIZE + 1:
        raise EnvelopeError(f"wrapped dek too short: {len(wrapped_dek)} bytes")
    nonce = wrapped_dek[:NONCE_SIZE]
    ct = wrapped_dek[NONCE_SIZE:]
    kek = _derive_kek(kek_id)
    try:
        return AESGCM(kek).decrypt(nonce, ct, None)
    except Exception as exc:
        raise EnvelopeError(f"DEK unwrap failed (wrong kek_id?): {exc}") from exc


def decrypt_blob(ciphertext: bytes, dek_nonce: bytes, wrapped_dek: bytes, kek_id: str) -> bytes:
    """Decrypt a full envelope-encrypted blob.

    Args:
        ciphertext:  the bytes downloaded from S3 (AES-GCM ct + 16-byte tag)
        dek_nonce:   12-byte nonce used when encrypting the body (from content_blobs.dek_nonce)
        wrapped_dek: 60-byte wrapped DEK (from content_blobs.encrypted_dek)
        kek_id:      tenant KEK alias (from content_blobs.kek_id)

    Returns the original plaintext bytes.
    """
    if len(dek_nonce) != NONCE_SIZE:
        raise EnvelopeError(f"dek_nonce must be {NONCE_SIZE} bytes, got {len(dek_nonce)}")
    if len(ciphertext) < TAG_SIZE + 1:
        raise EnvelopeError(f"ciphertext too short for GCM: {len(ciphertext)} bytes")
    try:
        dek = unwrap_dek(wrapped_dek, kek_id)
        return AESGCM(dek).decrypt(dek_nonce, ciphertext, None)
    finally:
        # Best-effort zeroize. Python doesn't make this easy; the
        # bytes object is immutable. Re-binding `dek` to None is the
        # closest we get; GC eventually frees the buffer.
        pass


def encrypt_blob(plaintext: bytes, kek_id: str) -> tuple[bytes, bytes, bytes]:
    """Encrypt under a FRESH DEK, exactly like the storage service's
    encryptAll (LocalKeyManager.GenerateDataKey + crypto.EncryptData).

    Returns (ciphertext, dek_nonce, wrapped_dek) — the bytes to upload and
    the content_blobs.dek_nonce / encrypted_dek values to persist."""
    kek = _derive_kek(kek_id)
    dek = os.urandom(DEK_SIZE)
    wrap_nonce = os.urandom(NONCE_SIZE)
    wrapped = wrap_nonce + AESGCM(kek).encrypt(wrap_nonce, dek, None)
    nonce = os.urandom(NONCE_SIZE)
    ciphertext = AESGCM(dek).encrypt(nonce, plaintext, None)
    return ciphertext, nonce, wrapped
