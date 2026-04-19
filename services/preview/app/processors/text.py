"""Text / code processor — read a snippet, guess a language hint."""
from __future__ import annotations

import os

from app.processors import ProcessResult

_MAX_SNIPPET_CHARS = 5000

# Extension → language hint for syntax-highlighting. Conservative list —
# anything unmapped falls back to "text".
_LANG_BY_EXT = {
    ".py": "python",
    ".go": "go",
    ".rs": "rust",
    ".js": "javascript",
    ".mjs": "javascript",
    ".ts": "typescript",
    ".tsx": "typescript",
    ".jsx": "javascript",
    ".java": "java",
    ".kt": "kotlin",
    ".rb": "ruby",
    ".php": "php",
    ".c": "c",
    ".h": "c",
    ".cpp": "cpp",
    ".cc": "cpp",
    ".hpp": "cpp",
    ".cs": "csharp",
    ".sh": "bash",
    ".sql": "sql",
    ".json": "json",
    ".yaml": "yaml",
    ".yml": "yaml",
    ".toml": "toml",
    ".xml": "xml",
    ".html": "html",
    ".css": "css",
    ".md": "markdown",
    ".csv": "csv",
}


def _detect_language(path: str, mime: str | None) -> str:
    ext = os.path.splitext(path)[1].lower()
    if ext in _LANG_BY_EXT:
        return _LANG_BY_EXT[ext]
    if mime:
        if "json" in mime:
            return "json"
        if "xml" in mime:
            return "xml"
        if "markdown" in mime:
            return "markdown"
        if "csv" in mime:
            return "csv"
    return "text"


def process(input_path: str, workdir: str, mime_type: str | None = None) -> ProcessResult:
    try:
        with open(input_path, "rb") as f:
            raw = f.read(_MAX_SNIPPET_CHARS * 4)  # worst-case 4 bytes/char
        snippet = raw.decode("utf-8", errors="replace")[:_MAX_SNIPPET_CHARS]
    except Exception as e:
        return ProcessResult(status="failed", error=f"read text: {type(e).__name__}")

    size = os.path.getsize(input_path)
    lines = snippet.count("\n") + (0 if snippet.endswith("\n") else 1)

    return ProcessResult(
        status="ready",
        text_preview=snippet,
        language_hint=_detect_language(input_path, mime_type),
        metadata={
            "page_count": 1,
            "byte_size": size,
            "line_count_snippet": lines,
            "truncated": size > len(snippet.encode("utf-8", errors="replace")),
        },
    )
