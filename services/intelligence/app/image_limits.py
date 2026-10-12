"""H-16 — process-wide Pillow decompression-bomb limits (matches preview).

Imported by app/__init__.py so the limits are in force before any module
in the service opens an image. Pillow's default ceiling (~89 MP, hard
error only at 2x) lets a small file that claims a huge canvas through;
50 MP covers any real scan, and the warning band between 1x and 2x the
ceiling is made an error as well.
"""
from __future__ import annotations

import warnings

from PIL import Image

MAX_IMAGE_PIXELS = 50_000_000


def install() -> None:
    """Apply the limits. Idempotent; runs at import. The warnings filter
    is process-global state, so callers that must not depend on it (OCR,
    capture) also check the decoded size explicitly."""
    Image.MAX_IMAGE_PIXELS = MAX_IMAGE_PIXELS
    warnings.simplefilter("error", Image.DecompressionBombWarning)


install()

# Both surface from Image.open() for an over-limit image.
BOMB_ERRORS = (Image.DecompressionBombError, Image.DecompressionBombWarning)
