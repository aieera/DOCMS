"""Shared test fixtures. Sample files are generated in-memory per test so
the repo stays free of binary fixtures."""
from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

import pytest

# Ensure `app` is importable without installing the service.
ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))


@pytest.fixture
def workdir():
    d = tempfile.mkdtemp(prefix="preview-test-")
    yield d
    # Best-effort cleanup; tests may have closed file handles already.
    import shutil
    shutil.rmtree(d, ignore_errors=True)


@pytest.fixture
def sample_png(workdir):
    """640x480 RGB PNG with solid fill."""
    from PIL import Image
    p = os.path.join(workdir, "sample.png")
    Image.new("RGB", (640, 480), color=(70, 130, 180)).save(p, "PNG")
    return p


@pytest.fixture
def sample_jpeg_with_exif(workdir):
    """JPEG carrying a fabricated EXIF block — used to verify strip."""
    from PIL import Image
    import piexif  # optional; fall back to Image.save without exif if missing
    p = os.path.join(workdir, "sample_exif.jpg")
    img = Image.new("RGB", (800, 600), color=(200, 100, 50))
    try:
        exif_dict = {"0th": {piexif.ImageIFD.Make: b"TestCamera"}}
        exif_bytes = piexif.dump(exif_dict)
        img.save(p, "JPEG", exif=exif_bytes)
    except ImportError:
        img.save(p, "JPEG")
    return p


@pytest.fixture
def sample_text(workdir):
    p = os.path.join(workdir, "sample.py")
    with open(p, "w", encoding="utf-8") as f:
        f.write("def greet(name):\n    return f'hello, {name}'\n\nprint(greet('world'))\n")
    return p


@pytest.fixture
def sample_pdf(workdir):
    """3-page PDF via PyMuPDF."""
    import fitz
    p = os.path.join(workdir, "sample.pdf")
    doc = fitz.open()
    for i in range(3):
        page = doc.new_page()
        page.insert_text((72, 72), f"Page {i + 1}")
    doc.save(p)
    doc.close()
    return p


@pytest.fixture
def sample_eml(workdir):
    p = os.path.join(workdir, "sample.eml")
    with open(p, "w", encoding="utf-8") as f:
        f.write(
            "From: alice@example.com\r\n"
            "To: bob@example.com\r\n"
            "Subject: Test\r\n"
            "Date: Thu, 16 Apr 2026 10:00:00 +0000\r\n"
            "Content-Type: text/plain; charset=utf-8\r\n"
            "\r\n"
            "Hello Bob, this is the body.\r\n"
        )
    return p
