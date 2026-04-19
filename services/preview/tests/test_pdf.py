from __future__ import annotations

import os

import pytest

pdf2image = pytest.importorskip("pdf2image")
from app.processors import pdf as pdf_proc


def _poppler_available() -> bool:
    import shutil
    return shutil.which("pdftoppm") is not None


@pytest.mark.skipif(not _poppler_available(), reason="poppler-utils not installed")
def test_pdf_renders_thumbnail_and_pages(workdir, sample_pdf):
    result = pdf_proc.process(sample_pdf, workdir)
    assert result.status == "ready"
    assert result.thumbnail_path and os.path.exists(result.thumbnail_path)
    assert result.metadata["page_count"] == 3
    assert 1 <= len(result.preview_paths) <= 3


def test_missing_pdf_returns_failed(workdir):
    result = pdf_proc.process(os.path.join(workdir, "does-not-exist.pdf"), workdir)
    assert result.status == "failed"
