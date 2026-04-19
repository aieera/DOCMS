"""PDF processor — thumbnail + first-N-page renders."""
from __future__ import annotations

import os

import fitz  # PyMuPDF
from pdf2image import convert_from_path
from PIL import Image

from app.config import settings
from app.processors import ProcessResult


def _is_password_protected(path: str) -> bool:
    try:
        doc = fitz.open(path)
    except Exception:
        return False
    try:
        return bool(doc.needs_pass)
    finally:
        doc.close()


def _page_count(path: str) -> int:
    doc = fitz.open(path)
    try:
        return len(doc)
    finally:
        doc.close()


def _has_text(path: str) -> bool:
    doc = fitz.open(path)
    try:
        for page in doc:
            if page.get_text("text").strip():
                return True
        return False
    finally:
        doc.close()


def process(input_path: str, workdir: str) -> ProcessResult:
    if _is_password_protected(input_path):
        return ProcessResult(status="password_protected", error="pdf is password protected")

    try:
        page_count = _page_count(input_path)
    except Exception as e:
        return ProcessResult(status="failed", error=f"cannot open pdf: {type(e).__name__}")

    last_page = min(page_count, settings.pdf_preview_pages)

    try:
        pages = convert_from_path(
            input_path,
            dpi=settings.pdf_preview_dpi,
            first_page=1,
            last_page=last_page,
            fmt="png",
            size=(settings.pdf_preview_width, None),
        )
    except Exception as e:
        return ProcessResult(status="failed", error=f"pdf render: {type(e).__name__}")

    preview_paths: list[str] = []
    for i, page_img in enumerate(pages, start=1):
        out = os.path.join(workdir, f"page_{i}.png")
        page_img.save(out, format="PNG", optimize=True)
        preview_paths.append(out)

    # Thumbnail: center-crop the first page to a square 256.
    thumb_path = os.path.join(workdir, "thumb.jpg")
    first = pages[0].copy()
    short = min(first.size)
    left = (first.width - short) // 2
    top = (first.height - short) // 2
    first = first.crop((left, top, left + short, top + short))
    first = first.resize((settings.thumbnail_size, settings.thumbnail_size), Image.LANCZOS)
    if first.mode != "RGB":
        first = first.convert("RGB")
    first.save(thumb_path, format="JPEG", quality=80, optimize=True)

    return ProcessResult(
        status="ready",
        thumbnail_path=thumb_path,
        preview_paths=preview_paths,
        metadata={
            "page_count": page_count,
            "rendered_pages": last_page,
            "has_text": _has_text(input_path),
        },
    )
