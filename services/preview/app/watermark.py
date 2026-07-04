"""Dynamic watermark rendering (§5).

The document service resolves the viewer's identity + the tenant/classification
watermark config, substitutes tokens into the final text, and calls the internal
endpoints here with a ready-to-draw ``WatermarkSpec`` (text + style). This module
is purely presentational — it never sees identity semantics or client input.

One overlay generator (:func:`render_overlay`) produces a transparent RGBA layer
of tiled, rotated, low-opacity text. It is reused for both surfaces:
  * in-app page view  -> composite the overlay onto the cached base PNG
  * download / print  -> stamp the overlay onto every PDF page via PyMuPDF
so the two surfaces are visually consistent.
"""
from __future__ import annotations

import io
import logging
from dataclasses import dataclass

from PIL import Image, ImageDraw, ImageFont

log = logging.getLogger(__name__)

# Candidate TrueType fonts; the first that loads wins. A scalable font is
# required (PIL's builtin bitmap font can't size), so fall back to load_default
# only as a last resort.
_FONT_CANDIDATES = [
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
    "/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
    "DejaVuSans.ttf",
]

# Hard caps so a pathological font size or page dimension can't OOM the worker.
# The document service already validates config (font 6-200), but these
# endpoints are defense-in-depth against any caller.
_MAX_FONT = 300
_MAX_DIM = 12000


@dataclass(frozen=True)
class WatermarkSpec:
    """Resolved, ready-to-draw watermark style. Text is already token-substituted
    by the document service (e.g. 'alice@acme.com · 2026-07-01 14:03 UTC · 203.0.113.7')."""

    text: str
    opacity: int = 15         # 0-100
    rotation_deg: int = 30
    tile: bool = True
    font_size: int = 28
    color: str = "#808080"    # hex #RRGGBB


def _load_font(size: int) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    size = max(6, min(_MAX_FONT, int(size)))
    for path in _FONT_CANDIDATES:
        try:
            return ImageFont.truetype(path, size)
        except Exception:  # noqa: BLE001 — any load failure => try next candidate
            continue
    log.warning("watermark: no scalable TrueType font found; falling back to bitmap default")
    return ImageFont.load_default()


def _hex_to_rgb(color: str) -> tuple[int, int, int]:
    c = (color or "").lstrip("#")
    if len(c) == 6:
        try:
            return int(c[0:2], 16), int(c[2:4], 16), int(c[4:6], 16)
        except ValueError:
            pass
    return 128, 128, 128


def _clamp_opacity(opacity: int) -> int:
    try:
        o = int(opacity)
    except (TypeError, ValueError):
        o = 15
    return max(0, min(100, o))


def render_overlay(width: int, height: int, spec: WatermarkSpec) -> Image.Image:
    """Return a transparent RGBA overlay of the (tiled) rotated watermark text."""
    # Clamp dimensions so a pathological page/scale can't allocate a giant image.
    width = max(1, min(_MAX_DIM, int(width)))
    height = max(1, min(_MAX_DIM, int(height)))
    overlay = Image.new("RGBA", (width, height), (0, 0, 0, 0))
    text = (spec.text or "").strip()
    if not text:
        return overlay

    r, g, b = _hex_to_rgb(spec.color)
    alpha = int(round(_clamp_opacity(spec.opacity) / 100 * 255))
    if alpha <= 0:
        return overlay
    font = _load_font(spec.font_size)

    # Draw the text onto its own tight tile, then rotate the tile once.
    measure = ImageDraw.Draw(Image.new("RGBA", (1, 1)))
    bbox = measure.textbbox((0, 0), text, font=font)
    tw, th = bbox[2] - bbox[0], bbox[3] - bbox[1]
    pad = max(spec.font_size, 12)
    tile = Image.new("RGBA", (tw + 2 * pad, th + 2 * pad), (0, 0, 0, 0))
    ImageDraw.Draw(tile).text((pad - bbox[0], pad - bbox[1]), text, font=font, fill=(r, g, b, alpha))
    tile = tile.rotate(spec.rotation_deg or 0, expand=True, resample=Image.BICUBIC)

    if spec.tile:
        step_x = max(1, int(tile.width * 1.15))
        step_y = max(1, int(tile.height * 1.6))
        # Start slightly off-canvas so rotated tiles cover the corners.
        for y in range(-tile.height, height + step_y, step_y):
            for x in range(-tile.width, width + step_x, step_x):
                overlay.alpha_composite(tile, (x, y))
    else:
        overlay.alpha_composite(tile, ((width - tile.width) // 2, (height - tile.height) // 2))
    return overlay


def stamp_image_bytes(base_png: bytes, spec: WatermarkSpec) -> bytes:
    """Composite the watermark onto a rendered page image; return PNG bytes."""
    base = Image.open(io.BytesIO(base_png)).convert("RGBA")
    overlay = render_overlay(base.width, base.height, spec)
    out = Image.alpha_composite(base, overlay).convert("RGB")
    buf = io.BytesIO()
    out.save(buf, format="PNG")
    return buf.getvalue()


def stamp_pdf_bytes(pdf_bytes: bytes, spec: WatermarkSpec) -> bytes:
    """Burn the watermark into every page of a PDF (vector-preserving); return PDF bytes.

    The overlay is rasterized once per page size and stamped via insert_image,
    which supports the arbitrary rotation angle PyMuPDF's insert_text does not.
    """
    import fitz  # PyMuPDF — imported lazily so image-only stamping needs no PDF dep

    doc = fitz.open(stream=pdf_bytes, filetype="pdf")
    try:
        scale = 2  # supersample the overlay so rotated text stays crisp at print DPI
        last_key = None
        overlay_png = None
        for page in doc:
            rect = page.rect
            w, h = int(rect.width * scale), int(rect.height * scale)
            key = (w, h)
            # Most PDFs share one page size — render the overlay once and reuse.
            if key != last_key:
                ov = render_overlay(w, h, WatermarkSpec(
                    text=spec.text, opacity=spec.opacity, rotation_deg=spec.rotation_deg,
                    tile=spec.tile, font_size=int(spec.font_size * scale), color=spec.color,
                ))
                buf = io.BytesIO()
                ov.save(buf, format="PNG")
                overlay_png = buf.getvalue()
                last_key = key
            page.insert_image(rect, stream=overlay_png, overlay=True, keep_proportion=False)
        return doc.tobytes()
    finally:
        doc.close()
