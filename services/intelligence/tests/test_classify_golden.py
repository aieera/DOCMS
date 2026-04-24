"""Tier-2 classifier golden-file regression test.

Skipped automatically when:
  - the fixture corpus is missing or empty (default scaffold state);
  - the tier-2 model isn't importable (no torch/transformers in env);
  - PyMuPDF (fitz) isn't installed.

When the corpus is populated and the model is available, asserts:
  - predicted category matches the golden category exactly;
  - predicted confidence is within ±CLASSIFY_GOLDEN_TOLERANCE of golden;
  - the model_version in the golden file matches the current code's
    model_version (catches stale goldens after a model bump).

To regenerate the golden file after corpus/model changes:
    python scripts/regenerate_classify_golden.py
"""
from __future__ import annotations

import json
from pathlib import Path

import pytest

CLASSIFY_GOLDEN_TOLERANCE = 0.05

FIXTURES_DIR = Path(__file__).parent / "fixtures" / "classify_golden"
GOLDEN_FILE = FIXTURES_DIR / "golden_outputs.json"


def _extract_text(pdf_path: Path) -> str:
    """Tier-2 classifier consumes plain text; we extract via PyMuPDF
    rather than running OCR because the corpus is born-digital PDFs."""
    import fitz  # type: ignore  # PyMuPDF
    with fitz.open(pdf_path) as doc:
        return "\n".join(page.get_text() for page in doc)


def _corpus_ready() -> tuple[bool, str]:
    if not GOLDEN_FILE.exists():
        return False, "golden_outputs.json missing"
    data = json.loads(GOLDEN_FILE.read_text())
    docs = data.get("documents", [])
    if not docs or any(d.get("_note", "").startswith("PLACEHOLDER") for d in docs):
        return False, "golden file is the scaffold placeholder; populate corpus + regen"
    pdfs_present = all((FIXTURES_DIR / d["filename"]).exists() for d in docs)
    if not pdfs_present:
        return False, "one or more PDFs listed in golden_outputs.json are missing"
    return True, ""


def _tier2_available() -> tuple[bool, str]:
    try:
        from app.tasks.classify import _tier2_ml  # noqa: F401
        from app.models.classifier import classify  # noqa: F401
        import fitz  # noqa: F401
    except Exception as exc:  # pragma: no cover — env-dependent
        return False, f"tier-2 deps missing: {exc.__class__.__name__}: {exc}"
    return True, ""


@pytest.fixture(scope="module")
def golden() -> dict:
    return json.loads(GOLDEN_FILE.read_text()) if GOLDEN_FILE.exists() else {}


def test_classify_golden_corpus(golden):
    corpus_ok, corpus_reason = _corpus_ready()
    if not corpus_ok:
        pytest.skip(corpus_reason)
    deps_ok, deps_reason = _tier2_available()
    if not deps_ok:
        pytest.skip(deps_reason)

    from app.tasks.classify import _tier2_ml

    expected_model = golden.get("model_version")
    failures: list[str] = []

    for entry in golden["documents"]:
        text = _extract_text(FIXTURES_DIR / entry["filename"])
        cls, conf, model_version = _tier2_ml(text)

        if expected_model and model_version != expected_model:
            failures.append(
                f"{entry['filename']}: model_version drift "
                f"(golden={expected_model}, current={model_version}) — "
                f"regenerate golden_outputs.json"
            )
            continue
        if cls != entry["expected_category"]:
            failures.append(
                f"{entry['filename']}: category {cls!r} != golden {entry['expected_category']!r}"
            )
            continue
        delta = abs(conf - entry["expected_confidence"])
        if delta > CLASSIFY_GOLDEN_TOLERANCE:
            failures.append(
                f"{entry['filename']}: confidence {conf:.3f} "
                f"differs from golden {entry['expected_confidence']:.3f} "
                f"by {delta:.3f} (>{CLASSIFY_GOLDEN_TOLERANCE})"
            )

    assert not failures, "Golden classification regressions:\n  - " + "\n  - ".join(failures)
