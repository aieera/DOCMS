"""Integration test for the capture glue (pipeline.py).

Exercises the REAL PyMuPDF rasterize + page-extraction + the analyze/split
wiring against a real in-memory PDF. The barcode *decode* (zxing-cpp) is
stubbed — decoding real barcodes off a rasterized page is resolution-sensitive
and can't be tuned without sample scans, so the DoD's barcode reading stays a
manual/live-stack check; everything around it is verified here.

Skips cleanly (and is runnable via plain `python3`) when PyMuPDF/Pillow aren't
installed, so it lives in the repo ready to run inside the intelligence image.
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


def _have_image_deps() -> bool:
    try:
        import fitz  # noqa: F401  (PyMuPDF)
        from PIL import Image  # noqa: F401

        return True
    except Exception:  # noqa: BLE001
        return False


def _make_pdf(n_pages: int) -> bytes:
    import fitz

    doc = fitz.open()
    for i in range(n_pages):
        page = doc.new_page()
        page.insert_text((72, 72), f"PAGE {i + 1}")
    data = doc.tobytes()
    doc.close()
    return data


def test_split_extracts_exact_page_ranges():
    import fitz

    from app.capture import pipeline

    pdf = _make_pdf(6)
    parts = pipeline.split(pdf, "application/pdf", [[0, 1], [2], [3, 4, 5]])
    assert len(parts) == 3
    counts = []
    for p in parts:
        d = fitz.open(stream=p, filetype="pdf")
        counts.append(d.page_count)
        d.close()
    assert counts == [2, 1, 3]


def test_analyze_wires_decode_into_dod_split():
    # DoD scenario end-to-end through the real rasterize + thumbnail + propose
    # path, with a stubbed decoder placing separators on pages 0/5/10/15.
    from app.capture import pipeline

    pdf = _make_pdf(20)
    seps = {0, 5, 10, 15}
    counter = {"i": -1}

    def fake_decode(_img):
        counter["i"] += 1
        return ["PATCH-T"] if counter["i"] in seps else []

    orig = pipeline._decode
    pipeline._decode = fake_decode
    try:
        res = pipeline.analyze(pdf, "application/pdf", "separator_sheet", r"^PATCH-")
    finally:
        pipeline._decode = orig

    assert res.page_count == 20
    assert len(res.page_thumbnails) == 20  # preview thumbnails really generated
    assert len(res.segments) == 4
    assert [len(s.pages) for s in res.segments] == [4, 4, 4, 4]
    assert res.segments[0].pages == [1, 2, 3, 4]


if __name__ == "__main__":
    if not _have_image_deps():
        print("SKIP test_capture_integration — PyMuPDF/Pillow not installed "
              "(runs inside the intelligence image)")
        sys.exit(0)
    import traceback

    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    failed = 0
    for fn in fns:
        try:
            fn()
            print(f"PASS {fn.__name__}")
        except Exception:  # noqa: BLE001
            failed += 1
            print(f"FAIL {fn.__name__}")
            traceback.print_exc()
    print(f"\n{len(fns) - failed}/{len(fns)} passed")
    sys.exit(1 if failed else 0)
