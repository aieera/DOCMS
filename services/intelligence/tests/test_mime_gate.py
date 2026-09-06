"""Extraction-gate predicate (QA SD-02 / SD-13).

The upload-event consumer kept its own copy of OCR_MIMES without the
text-mime branch, so directly-uploaded .txt files were ACK'd and skipped:
never extracted, never language-detected, never embedded, never indexed —
unfindable in search while the Text tab showed "OCR Pending" forever.
app.mime_gate is the single source of truth both the consumer and the OCR
task import, so the two can no longer drift.
"""
from app.mime_gate import OCR_MIMES, is_extractable_mime, is_text_mime


def test_text_files_are_extractable():
    # The bytes ARE the text — no OCR needed, but extraction must run so
    # content flows to lang_detect + embed + index.
    assert is_extractable_mime("text/plain")
    assert is_extractable_mime("text/markdown")
    assert is_extractable_mime("text/csv")
    assert is_extractable_mime("application/json")


def test_ocr_mimes_are_extractable():
    for mime in OCR_MIMES:
        assert is_extractable_mime(mime), mime


def test_binary_non_ocr_mimes_are_not_extractable():
    assert not is_extractable_mime("application/zip")
    assert not is_extractable_mime("application/octet-stream")
    assert not is_extractable_mime("")


def test_is_text_mime_matches_textual_applications():
    assert is_text_mime("application/xml")
    assert not is_text_mime("application/pdf")
