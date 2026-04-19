from __future__ import annotations

import os

from PIL import Image

from app.processors import image as image_proc


def test_png_produces_thumbnail_and_preview(workdir, sample_png):
    result = image_proc.process(sample_png, workdir)
    assert result.status == "ready"
    assert result.thumbnail_path and os.path.exists(result.thumbnail_path)
    assert result.preview_paths and os.path.exists(result.preview_paths[0])

    thumb = Image.open(result.thumbnail_path)
    assert thumb.size == (256, 256)
    assert result.metadata["width"] == 640
    assert result.metadata["height"] == 480


def test_exif_is_stripped(workdir, sample_jpeg_with_exif):
    result = image_proc.process(sample_jpeg_with_exif, workdir)
    assert result.status == "ready"
    thumb = Image.open(result.thumbnail_path)
    # Pillow exposes EXIF via _getexif / info['exif']; both should be empty.
    assert not thumb.info.get("exif")
    assert thumb._getexif() in (None, {})


def test_corrupt_image_fails_gracefully(workdir):
    bad = os.path.join(workdir, "bad.png")
    with open(bad, "wb") as f:
        f.write(b"not actually a png")
    result = image_proc.process(bad, workdir)
    assert result.status == "failed"
    assert result.error
