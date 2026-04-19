"""Image processor — thumbnail + resized preview, strips EXIF."""
from __future__ import annotations

import os
from typing import Optional

from PIL import Image, ImageOps

from app.config import settings
from app.processors import ProcessResult


def _load_oriented(path: str) -> Image.Image:
    """Open + apply EXIF rotation so thumbnails aren't sideways. Returns an
    image whose pixel data has no residual EXIF."""
    img = Image.open(path)
    img = ImageOps.exif_transpose(img)
    if img.mode not in ("RGB", "RGBA", "L"):
        img = img.convert("RGB")
    # Drop EXIF by reconstructing from pixel data.
    clean = Image.new(img.mode, img.size)
    clean.putdata(list(img.getdata()))
    return clean


def _save_thumb(img: Image.Image, dest: str) -> None:
    """Center-cropped square thumb at settings.thumbnail_size."""
    size = settings.thumbnail_size
    im = img.copy()
    short = min(im.size)
    left = (im.width - short) // 2
    top = (im.height - short) // 2
    im = im.crop((left, top, left + short, top + short))
    im = im.resize((size, size), Image.LANCZOS)
    if im.mode != "RGB":
        im = im.convert("RGB")
    im.save(dest, format="JPEG", quality=80, optimize=True)


def _save_preview(img: Image.Image, dest: str) -> None:
    """Max-dim resize preserving aspect ratio."""
    max_dim = settings.image_preview_max_dim
    im = img.copy()
    im.thumbnail((max_dim, max_dim), Image.LANCZOS)
    if im.mode not in ("RGB", "RGBA"):
        im = im.convert("RGB")
    fmt = "PNG" if im.mode == "RGBA" else "JPEG"
    if fmt == "JPEG":
        im.save(dest, format="JPEG", quality=85, optimize=True)
    else:
        im.save(dest, format="PNG", optimize=True)


def process(input_path: str, workdir: str) -> ProcessResult:
    try:
        img = _load_oriented(input_path)
    except Exception as e:
        return ProcessResult(status="failed", error=f"cannot open image: {type(e).__name__}")

    thumb_path = os.path.join(workdir, "thumb.jpg")
    preview_path = os.path.join(workdir, "page_1.png")

    try:
        _save_thumb(img, thumb_path)
        _save_preview(img, preview_path)
    except Exception as e:
        return ProcessResult(status="failed", error=f"render failed: {type(e).__name__}")

    return ProcessResult(
        status="ready",
        thumbnail_path=thumb_path,
        preview_paths=[preview_path],
        metadata={
            "page_count": 1,
            "width": img.width,
            "height": img.height,
            "mode": img.mode,
        },
    )
