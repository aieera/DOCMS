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

# Capture safety limits. A scanned bundle is attacker-influenced input, so
# reject one that would rasterize to an unbounded amount of memory BEFORE
# allocating anything. PyMuPDF's get_pixmap bypasses Pillow's
# DecompressionBomb guard, so the per-page pixel budget is enforced here
# explicitly. A tiny PDF can declare a huge MediaBox or tens of thousands of
# pages; both are rejected up front.
RENDER_DPI = 200
MAX_PAGES = 500
MAX_PAGE_PIXELS = 40_000_000  # ~40 MP per rasterized page


class CaptureLimitError(ValueError):
    """Raised when a capture input bundle exceeds a safety limit or carries an
    invalid page grouping. The API layer maps this to a 400/413, never a 500."""


def _is_pdf(data: bytes, mime: str) -> bool:
    return mime == "application/pdf" or data[:5] == b"%PDF-"


def _load_images(data: bytes, mime: str):
    """Yield one PIL.Image (RGB) per page — PDF via PyMuPDF, else multi-frame TIFF/image via Pillow."""
    from PIL import Image

    if _is_pdf(data, mime):
        import fitz  # PyMuPDF

        doc = fitz.open(stream=data, filetype="pdf")
        try:
            if doc.page_count > MAX_PAGES:
                raise CaptureLimitError(f"bundle has {doc.page_count} pages (max {MAX_PAGES})")
            for page in doc:
                # Bound the rasterized pixel count before get_pixmap allocates.
                rect = page.rect
                px = (rect.width / 72.0 * RENDER_DPI) * (rect.height / 72.0 * RENDER_DPI)
                if px > MAX_PAGE_PIXELS:
                    raise CaptureLimitError(
                        f"page {page.number} rasterizes to ~{int(px)} px (max {MAX_PAGE_PIXELS})"
                    )
                pix = page.get_pixmap(dpi=RENDER_DPI)
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
        if frame >= MAX_PAGES:
            raise CaptureLimitError(f"bundle has more than {MAX_PAGES} frames")
        w, h = img.size
        if w * h > MAX_PAGE_PIXELS:
            raise CaptureLimitError(f"frame {frame} is {w}x{h} px (max {MAX_PAGE_PIXELS})")
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


def _validate_groups(page_groups: list[list[int]], page_count: int) -> None:
    """Reject page groupings that would corrupt the split. Every index must be
    in [0, page_count) — the caller supplies these, and unchecked values were
    dangerous: a PDF from_page=-1 is PyMuPDF's 'whole document' sentinel (so a
    single -1 copied the ENTIRE bundle into one segment), while for images a
    negative index silently emits the wrong (wrapped) page and an out-of-range
    index raised IndexError that aborted the whole split. Empty groups are also
    rejected so every segment yields exactly one output PDF — otherwise the two
    code paths handled them differently and the outputs mis-paired with the
    input groups."""
    for gi, pages in enumerate(page_groups):
        if not pages:
            raise CaptureLimitError(f"page group {gi} is empty")
        for p in pages:
            if not isinstance(p, int) or isinstance(p, bool) or p < 0 or p >= page_count:
                raise CaptureLimitError(f"page index {p!r} out of range [0, {page_count})")


def split(data: bytes, mime: str, page_groups: list[list[int]]) -> list[bytes]:
    """Render each page-group (the user-corrected grouping) to a standalone PDF
    — one output document per group, ready for IngestFile. Output length always
    equals len(page_groups)."""
    if _is_pdf(data, mime):
        import fitz

        src = fitz.open(stream=data, filetype="pdf")
        out: list[bytes] = []
        try:
            _validate_groups(page_groups, src.page_count)
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
    _validate_groups(page_groups, len(imgs))
    out = []
    for pages in page_groups:
        group = [imgs[p] for p in pages]
        buf = io.BytesIO()
        group[0].save(buf, format="PDF", save_all=True, append_images=group[1:])
        out.append(buf.getvalue())
    return out
