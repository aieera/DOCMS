"""MIME → processor routing."""
from __future__ import annotations

from app.tasks.preview import _select_processor
from app.processors import email as email_proc
from app.processors import image as image_proc
from app.processors import office as office_proc
from app.processors import pdf as pdf_proc
from app.processors import text as text_proc
from app.processors import video as video_proc


def test_dispatch_table():
    cases = [
        ("application/pdf", pdf_proc),
        ("image/png", image_proc),
        ("image/jpeg", image_proc),
        ("video/mp4", video_proc),
        ("message/rfc822", email_proc),
        ("application/vnd.ms-outlook", email_proc),
        ("application/vnd.openxmlformats-officedocument.wordprocessingml.document", office_proc),
        ("application/msword", office_proc),
        ("application/vnd.ms-excel", office_proc),
        ("text/plain", text_proc),
        ("text/x-python", text_proc),
        ("application/json", text_proc),
    ]
    for mime, expected in cases:
        proc, _ = _select_processor(mime)
        assert proc is expected, f"{mime} should map to {expected.__name__}"


def test_unknown_mime_returns_none():
    proc, _ = _select_processor("application/x-mystery-blob")
    assert proc is None
