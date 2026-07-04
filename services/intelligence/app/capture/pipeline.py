"""Rasterize + barcode-decode + PDF-split glue for the capture pipeline.

The heavy deps (PyMuPDF/fitz, Pillow, zxing-cpp) are imported lazily inside
the functions, so importing this module — and the pure `separation` module —
never requires the image stack. This keeps the segmentation algorithm and its
unit test runnable in any environment, while the actual decode/rasterize runs
only inside the intelligence worker where the deps are installed.

NOTE: barcode detection here is review-verified, not runtime-verified in CI —
it needs zxing-cpp + sample scanned bundles. The pure segmentation logic in
`separation.py` IS unit-tested.
"""
from __future__ import annotations

import base64
import io
from dataclasses import dataclass

from .separation import Mode, Segment, propose_segments

# Zone as fractions 0..1 of the page: (x0, y0, x1, y1). None = whole page.
Zone = tuple[float, float, float, float] | None


def _is_pdf(data: bytes, mime: str) -> bool:
    return mime == "application/pdf" or data[:5] == b"%PDF-"


def _load_images(data: bytes, mime: str):
    """Yield one PIL.Image (RGB) per page — PDF via PyMuPDF, else multi-frame TIFF/image via Pillow."""
    from PIL import Image

    if _is_pdf(data, mime):
        import fitz  # PyMuPDF

        doc = fitz.open(stream=data, filetype="pdf")
        try:
            for page in doc:
                pix = page.get_pixmap(dpi=200)
                yield Image.open(io.BytesIO(pix.tobytes("png"))).convert("RGB")
        finally:
            doc.close()
        return

    img = Image.open(io.BytesIO(data))
    frame = 0
    while True:
        try:
            img.seek(frame)
        except EOFError:
            break
        yield img.convert("RGB")
        frame += 1


def _crop_zone(img, zone: Zone):
    if not zone:
        return img
    w, h = img.size
    x0, y0, x1, y1 = zone
    return img.crop((int(x0 * w), int(y0 * h), int(x1 * w), int(y1 * h)))


def _decode(img) -> list[str]:
    """Decode every barcode / patch-code in `img`; [] if none."""
    import zxingcpp

    return [r.text for r in zxingcpp.read_barcodes(img) if getattr(r, "valid", True) and r.text]


def _thumb_b64(img, max_px: int = 180) -> str:
    img = img.copy()
    img.thumbnail((max_px, max_px))
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return base64.b64encode(buf.getvalue()).decode("ascii")


@dataclass
class Analysis:
    page_count: int
    page_barcodes: list[list[str]]
    page_thumbnails: list[str]  # base64 PNG per page, for the split-preview UI
    segments: list[Segment]


def analyze(
    data: bytes,
    mime: str,
    mode: Mode | str = Mode.SEPARATOR_SHEET,
    separator_pattern: str | None = None,
    zone: Zone = None,
) -> Analysis:
    """Decode every page, then propose the split. Returns thumbnails so the UI
    can render the proposed grouping for manual correction before commit."""
    page_barcodes: list[list[str]] = []
    thumbs: list[str] = []
    for img in _load_images(data, mime):
        page_barcodes.append(_decode(_crop_zone(img, zone)))
        thumbs.append(_thumb_b64(img))
    segments = propose_segments(page_barcodes, mode, separator_pattern)
    return Analysis(len(page_barcodes), page_barcodes, thumbs, segments)


def split(data: bytes, mime: str, page_groups: list[list[int]]) -> list[bytes]:
    """Render each page-group (the user-corrected grouping) to a standalone PDF
    — one output document per group, ready for IngestFile."""
    if _is_pdf(data, mime):
        import fitz

        src = fitz.open(stream=data, filetype="pdf")
        out: list[bytes] = []
        try:
            for pages in page_groups:
                seg = fitz.open()
                for p in pages:
                    seg.insert_pdf(src, from_page=p, to_page=p)
                out.append(seg.tobytes())
                seg.close()
        finally:
            src.close()
        return out

    # TIFF / image bundle → render each group to a PDF via Pillow.
    imgs = list(_load_images(data, mime))
    out = []
    for pages in page_groups:
        group = [imgs[p] for p in pages]
        if not group:
            continue
        buf = io.BytesIO()
        group[0].save(buf, format="PDF", save_all=True, append_images=group[1:])
        out.append(buf.getvalue())
    return out
