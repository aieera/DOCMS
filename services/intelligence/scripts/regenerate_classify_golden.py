"""Regenerate services/intelligence/tests/fixtures/classify_golden/golden_outputs.json.

Runs the real tier-2 classifier on every PDF in the fixtures directory
and writes the result. Intended for the operator who just added/replaced
a fixture PDF or bumped the model version. Prints a unified diff vs the
prior golden so model drift surfaces in code review.

Usage:
    cd services/intelligence
    python scripts/regenerate_classify_golden.py [--write]

Without --write, prints the proposed JSON to stdout (dry-run).
"""
from __future__ import annotations

import argparse
import difflib
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
FIXTURES_DIR = REPO_ROOT / "tests" / "fixtures" / "classify_golden"
GOLDEN_FILE = FIXTURES_DIR / "golden_outputs.json"


def _extract_text(pdf_path: Path) -> str:
    import fitz  # PyMuPDF
    with fitz.open(pdf_path) as doc:
        return "\n".join(page.get_text() for page in doc)


def _classify_one(text: str) -> tuple[str, float, str]:
    sys.path.insert(0, str(REPO_ROOT))
    from app.tasks.classify import _tier2_ml
    return _tier2_ml(text)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--write", action="store_true",
                        help="overwrite golden_outputs.json (default: dry-run)")
    args = parser.parse_args()

    pdfs = sorted(FIXTURES_DIR.glob("*.pdf"))
    if not pdfs:
        print(f"no PDFs found in {FIXTURES_DIR}", file=sys.stderr)
        return 1

    documents = []
    model_version = None
    for pdf in pdfs:
        text = _extract_text(pdf)
        cls, conf, mv = _classify_one(text)
        if model_version is None:
            model_version = mv
        elif mv != model_version:
            print(f"WARN: model_version drift between docs ({mv} vs {model_version})",
                  file=sys.stderr)
        documents.append({
            "filename": pdf.name,
            "expected_category": cls,
            "expected_confidence": round(conf, 3),
        })

    new = {
        "_schema": {
            "model_version": "string — value of _tier2_ml's third return; freeze at regen time",
            "generated_at": "ISO-8601 UTC; rewritten by scripts/regenerate_classify_golden.py",
            "tolerance": "±0.05 on confidence; exact match on category",
            "documents": "ordered list, one entry per PDF in this directory",
        },
        "model_version": model_version,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "documents": documents,
    }
    new_text = json.dumps(new, indent=2) + "\n"

    if GOLDEN_FILE.exists():
        old_text = GOLDEN_FILE.read_text()
        diff = difflib.unified_diff(
            old_text.splitlines(keepends=True),
            new_text.splitlines(keepends=True),
            fromfile="golden_outputs.json (old)",
            tofile="golden_outputs.json (new)",
        )
        sys.stdout.write("".join(diff))
    else:
        sys.stdout.write(new_text)

    if args.write:
        GOLDEN_FILE.write_text(new_text)
        print(f"\nWrote {GOLDEN_FILE}", file=sys.stderr)
    else:
        print("\n(dry-run; pass --write to apply)", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
