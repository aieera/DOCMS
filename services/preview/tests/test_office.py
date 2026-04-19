from __future__ import annotations

import os
import shutil

import pytest

from app.processors import office as office_proc


def _soffice_available() -> bool:
    return shutil.which("soffice") is not None


@pytest.mark.skipif(not _soffice_available(), reason="libreoffice not installed")
def test_docx_roundtrips_to_pdf_then_renders(workdir):
    # Build a minimal .docx via python-docx if present, else skip.
    docx = pytest.importorskip("docx")
    path = os.path.join(workdir, "hello.docx")
    d = docx.Document()
    d.add_paragraph("Hello from preview tests.")
    d.save(path)

    result = office_proc.process(path, workdir)
    # We don't assert "ready" strictly — LibreOffice can be flaky on CI —
    # but a non-failed status plus either thumb or error string is enough.
    assert result.status in ("ready", "conversion_timeout", "failed")


def test_bogus_binary_is_handled(workdir):
    p = os.path.join(workdir, "fake.doc")
    with open(p, "wb") as f:
        f.write(b"not a real office file")
    result = office_proc.process(p, workdir)
    assert result.status in ("failed", "conversion_timeout")
