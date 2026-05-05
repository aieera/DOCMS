"""Section-aware chunking tests (ADR 0063 §"Chunking").

Covers the heading detector + section_path stamping + page resolution
that the workspace RAG path relies on for precise citations. The
existing chunk-boundary tests in test_embed.py still apply on top of
these — the §6.8 enrichments are additive.
"""
from __future__ import annotations

import pytest

from app.chunker import _detect_heading, chunk_text


# ---- _detect_heading regex matrix --------------------------------------

@pytest.mark.parametrize("line,expected", [
    ("ARTICLE 5",           (1, "Article 5")),
    ("ARTICLE V",           (1, "Article V")),
    ("Article 5 — Term",    (1, "Article 5 — Term")),
    ("Section 5.2",         (2, "Section 5.2")),
    ("SECTION 5.2 SCOPE",   (2, "Section 5.2 SCOPE")),
    ("Clause 5.2.1",        (3, "Clause 5.2.1")),
    ("# Title",             (1, "Title")),
    ("## Sub",              (2, "Sub")),
    ("### Detail",          (3, "Detail")),
    ("5.2 Definitions",     (2, "5.2 Definitions")),
    ("5.2.1 Sub-defs",      (3, "5.2.1 Sub-defs")),
    ("DEFINITIONS AND INTERPRETATION", (2, "Definitions And Interpretation")),
])
def test_detect_heading_recognizes_common_shapes(line, expected):
    assert _detect_heading(line) == expected


@pytest.mark.parametrize("line", [
    "",
    "  ",
    "This is a normal sentence with a period.",
    "This is also normal but ends with a question mark?",
    # All-caps single word — too noisy to count as a heading.
    "TOTAL",
    # Long lines never count, even if all-caps.
    "X" * 220,
    # Numeric outline that's actually a list item with no title.
    "1.2.3",
    # Lowercase prose shouldn't accidentally match the "all caps" heuristic.
    "the quick brown fox jumps over the lazy dog",
])
def test_detect_heading_skips_non_headings(line):
    assert _detect_heading(line) is None


# ---- section_path stamping ---------------------------------------------

CONTRACT = (
    "ARTICLE 1\n"
    "This Master Services Agreement is entered into as of the Effective Date.\n"
    "\n"
    "Section 1.1\n"
    "The parties named below agree to the following terms and conditions.\n"
    "\n"
    "ARTICLE 2\n"
    "Confidentiality. The receiving party shall maintain in strict confidence "
    "all Confidential Information disclosed by the disclosing party.\n"
    "\n"
    "Section 2.1\n"
    "Confidential Information means any information that is marked confidential.\n"
)


def test_chunker_stamps_section_path_on_contract():
    # Tight token budget forces enough chunks that one starts inside
    # Section 1.1 (depth 2), one inside Section 2.1 (also depth 2).
    chunks = chunk_text(CONTRACT, chunk_size_tokens=24, chunk_overlap_tokens=4)
    assert len(chunks) >= 2
    paths = [c["section_path"] for c in chunks if c["section_path"]]
    # Every chunk that lands inside an ARTICLE block should pick up
    # the cumulative path.
    assert any("Article 1" in p for p in paths), paths
    assert any("Article 2" in p for p in paths), paths
    # Section_path nests properly: a chunk that starts inside
    # Section 1.1 should carry "Article 1 / Section 1.1".
    nested = [p for p in paths if "Section 1.1" in p]
    assert nested, f"expected a nested path for Section 1.1; got {paths}"
    assert all(p.startswith("Article 1 / Section 1.1") for p in nested)


def test_chunker_section_path_pops_on_new_top_level():
    """Nesting stack must pop when a new Article appears — without
    that, every chunk would inherit Article 1's path even after we
    cross into Article 2."""
    chunks = chunk_text(CONTRACT, chunk_size_tokens=32, chunk_overlap_tokens=4)
    article_2_chunks = [c for c in chunks if c["section_path"] and "Article 2" in c["section_path"]]
    assert article_2_chunks, "expected at least one chunk under Article 2"
    # No Article 2 chunk should also list Article 1 (pop semantics).
    for c in article_2_chunks:
        assert "Article 1" not in c["section_path"], (
            f"section path should pop when a new article begins; got {c['section_path']}"
        )


def test_chunker_section_path_none_on_plain_prose():
    plain = (
        "This is just a paragraph of plain prose with no headings whatsoever. "
        "It goes on for a sentence or two so the chunker has something to work with. "
        "Final sentence to round things out."
    )
    chunks = chunk_text(plain, chunk_size_tokens=64, chunk_overlap_tokens=8)
    assert chunks
    assert all(c["section_path"] is None for c in chunks)


# ---- page mapping ------------------------------------------------------

def test_chunker_resolves_page_number_from_pages_arg():
    # Each page text has to be long enough that the chunker emits at
    # least one chunk whose start_char falls inside the page's range.
    # Tight budget + long text per page guarantees coverage.
    long = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. " * 12
    pages = [
        {"page_number": 1, "text_content": long},
        {"page_number": 2, "text_content": long},
        {"page_number": 3, "text_content": long},
    ]
    text = "\n\n".join(p["text_content"] for p in pages)
    chunks = chunk_text(text, chunk_size_tokens=64, chunk_overlap_tokens=8, pages=pages)
    pages_seen = {c["page_number"] for c in chunks}
    # All three pages should appear at least once.
    assert pages_seen == {1, 2, 3}, pages_seen


def test_chunker_page_number_none_without_pages_arg():
    chunks = chunk_text("Some text on a page." * 20, chunk_size_tokens=32)
    assert all(c["page_number"] is None for c in chunks)
