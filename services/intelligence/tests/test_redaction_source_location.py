"""M-24 follow-up — redaction reads and writes the location recorded on
the source version's content_blobs row, never the event-supplied
bucket/key, and refuses to run when that row is not visible (it cannot
know whether the bytes are encrypted, so it must not treat them as
plaintext)."""
from __future__ import annotations

import io
from unittest import mock

import fitz
import pytest

from app.tasks import redact

SSN = "123-45-6789"


def _pdf() -> bytes:
    doc = fitz.open()
    page = doc.new_page(width=612, height=792)
    page.insert_text((72, 100), f"SSN: {SSN}", fontsize=11)
    out = io.BytesIO()
    doc.save(out)
    doc.close()
    return out.getvalue()


def _region(pdf_bytes):
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    r = doc[0].search_for(SSN)[0]
    doc.close()
    return r


class _FakeS3:
    def __init__(self, initial):
        self.store = dict(initial)
        self.downloads = []

    def download_file(self, bucket, key, dest):
        self.downloads.append((bucket, key))
        with open(dest, "wb") as fh:
            fh.write(self.store[(bucket, key)])

    def upload_file(self, src, bucket, key, ExtraArgs=None):
        with open(src, "rb") as fh:
            self.store[(bucket, key)] = fh.read()


ROW = {"id": "src-blob", "storage_bucket": "dms-eu-west-1-hot", "storage_key": "t1/real/src.pdf",
       "storage_region": "eu-west-1", "encrypted_dek": b"", "dek_nonce": b"", "kek_id": ""}


def test_redaction_uses_blob_row_location(monkeypatch):
    monkeypatch.delenv("SEDOC_LOCAL_KEK", raising=False)
    src = _pdf()
    # The event points somewhere else; the content_blobs row is the truth.
    fake_s3 = _FakeS3({("dms-eu-west-1-hot", "t1/real/src.pdf"): src,
                       ("attacker-bucket", "other/tenant.pdf"): b"not this"})
    r = _region(src)
    persisted = {}

    async def _persist(**kw):
        persisted.update(kw)
        return kw["blob_id"]

    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_fetch_source_blob", new=mock.AsyncMock(return_value=dict(ROW))), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock(side_effect=_persist)), \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()), \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()):
        result = redact.apply_document_redaction.run(
            tenant_id="t1", document_id="d1", redaction_id="r1", version_id="v1",
            storage_bucket="attacker-bucket", storage_key="other/tenant.pdf",
            regions=[{"page": 0, "x": r.x0, "y": r.y0, "width": r.x1 - r.x0,
                      "height": r.y1 - r.y0}],
            entity_types=[], applied_by="admin1",
        )
    assert fake_s3.downloads == [("dms-eu-west-1-hot", "t1/real/src.pdf")]
    assert result["redacted_key"].startswith("t1/real/")
    assert ("dms-eu-west-1-hot", result["redacted_key"]) in fake_s3.store
    assert persisted["storage_bucket"] == "dms-eu-west-1-hot"
    assert persisted["storage_region"] == "eu-west-1"

    # Candidate-review job: same rule.
    fake_s3.downloads.clear()
    persisted.clear()
    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_fetch_source_blob", new=mock.AsyncMock(return_value=dict(ROW))), \
         mock.patch.object(redact, "_persist_redacted_version",
                           new=mock.AsyncMock(side_effect=_persist)), \
         mock.patch.object(redact, "_mark_job_failed", new=mock.AsyncMock()), \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()):
        result = redact.apply_redaction_job.run(
            tenant_id="t1", job_id="j1", document_id="d1", source_version_id="v1",
            storage_bucket="attacker-bucket", storage_key="other/tenant.pdf",
            candidates=[{"rectangles": [{"page": 0, "x0": r.x0, "y0": r.y0,
                                         "x1": r.x1, "y1": r.y1}]}],
            applied_by="admin1",
        )
    assert fake_s3.downloads == [("dms-eu-west-1-hot", "t1/real/src.pdf")]
    assert ("dms-eu-west-1-hot", result["redacted_key"]) in fake_s3.store
    assert persisted["storage_bucket"] == "dms-eu-west-1-hot"


def test_redaction_missing_source_row_fails_closed():
    fake_s3 = _FakeS3({("blobs", "src/doc.pdf"): _pdf()})
    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_fetch_source_blob", new=mock.AsyncMock(return_value=None)), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock()) as persist, \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()) as failed, \
         mock.patch.object(redact, "_notify_redaction_failed"):
        with pytest.raises(redact.RedactionSourceError):
            redact.apply_document_redaction.run(
                tenant_id="t1", document_id="d1", redaction_id="r1", version_id="v1",
                storage_bucket="blobs", storage_key="src/doc.pdf",
                regions=[{"page": 0, "x": 1, "y": 1, "width": 5, "height": 5}],
                entity_types=[], applied_by="admin1",
            )
    assert fake_s3.downloads == []
    persist.assert_not_awaited()
    failed.assert_awaited_once()

    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_fetch_source_blob", new=mock.AsyncMock(return_value=None)), \
         mock.patch.object(redact, "_persist_redacted_version", new=mock.AsyncMock()) as persist2, \
         mock.patch.object(redact, "_mark_job_failed", new=mock.AsyncMock()) as failed2:
        with pytest.raises(redact.RedactionSourceError):
            redact.apply_redaction_job.run(
                tenant_id="t1", job_id="j1", document_id="d1", source_version_id="v1",
                storage_bucket="blobs", storage_key="src/doc.pdf",
                candidates=[], applied_by="admin1",
            )
    assert fake_s3.downloads == []
    persist2.assert_not_awaited()
    failed2.assert_awaited_once()
