"""Envelope decryption for blobs written by the storage service (M-24).

Python port of pkg/crypto (LocalKeyManager + DecryptData), byte-for-byte
compatible with services/storage's encryptAll:

  * DEK: 32 bytes (AES-256); body nonce: 12 bytes (content_blobs.dek_nonce)
  * body: AES-256-GCM(DEK, nonce) with the 16-byte tag appended
  * wrapped DEK (content_blobs.encrypted_dek): nonce(12) || AES-GCM(KEK, DEK)
  * KEK = HKDF-SHA256(ikm=base64-decoded SEDOC_LOCAL_KEK,
                      salt=b"vaultdms/kek/" + kek_id, info=b"vaultdms/kek/v1")

Local-KEK mode only (the mode the storage service runs). Vault / AWS KMS
managers are not supported here.
"""
from __future__ import annotations

import base64
import binascii
import os

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

DEK_SIZE = 32
NONCE_SIZE = 12
TAG_SIZE = 16


class EnvelopeError(Exception):
    """Any decrypt failure: missing/invalid KEK, wrong kek_id, corrupt or
    truncated ciphertext. Never retryable — the bytes won't change."""


def _master_key() -> bytes:
    raw = os.environ.get("SEDOC_LOCAL_KEK", "").strip()
    if not raw:
        raise EnvelopeError("SEDOC_LOCAL_KEK not set; cannot decrypt an encrypted blob")
    try:
        decoded = base64.b64decode(raw, validate=True)
    except (binascii.Error, ValueError) as exc:
        raise EnvelopeError("SEDOC_LOCAL_KEK is not valid base64") from exc
    if len(decoded) != DEK_SIZE:
        raise EnvelopeError(f"SEDOC_LOCAL_KEK must decode to {DEK_SIZE} bytes")
    return decoded


def derive_kek(kek_id: str) -> bytes:
    if not kek_id:
        raise EnvelopeError("kek_id required")
    return HKDF(
        algorithm=hashes.SHA256(), length=DEK_SIZE,
        salt=b"vaultdms/kek/" + kek_id.encode("utf-8"), info=b"vaultdms/kek/v1",
    ).derive(_master_key())


def decrypt_blob(ciphertext: bytes, dek_nonce: bytes, wrapped_dek: bytes, kek_id: str) -> bytes:
    if len(dek_nonce) != NONCE_SIZE:
        raise EnvelopeError(f"dek_nonce must be {NONCE_SIZE} bytes")
    if len(wrapped_dek) < NONCE_SIZE + 1:
        raise EnvelopeError("wrapped DEK too short")
    if len(ciphertext) < TAG_SIZE:
        raise EnvelopeError("ciphertext too short for GCM")
    try:
        dek = AESGCM(derive_kek(kek_id)).decrypt(wrapped_dek[:NONCE_SIZE], wrapped_dek[NONCE_SIZE:], None)
        return AESGCM(dek).decrypt(dek_nonce, ciphertext, None)
    except EnvelopeError:
        raise
    except Exception as exc:  # InvalidTag, ValueError
        raise EnvelopeError(f"envelope decrypt failed: {type(exc).__name__}") from exc


def decrypt_file_in_place(path: str, dek_nonce: bytes, wrapped_dek: bytes, kek_id: str) -> None:
    with open(path, "rb") as fh:
        ciphertext = fh.read()
    plaintext = decrypt_blob(ciphertext, dek_nonce, wrapped_dek, kek_id)
    with open(path, "wb") as fh:
        fh.write(plaintext)
