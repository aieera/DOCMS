from __future__ import annotations

from app.processors import email as email_proc


def test_eml_parses_headers_and_body(workdir, sample_eml):
    result = email_proc.process(sample_eml, workdir, mime_type="message/rfc822")
    assert result.status == "ready"
    md = result.metadata
    assert md["from"].startswith("alice@")
    assert md["subject"] == "Test"
    assert md["attachment_count"] == 0
    assert "Hello Bob" in result.text_preview


def test_corrupt_msg_fails(workdir, tmp_path):
    bad = tmp_path / "bad.msg"
    bad.write_bytes(b"not a real outlook file")
    result = email_proc.process(str(bad), workdir, mime_type="application/vnd.ms-outlook")
    assert result.status == "failed"
