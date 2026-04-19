"""Per-format preview processors.

Each processor exposes a `process(input_path, workdir) -> ProcessResult`
function. Selection is by MIME type in tasks.preview.dispatch.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional


@dataclass
class ProcessResult:
    """What every processor returns.

    - thumbnail_path:    absolute path to a 256x256 JPEG thumbnail, or None
    - preview_paths:     ordered list of full preview renders (pages, frames)
    - metadata:          JSON-serializable dict (page_count, duration, etc.)
    - text_preview:      first-5000-char snippet for text/code files
    - status:            ready | failed | password_protected | too_large |
                         conversion_timeout | unsupported
    - error:             human-readable reason when status != ready
    """
    status: str = "ready"
    thumbnail_path: Optional[str] = None
    preview_paths: list[str] = field(default_factory=list)
    metadata: dict = field(default_factory=dict)
    text_preview: Optional[str] = None
    language_hint: Optional[str] = None
    error: Optional[str] = None
