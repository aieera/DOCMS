"""Real document redaction (legal-hold /redact path) — the false-
compliance fix.

nats_consumer._on_redaction_requested used to flip document_redactions
to 'applied' WITHOUT touching the PDF: reviewers believed PII/PHI was
removed while the original text stayed readable and indexed. These tests
pin the real behavior of apply_document_redaction:

  - a region over a known SSN → the output PDF's text layer no longer
    contains the SSN (true removal, not an overlay), and verification
    passes → status 'applied' with a new redacted version;
  - a failure (verification leak / burn error) → status 'failed', and
    NO 'applied' path runs without a verified artifact.

The burn + verify run for real against PyMuPDF; S3 and Postgres are
faked so the test needs no containers.
"""
from __future__ import annotations

import io
from unittest import mock

import fitz
import pytest

from app.tasks import redact
from app.tasks.redact import (
    _burn_and_verify,
    RedactionVerificationError,
    apply_document_redaction,
)


SSN = "123-45-6789"
EMAIL = "alice@example.com"


def _pdf_with_pii() -> bytes:
    doc = fitz.open()
    page = doc.new_page(width=612, height=792)
    page.insert_text((72, 100), "Patient: Alice Doe", fontsize=11)
    page.insert_text((72, 120), f"SSN: {SSN}", fontsize=11)
    page.insert_text((72, 140), f"Email: {EMAIL}", fontsize=11)
    page.insert_text((72, 160), "Visit: 2026-04-12", fontsize=11)
    out = io.BytesIO()
    doc.save(out)
    doc.close()
    return out.getvalue()


def _ssn_region(pdf_bytes: bytes) -> dict:
    """The rectangle around the SSN, as the /redact UI would send
    ({page, x, y, width, height}, 0-based page)."""
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    rects = doc[0].search_for(SSN)
    doc.close()
    assert rects, "fixture must contain a findable SSN"
    r = rects[0]
    return {"page": 0, "x": r.x0, "y": r.y0, "width": r.x1 - r.x0, "height": r.y1 - r.y0}


def _text_of(pdf_bytes: bytes) -> str:
    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    t = "\n".join(p.get_text("text") for p in doc)
    doc.close()
    return t


# ---- the pure burn+verify core --------------------------------------------

def test_burn_and_verify_removes_ssn_from_text_layer():
    src = _pdf_with_pii()
    assert SSN in _text_of(src)  # sanity

    redacted, burned, must_absent = _burn_and_verify(src, [_ssn_region(src)], [])

    assert burned >= 1
    assert SSN in must_absent or any(SSN in s for s in must_absent)
    out_text = _text_of(redacted)
    assert SSN not in out_text, f"SSN survived burn:\n{out_text}"
    # Non-targeted content survives.
    assert "Patient" in out_text
    assert "Visit" in out_text
    # Email was NOT in a region → still present (targeted redaction).
    assert EMAIL in out_text


def test_burn_and_verify_burns_entity_values_across_pages():
    src = _pdf_with_pii()
    redacted, burned, must_absent = _burn_and_verify(src, [], [SSN, EMAIL])
    out_text = _text_of(redacted)
    assert SSN not in out_text
    assert EMAIL not in out_text
    assert SSN in must_absent and EMAIL in must_absent


def test_burn_and_verify_raises_when_content_survives():
    # A zero-area region burns nothing, but we claim the SSN must be
    # absent — verification must catch that the SSN is still present.
    src = _pdf_with_pii()
    with mock.patch.object(redact, "fitz", fitz):
        # Force a must-absent string that the burn can't have removed by
        # passing it as an entity value while making search_for find
        # nothing (use a value that isn't in the doc but assert a real
        # one is absent). Simplest: target a string physically present
        # but give a region that misses it.
        bogus_region = {"page": 0, "x": 0, "y": 0, "width": 1, "height": 1}
        with pytest.raises(RedactionVerificationError):
            # entity_values includes the SSN (added to must_absent) but
            # search_for is monkeypatched to find nothing, so it never
            # burns → the SSN survives → verify raises.
            with mock.patch("fitz.Page.search_for", return_value=[]):
                _burn_and_verify(src, [bogus_region], [SSN])


# ---- the full task: fake S3 + mocked DB -----------------------------------

class _FakeS3:
    def __init__(self, initial: dict[tuple[str, str], bytes]):
        self.store = dict(initial)

    def download_file(self, bucket, key, dest):
        with open(dest, "wb") as fh:
            fh.write(self.store[(bucket, key)])

    def upload_file(self, src, bucket, key, ExtraArgs=None):
        with open(src, "rb") as fh:
            self.store[(bucket, key)] = fh.read()


def test_apply_document_redaction_happy_path_marks_applied():
    src = _pdf_with_pii()
    fake_s3 = _FakeS3({("blobs", "src/doc.pdf"): src})
    persisted = {}

    async def _fake_persist(**kw):
        persisted.update(kw)

    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock(side_effect=_fake_persist)) as persist, \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()) as failed, \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()) as pub:
        result = apply_document_redaction.run(
            tenant_id="t1", document_id="d1", redaction_id="r1",
            version_id="v1", storage_bucket="blobs", storage_key="src/doc.pdf",
            regions=[_ssn_region(src)], entity_types=[], applied_by="admin1",
        )

    assert result["status"] == "applied"
    persist.assert_awaited_once()
    failed.assert_not_awaited()  # never the failure path on success
    pub.assert_awaited()          # version.uploaded emitted → OCR/NER/index re-run

    # The redacted artifact really was uploaded, and its text layer has
    # no SSN.
    redacted_key = result["redacted_key"]
    stored = fake_s3.store[("blobs", redacted_key)]
    assert SSN not in _text_of(stored)
    # Verified BEFORE persist was called (persist marks 'applied').
    assert persisted["version_id"] == result["redacted_version_id"]


def test_apply_document_redaction_verification_failure_marks_failed_not_applied():
    src = _pdf_with_pii()
    fake_s3 = _FakeS3({("blobs", "src/doc.pdf"): src})
    marked_failed = {}

    async def _fake_failed(**kw):
        marked_failed.update(kw)

    # Region misses the SSN, but we target the SSN via entity_types and
    # force NER to "find" it while search_for burns nothing → the SSN
    # survives → verification raises → failure path.
    with mock.patch.object(redact, "_s3", return_value=fake_s3), \
         mock.patch.object(redact, "_resolve_entity_values", return_value=[SSN]), \
         mock.patch("fitz.Page.search_for", return_value=[]), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock()) as persist, \
         mock.patch.object(redact, "_mark_redaction_failed",
                           new=mock.AsyncMock(side_effect=_fake_failed)) as failed, \
         mock.patch.object(redact, "_notify_redaction_failed") as notify, \
         mock.patch.object(redact, "publish_cloudevent", new=mock.AsyncMock()):
        with pytest.raises(RedactionVerificationError):
            apply_document_redaction.run(
                tenant_id="t1", document_id="d1", redaction_id="r1",
                version_id="v1", storage_bucket="blobs", storage_key="src/doc.pdf",
                regions=[{"page": 0, "x": 0, "y": 0, "width": 1, "height": 1}],
                entity_types=["US_SSN"], applied_by="admin1",
            )

    # The verified-artifact path never ran; the failure path did.
    persist.assert_not_awaited()
    failed.assert_awaited_once()
    assert marked_failed["redaction_id"] == "r1"
    notify.assert_called_once()  # operator notified


def test_apply_document_redaction_download_error_marks_failed():
    # S3 download blows up → failed, never applied.
    class _BrokenS3:
        def download_file(self, *a, **k):
            raise RuntimeError("s3 down")

    with mock.patch.object(redact, "_s3", return_value=_BrokenS3()), \
         mock.patch.object(redact, "_persist_redacted_document_version",
                           new=mock.AsyncMock()) as persist, \
         mock.patch.object(redact, "_mark_redaction_failed", new=mock.AsyncMock()) as failed, \
         mock.patch.object(redact, "_notify_redaction_failed"):
        with pytest.raises(RuntimeError):
            apply_document_redaction.run(
                tenant_id="t1", document_id="d1", redaction_id="r1",
                version_id="v1", storage_bucket="blobs", storage_key="src/doc.pdf",
                regions=[{"page": 0, "x": 1, "y": 1, "width": 5, "height": 5}],
                entity_types=[], applied_by="admin1",
            )

    persist.assert_not_awaited()
    failed.assert_awaited_once()
