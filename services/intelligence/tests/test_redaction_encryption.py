"""M-24 — redaction decrypts envelope-encrypted sources and stores its
output encrypted with a fresh DEK, in the source blob's region.

Redaction used to download the raw object (ciphertext on any install
with a KEK, so every burn failed), upload the redacted PDF in
plaintext, and insert content_blobs rows with no DEK metadata and a
hard-coded storage_region='us-east-1'.
"""
from __future__ import annotations

import base64
import io
import os
from unittest import mock

import fitz
import pytest
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

from app.storage import envelope
from app.tasks import redact

MASTER = bytes(range(32))
TENANT = "11111111-1111-1111-1111-111111111111"
ALIAS = f"vaultdms/tenant/{TENANT}"
SSN = "123-45-6789"


def _go_style_encrypt(plaintext: bytes, kek_id: str = ALIAS):
    kek = HKDF(algorithm=hashes.SHA256(), length=32,
               salt=b"vaultdms/kek/" + kek_id.encode(), info=b"vaultdms/kek/v1").derive(MASTER)
    dek = os.urandom(32)
    wn = os.urandom(12)
    wrapped = wn + AESGCM(kek).encrypt(wn, dek, None)
    nonce = os.urandom(12)
    return AESGCM(dek).encrypt(nonce, plaintext, None), nonce, wrapped


def _pdf() -> bytes:
    doc = fitz.open()
    page = doc.new_page(width=612, height=792)
    page.insert_text((72, 100), f"SSN: {SSN}", fontsize=11)
    page.insert_text((72, 120), "Visit: 2026-04-12", fontsize=11)
    out = io.BytesIO()
    doc.save(out)
    doc.close()
    return out.getvalue()


def _ssn_region(pdf_bytes):
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    r = doc[0].search_for(SSN)[0]
    doc.close()
    return {"page": 0, "x": r.x0, "y": r.y0, "width": r.x1 - r.x0, "height": r.y1 - r.y0}


def _text(pdf_bytes):
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    t = "\n".join(p.get_text("text") for p in doc)
    doc.close()
    return t


class _FakeS3:
    def __init__(self, initial):
        self.store = dict(initial)
        self.content_types = {}

    def download_file(self, bucket, key, dest):
        with open(dest, "wb") as fh:
            fh.write(self.store[(bucket, key)])

    def upload_file(self, src, bucket, key, ExtraArgs=None):
        with open(src, "rb") as fh:
            self.store[(bucket, key)] = fh.read()
        self.content_types[(bucket, key)] = (ExtraArgs or {}).get("ContentType")


@pytest.fixture()
def kek_env(monkeypatch):
    monkeypatch.setenv("SEDOC_LOCAL_KEK", base64.b64encode(MASTER).decode())


def _source(wrapped, nonce, region="eu-west-1"):
    return {"id": "src-blob", "storage_bucket": "dms-eu-west-1-hot",
            "storage_key": "t/src.pdf", "storage_region": region,
            "encrypted_dek": wrapped, "dek_nonce": nonce, "kek_id": ALIAS}


def _assert_sealed(fake_s3, bucket, key, persisted, src_wrapped):
    stored = fake_s3.store[(bucket, key)]
    assert not stored.startswith(b"%PDF"), "redacted output stored in plaintext"
    assert fake_s3.content_types[(bucket, key)] == "application/octet-stream"
    assert persisted["storage_region"] == "eu-west-1"           # source region, not us-east-1
    assert persisted["kek_id"] == ALIAS
    assert persisted["encrypted_dek"] != src_wrapped             # fresh DEK
    assert len(persisted["dek_nonce"]) == 12
    plain = envelope.decrypt_blob(stored, persisted["dek_nonce"],
                                  persisted["encrypted_dek"], persisted["kek_id"])
    assert plain.startswith(b"%PDF")
    assert SSN not in _text(plain)


def test_redaction_output_encrypted_same_region(kek_env):
    src_plain = _pdf()
    ct, nonce, wrapped = _go_style_encrypt(src_plain)
    fake_s3 = _FakeS3({("dms-eu-west-1-hot", "t/src.pdf"): ct})
    persisted = {}

    async def _persist(**kw):
        persisted.update(kw)
        return kw["blob_id"]

    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_fetch_source_blob",
                           new=mock.AsyncMock(return_value=_source(wrapped, nonce))), \
         mock.patch.object(redact, "_resolve_live_kek_alias", new=mock.AsyncMock(return_value=ALIAS)), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock(side_effect=_persist)), \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()) as failed, \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()) as pub:
        result = redact.apply_document_redaction.run(
            tenant_id=TENANT, document_id="d1", redaction_id="r1", version_id="v1",
            storage_bucket="dms-eu-west-1-hot", storage_key="t/src.pdf",
            regions=[_ssn_region(src_plain)], entity_types=[], applied_by="admin1",
        )

    assert result["status"] == "applied", result
    failed.assert_not_awaited()
    _assert_sealed(fake_s3, "dms-eu-west-1-hot", result["redacted_key"], persisted, wrapped)
    # OCR/NER re-run on the redacted version must be able to find (and
    # decrypt) the new blob: the event carries its id.
    data = pub.await_args.args[1]["data"]
    assert data["content_blob_id"] == persisted["blob_id"]


def test_candidate_job_output_encrypted_same_region(kek_env):
    src_plain = _pdf()
    ct, nonce, wrapped = _go_style_encrypt(src_plain)
    fake_s3 = _FakeS3({("dms-eu-west-1-hot", "t/src.pdf"): ct})
    doc = fitz.open(stream=src_plain, filetype="pdf")
    r = doc[0].search_for(SSN)[0]
    doc.close()
    candidates = [{"rectangles": [{"page": 0, "x0": r.x0, "y0": r.y0, "x1": r.x1, "y1": r.y1}]}]
    persisted = {}

    async def _persist(**kw):
        persisted.update(kw)
        return kw["blob_id"]

    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_fetch_source_blob",
                           new=mock.AsyncMock(return_value=_source(wrapped, nonce))), \
         mock.patch.object(redact, "_resolve_live_kek_alias", new=mock.AsyncMock(return_value=ALIAS)), \
         mock.patch.object(redact, "_persist_redacted_version",
                           new=mock.AsyncMock(side_effect=_persist)), \
         mock.patch.object(redact, "_mark_job_failed", new=mock.AsyncMock()), \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()):
        result = redact.apply_redaction_job.run(
            tenant_id=TENANT, job_id="j1", document_id="d1", source_version_id="v1",
            storage_bucket="dms-eu-west-1-hot", storage_key="t/src.pdf",
            candidates=candidates, applied_by="admin1",
        )
    assert result["status"] == "completed"
    _assert_sealed(fake_s3, "dms-eu-west-1-hot", result["redacted_key"], persisted, wrapped)


def test_encrypted_source_without_kek_fails(monkeypatch):
    monkeypatch.delenv("SEDOC_LOCAL_KEK", raising=False)
    ct, nonce, wrapped = _go_style_encrypt(_pdf())
    fake_s3 = _FakeS3({("dms-eu-west-1-hot", "t/src.pdf"): ct})
    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_fetch_source_blob",
                           new=mock.AsyncMock(return_value=_source(wrapped, nonce))), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock()) as persist, \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()) as failed, \
         mock.patch.object(redact, "_notify_redaction_failed"):
        with pytest.raises(envelope.EnvelopeError):
            redact.apply_document_redaction.run(
                tenant_id=TENANT, document_id="d1", redaction_id="r1", version_id="v1",
                storage_bucket="dms-eu-west-1-hot", storage_key="t/src.pdf",
                regions=[{"page": 0, "x": 1, "y": 1, "width": 5, "height": 5}],
                entity_types=[], applied_by="admin1",
            )
    persist.assert_not_awaited()
    failed.assert_awaited_once()


def test_encrypt_blob_round_trip(kek_env):
    ct, nonce, wrapped = envelope.encrypt_blob(b"payload", ALIAS)
    assert envelope.decrypt_blob(ct, nonce, wrapped, ALIAS) == b"payload"
    assert len(wrapped) == envelope.WRAPPED_DEK_SIZE
