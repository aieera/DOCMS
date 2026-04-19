"""Video processor — ffmpeg thumbnail + 5 preview frames."""
from __future__ import annotations

import json
import logging
import os
import subprocess

from PIL import Image

from app.config import settings
from app.processors import ProcessResult

log = logging.getLogger(__name__)


def _probe_duration(path: str) -> float:
    try:
        result = subprocess.run(
            ["ffprobe", "-v", "error", "-show_entries",
             "format=duration", "-of", "json", path],
            capture_output=True, check=True, timeout=30,
        )
        data = json.loads(result.stdout or b"{}")
        return float(data.get("format", {}).get("duration", 0) or 0)
    except Exception as e:
        log.warning("ffprobe failed: %s", e)
        return 0.0


def _extract_frame(path: str, ts_seconds: float, dest: str, width: int) -> bool:
    try:
        subprocess.run(
            ["ffmpeg", "-y", "-ss", f"{ts_seconds:.2f}", "-i", path,
             "-vframes", "1", "-vf", f"scale={width}:-1", dest],
            timeout=settings.ffmpeg_timeout_seconds,
            capture_output=True, check=False,
        )
    except subprocess.TimeoutExpired:
        log.warning("ffmpeg timeout extracting frame at %s", ts_seconds)
        return False
    return os.path.exists(dest)


def process(input_path: str, workdir: str) -> ProcessResult:
    duration = _probe_duration(input_path)
    if duration <= 0:
        return ProcessResult(status="failed", error="cannot probe video duration")

    thumb_src = os.path.join(workdir, "thumb_src.jpg")
    thumb_ts = min(5.0, duration * 0.1)
    if not _extract_frame(input_path, thumb_ts, thumb_src, settings.thumbnail_size):
        return ProcessResult(status="failed", error="ffmpeg thumb extract failed")

    # Square-crop the ffmpeg output into a proper 256x256 thumbnail.
    thumb_path = os.path.join(workdir, "thumb.jpg")
    try:
        im = Image.open(thumb_src)
        short = min(im.size)
        left = (im.width - short) // 2
        top = (im.height - short) // 2
        im = im.crop((left, top, left + short, top + short))
        im = im.resize((settings.thumbnail_size, settings.thumbnail_size), Image.LANCZOS)
        if im.mode != "RGB":
            im = im.convert("RGB")
        im.save(thumb_path, format="JPEG", quality=80, optimize=True)
    except Exception as e:
        return ProcessResult(status="failed", error=f"thumb finalize: {type(e).__name__}")

    # 5 preview frames at 0/20/40/60/80% of duration.
    preview_paths: list[str] = []
    for i, pct in enumerate((0.0, 0.2, 0.4, 0.6, 0.8), start=1):
        ts = max(0.1, duration * pct)
        out = os.path.join(workdir, f"page_{i}.png")
        if _extract_frame(input_path, ts, out, 1280):
            preview_paths.append(out)

    return ProcessResult(
        status="ready",
        thumbnail_path=thumb_path,
        preview_paths=preview_paths,
        metadata={
            "duration_seconds": duration,
            "frames_extracted": len(preview_paths),
            "page_count": len(preview_paths),
        },
    )
