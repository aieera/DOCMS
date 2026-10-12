"""M-24 — preview decrypts envelope-encrypted blobs.

Storage now encrypts every blob (per-blob DEK, wrapped by a per-tenant
KEK derived from SEDOC_LOCAL_KEK — pkg/crypto). The preview worker
handed the raw ciphertext to the processors, so any install with a KEK
broke every preview.
"""
from __future__ import annotations

import base64
import io
import os

import pytest
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF
from PIL import Image

MASTER = bytes(range(32))
KEK_ID = "vaultdms/tenant/11111111-1111-1111-1111-111111111111"


def _go_style_encrypt(plaintext: bytes, master: bytes = MASTER, kek_id: str = KEK_ID):
    """What services/storage does: LocalKeyManager.GenerateDataKey +
    crypto.EncryptData (pkg/crypto/kms.go, envelope.go)."""
    kek = HKDF(algorithm=hashes.SHA256(), length=32,
               salt=b"vaultdms/kek/" + kek_id.encode(), info=b"vaultdms/kek/v1").derive(master)
    dek = os.urandom(32)
    wrap_nonce = os.urandom(12)
    wrapped = wrap_nonce + AESGCM(kek).encrypt(wrap_nonce, dek, None)
    nonce = os.urandom(12)
    return AESGCM(dek).encrypt(nonce, plaintext, None), nonce, wrapped


def _png() -> bytes:
    buf = io.BytesIO()
    Image.new("RGB", (64, 48), (10, 120, 200)).save(buf, "PNG")
    return buf.getvalue()


def test_envelope_round_trip_matches_go_format(monkeypatch):
    from app import envelope
    monkeypatch.setenv("SEDOC_LOCAL_KEK", base64.b64encode(MASTER).decode())
    ct, nonce, wrapped = _go_style_encrypt(b"hello preview")
    assert envelope.decrypt_blob(ct, nonce, wrapped, KEK_ID) == b"hello preview"
    with pytest.raises(envelope.EnvelopeError):
        envelope.decrypt_blob(ct, nonce, wrapped, KEK_ID + "/other")  # tenant boundary


def _run(monkeypatch, blob_meta, stored: bytes):
    from app.tasks import preview as task_mod

    uploaded = {}
    monkeypatch.setattr(task_mod.s3, "head", lambda b, k: {"ContentLength": len(stored)})
    monkeypatch.setattr(task_mod.s3, "download_to",
                        lambda b, k, p: open(p, "wb").write(stored))
    monkeypatch.setattr(task_mod, "fetch_source_blob", lambda t, b: blob_meta)
    monkeypatch.setattr(task_mod, "_cache_manifest", lambda *a: None)
    monkeypatch.setattr(task_mod, "_publish_ready_sync", lambda *a: None)

    def _upload(bucket, t, d, v, result):
        uploaded["result"] = result
        return {"thumbnail_key": "k/thumb.jpg", "preview_page_keys": ["k/page_1.png"],
                "metadata_key": "k/metadata.json"}

    monkeypatch.setattr(task_mod, "_upload_artifacts", _upload)
    out = task_mod.generate_preview.run(
        tenant_id="11111111-1111-1111-1111-111111111111", document_id="d1",
        version_id="v1", content_blob_id="b1", mime_type="image/png",
        region_pin="us-east-1", storage_bucket="dms-us-east-1-hot", storage_key="t/src",
    )
    return out, uploaded


def test_preview_decrypts_envelope(monkeypatch):
    monkeypatch.setenv("SEDOC_LOCAL_KEK", base64.b64encode(MASTER).decode())
    ct, nonce, wrapped = _go_style_encrypt(_png())
    meta = {"storage_bucket": "dms-us-east-1-hot", "storage_key": "t/src",
            "storage_region": "us-east-1", "encrypted_dek": wrapped,
            "dek_nonce": nonce, "kek_id": KEK_ID}
    out, uploaded = _run(monkeypatch, meta, ct)
    assert out["status"] == "ready", out
    assert uploaded["result"].metadata["width"] == 64


def test_preview_plaintext_blob_still_works(monkeypatch):
    meta = {"storage_bucket": "dms-us-east-1-hot", "storage_key": "t/src",
            "storage_region": "us-east-1", "encrypted_dek": b"", "dek_nonce": b"", "kek_id": ""}
    out, _ = _run(monkeypatch, meta, _png())
    assert out["status"] == "ready", out


def test_preview_wrong_kek_is_terminal_failure(monkeypatch):
    monkeypatch.setenv("SEDOC_LOCAL_KEK", base64.b64encode(b"\x01" * 32).decode())
    ct, nonce, wrapped = _go_style_encrypt(_png())
    meta = {"storage_bucket": "dms-us-east-1-hot", "storage_key": "t/src",
            "storage_region": "us-east-1", "encrypted_dek": wrapped,
            "dek_nonce": nonce, "kek_id": KEK_ID}
    out, uploaded = _run(monkeypatch, meta, ct)   # returns, does not raise/retry
    assert out["status"] == "failed"
    assert "decrypt" in out["error"]
    assert "result" not in uploaded
