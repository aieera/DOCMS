from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.capture.separation import Mode, propose_segments  # noqa: E402


def test_separator_sheet_basic():
    # [content, content, SEP-001, content, SEP-002, content]
    pb = [[], [], ["SEP-001"], [], ["SEP-002"], []]
    segs = propose_segments(pb, Mode.SEPARATOR_SHEET, separator_pattern=r"^SEP-")
    assert [s.pages for s in segs] == [[0, 1], [3], [5]]
    # The separator value seeds the document that follows it.
    assert segs[1].barcode == "SEP-001"
    assert segs[2].barcode == "SEP-002"
    # Separator pages are dropped (not in any segment).
    assert all(2 not in s.pages and 4 not in s.pages for s in segs)


def test_dod_20_page_4_separators_yields_4_documents():
    # DoD: a 20-page bundle with 4 separators → 4 documents.
    pb = [[] for _ in range(20)]
    for sep_page in (0, 5, 10, 15):
        pb[sep_page] = ["PATCH-T"]
    segs = propose_segments(pb, Mode.SEPARATOR_SHEET, separator_pattern=r"^PATCH-")
    assert len(segs) == 4
    assert [len(s.pages) for s in segs] == [4, 4, 4, 4]
    assert segs[0].pages == [1, 2, 3, 4]
    assert segs[3].pages == [16, 17, 18, 19]


def test_separator_no_pattern_treats_any_barcode_as_separator():
    pb = [[], ["anything"], [], ["x"], []]
    segs = propose_segments(pb, Mode.SEPARATOR_SHEET, separator_pattern=None)
    assert [s.pages for s in segs] == [[0], [2], [4]]


def test_content_barcodes_ignored_when_pattern_set():
    # A content page carries an unrelated barcode (e.g. an invoice number) —
    # it must NOT split when it doesn't match the separator pattern.
    pb = [["INV-99"], [], ["SEP-1"], ["INV-100"], []]
    segs = propose_segments(pb, Mode.SEPARATOR_SHEET, separator_pattern=r"^SEP-")
    assert [s.pages for s in segs] == [[0, 1], [3, 4]]


def test_zonal_start_page_is_kept():
    # ZONAL: an in-zone barcode starts a new doc and the page stays.
    pb = [["DOC-A"], [], [], ["DOC-B"], []]
    segs = propose_segments(pb, Mode.ZONAL)
    assert [s.pages for s in segs] == [[0, 1, 2], [3, 4]]
    assert segs[0].barcode == "DOC-A"
    assert segs[1].barcode == "DOC-B"


def test_zonal_leading_pages_before_first_marker():
    pb = [[], ["DOC-A"], []]
    segs = propose_segments(pb, Mode.ZONAL)
    assert [s.pages for s in segs] == [[0], [1, 2]]


def test_empty_input():
    assert propose_segments([], Mode.SEPARATOR_SHEET) == []


if __name__ == "__main__":
    # Runnable without pytest (it isn't installed in every env).
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
