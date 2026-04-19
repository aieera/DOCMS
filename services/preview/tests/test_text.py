from __future__ import annotations

import os

from app.processors import text as text_proc


def test_python_snippet_language_hint(workdir, sample_text):
    result = text_proc.process(sample_text, workdir, mime_type="text/x-python")
    assert result.status == "ready"
    assert result.language_hint == "python"
    assert "def greet" in result.text_preview
    assert result.metadata["page_count"] == 1


def test_snippet_truncation(workdir):
    big = os.path.join(workdir, "big.txt")
    with open(big, "w", encoding="utf-8") as f:
        f.write("x" * 20_000)
    result = text_proc.process(big, workdir, mime_type="text/plain")
    assert result.status == "ready"
    assert len(result.text_preview) == 5000
    assert result.metadata["truncated"] is True


def test_json_mime_maps_to_json(workdir):
    p = os.path.join(workdir, "data.json")
    with open(p, "w") as f:
        f.write('{"hello": "world"}\n')
    result = text_proc.process(p, workdir, mime_type="application/json")
    assert result.language_hint == "json"
