"""Scan-capture page separation (capture pipeline, §3).

Splits a multi-page scan (PDF/TIFF) into per-document segments at barcode /
patch-code boundaries. Two modes:

- SEPARATOR_SHEET: a page that *is* a separator (its decoded barcode matches
  the separator pattern, or — when no pattern is set — carries any barcode)
  starts a new document and is itself dropped. The pages after a separator
  (up to the next separator) become one document; the separator's barcode
  value seeds that document's metadata. A 20-page scan with 4 separator sheets
  yields 4 documents.

- ZONAL: a barcode found in a configured zone of a *content* page marks the
  START of a new document; that page is KEPT. Pages with no start barcode
  continue the current document. (The caller restricts decoding to the zone,
  so `page_barcodes` here already contains only in-zone hits.)

This module is PURE: it operates on already-decoded per-page barcode lists, so
the segmentation algorithm is unit-testable without an image/barcode stack.
The rasterize + decode + PDF-split glue lives in pipeline.py.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from enum import Enum


class Mode(str, Enum):
    SEPARATOR_SHEET = "separator_sheet"
    ZONAL = "zonal"


@dataclass
class Segment:
    """One proposed output document: a contiguous run of source page indices."""

    pages: list[int] = field(default_factory=list)  # 0-based source page indices, in order
    barcode: str | None = None  # the boundary barcode value, if any
    title: str = ""

    def to_dict(self) -> dict:
        return {"pages": list(self.pages), "barcode": self.barcode, "title": self.title}


def _boundary_barcode(barcodes: list[str], pattern: re.Pattern | None) -> str | None:
    """Return the first barcode that marks a boundary, or None.

    With a pattern, only matching barcodes count (so content-page barcodes that
    aren't separators are ignored). Without one, any barcode is a boundary —
    the common "blank patch-code separator sheet" case.
    """
    for b in barcodes:
        if pattern is None or pattern.search(b):
            return b
    return None


def propose_segments(
    page_barcodes: list[list[str]],
    mode: Mode | str = Mode.SEPARATOR_SHEET,
    separator_pattern: str | None = None,
    title_prefix: str = "Document",
) -> list[Segment]:
    """Group page indices into segments per the chosen mode.

    `page_barcodes[i]` is the list of barcode values decoded on page i. Returns
    segments in page order; empty input → []. Deterministic — the frontend's
    manual merge/split correction edits the returned grouping, then re-submits
    it to `split` verbatim.
    """
    mode = Mode(mode)
    # A caller-supplied pattern that doesn't compile is a client error (400),
    # not a server fault (500) — surface it as a ValueError the API maps.
    if separator_pattern:
        try:
            pattern = re.compile(separator_pattern)
        except re.error as e:
            raise ValueError(f"invalid separator_pattern: {e}") from e
    else:
        pattern = None
    segments: list[Segment] = []

    if mode == Mode.SEPARATOR_SHEET:
        current = Segment()
        for i, barcodes in enumerate(page_barcodes):
            boundary = _boundary_barcode(barcodes, pattern)
            if boundary is not None:
                # Separator: close the current run (if any), drop this page,
                # and seed the next document with the separator's value.
                if current.pages:
                    segments.append(current)
                current = Segment(barcode=boundary)
                continue
            current.pages.append(i)
        if current.pages:
            segments.append(current)
    else:  # ZONAL
        current: Segment | None = None
        for i, barcodes in enumerate(page_barcodes):
            start = _boundary_barcode(barcodes, pattern)
            if start is not None:
                if current is not None and current.pages:
                    segments.append(current)
                current = Segment(barcode=start, pages=[i])  # keep the start page
            else:
                if current is None:
                    current = Segment()  # leading pages before the first marker
                current.pages.append(i)
        if current is not None and current.pages:
            segments.append(current)

    for n, seg in enumerate(segments, start=1):
        seg.title = f"{title_prefix} {n}" + (f" [{seg.barcode}]" if seg.barcode else "")
    return segments
