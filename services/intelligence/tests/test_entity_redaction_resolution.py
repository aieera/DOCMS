"""H-15 — entity-type redaction can no longer burn nothing and be marked
`applied`.

_resolve_entity_values called detect_entities with an EMPTY tenant; the
dedupe insert failed on tenant_id='', the exception was swallowed, []
came back, verification passed trivially and a byte-identical
"redacted" version became current. Now the regex/spaCy passes run
directly (no DB, no Celery round-trip) and a requested type that
resolves to nothing fails the job.
"""
from __future__ import annotations

import io
from unittest import mock

import fitz
import pytest

from app.tasks import redact
from app.tasks.redact import apply_document_redaction

EMAIL = "alice@example.com"


def _pdf(text_lines) -> bytes:
    doc = fitz.open()
    page = doc.new_page(width=612, height=792)
    y = 100
    for line in text_lines:
        page.insert_text((72, y), line, fontsize=11)
        y += 20
    out = io.BytesIO()
    doc.save(out)
    doc.close()
    return out.getvalue()


def _text_of(pdf_bytes: bytes) -> str:
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    t = "\n".join(p.get_text("text") for p in doc)
    doc.close()
    return t


class _FakeS3:
    def __init__(self, initial):
        self.store = dict(initial)

    def download_file(self, bucket, key, dest):
        with open(dest, "wb") as fh:
            fh.write(self.store[(bucket, key)])

    def upload_file(self, src, bucket, key, ExtraArgs=None):
        with open(src, "rb") as fh:
            self.store[(bucket, key)] = fh.read()


def _no_celery_ner():
    """The old path round-tripped through the detect_entities Celery task
    with tenant ''. It must not be used at all any more."""
    return mock.patch.object(
        redact.detect_entities, "apply",
        side_effect=AssertionError("detect_entities task must not be used"),
    )


def test_resolve_entity_values_runs_passes_directly():
    src = _pdf(["Contact: " + EMAIL, "Visit: 2026-04-12"])
    with _no_celery_ner():
        values = redact._resolve_entity_values(src, ["EMAIL"], tenant_id="t1")
    assert values == [EMAIL]


def test_resolve_entity_values_empty_resolution_raises():
    src = _pdf(["No personal data on this page."])
    with _no_celery_ner():
        with pytest.raises(redact.RedactionResolutionError):
            redact._resolve_entity_values(src, ["phone"], tenant_id="t1")


def test_entity_redaction_empty_resolution_fails():
    src = _pdf(["No personal data on this page."])
    fake_s3 = _FakeS3({("blobs", "src/doc.pdf"): src})
    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         _no_celery_ner(), \
         mock.patch.object(redact, "_fetch_source_blob", new=mock.AsyncMock(return_value=None)), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock()) as persist, \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()) as failed, \
         mock.patch.object(redact, "_notify_redaction_failed") as notify, \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()) as pub:
        with pytest.raises(redact.RedactionResolutionError):
            apply_document_redaction.run(
                tenant_id="t1", document_id="d1", redaction_id="r1",
                version_id="v1", storage_bucket="blobs", storage_key="src/doc.pdf",
                regions=[], entity_types=["phone"], applied_by="admin1",
            )

    persist.assert_not_awaited()      # never 'applied'
    pub.assert_not_awaited()          # no new version announced
    failed.assert_awaited_once()      # visibly failed
    notify.assert_called_once()
    assert list(fake_s3.store) == [("blobs", "src/doc.pdf")]  # nothing uploaded


def test_entity_redaction_burns_resolved_values():
    src = _pdf(["Contact: " + EMAIL])
    fake_s3 = _FakeS3({("blobs", "src/doc.pdf"): src})
    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         _no_celery_ner(), \
         mock.patch.object(redact, "_fetch_source_blob", new=mock.AsyncMock(return_value=None)), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock()) as persist, \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()), \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()):
        result = apply_document_redaction.run(
            tenant_id="t1", document_id="d1", redaction_id="r1",
            version_id="v1", storage_bucket="blobs", storage_key="src/doc.pdf",
            regions=[], entity_types=["email"], applied_by="admin1",
        )
    assert result["status"] == "applied"
    persist.assert_awaited_once()
    assert EMAIL not in _text_of(fake_s3.store[("blobs", result["redacted_key"])])
