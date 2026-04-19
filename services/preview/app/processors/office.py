"""Office processor — shell out to LibreOffice, then delegate to pdf.py."""
from __future__ import annotations

import logging
import os
import subprocess

from app.config import settings
from app.processors import ProcessResult
from app.processors import pdf as pdf_processor

log = logging.getLogger(__name__)


def _soffice_to_pdf(input_path: str, outdir: str) -> str | None:
    """Run `soffice --headless --convert-to pdf`. Returns the output path
    on success, None on timeout, raises on other failures."""
    try:
        result = subprocess.run(
            [
                "soffice",
                "--headless",
                "--convert-to", "pdf",
                "--outdir", outdir,
                input_path,
            ],
            timeout=settings.libreoffice_timeout_seconds,
            capture_output=True,
            check=False,
        )
    except subprocess.TimeoutExpired:
        log.warning("libreoffice timeout on %s", os.path.basename(input_path))
        return None

    if result.returncode != 0:
        log.error("libreoffice failed rc=%d stderr=%s",
                  result.returncode, result.stderr[:500].decode(errors="ignore"))
        raise RuntimeError(f"libreoffice rc={result.returncode}")

    base = os.path.splitext(os.path.basename(input_path))[0]
    pdf_path = os.path.join(outdir, base + ".pdf")
    if not os.path.exists(pdf_path):
        raise RuntimeError("libreoffice produced no output file")
    return pdf_path


def process(input_path: str, workdir: str) -> ProcessResult:
    convert_dir = os.path.join(workdir, "soffice")
    os.makedirs(convert_dir, exist_ok=True)
    try:
        pdf_path = _soffice_to_pdf(input_path, convert_dir)
    except Exception as e:
        return ProcessResult(status="failed", error=f"office convert: {type(e).__name__}")

    if pdf_path is None:
        return ProcessResult(status="conversion_timeout", error="libreoffice exceeded timeout")

    return pdf_processor.process(pdf_path, workdir)
