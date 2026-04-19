from __future__ import annotations

import os
import shutil
import subprocess

import pytest

from app.processors import video as video_proc


def _ffmpeg_available() -> bool:
    return shutil.which("ffmpeg") is not None and shutil.which("ffprobe") is not None


@pytest.mark.skipif(not _ffmpeg_available(), reason="ffmpeg not installed")
def test_synthetic_video_produces_thumbnail(workdir):
    src = os.path.join(workdir, "sample.mp4")
    # 3s of SMPTE bars — no network, no fixture files.
    subprocess.run(
        ["ffmpeg", "-y", "-f", "lavfi", "-i", "smptebars=duration=3:size=320x240:rate=10",
         "-c:v", "libx264", "-pix_fmt", "yuv420p", src],
        capture_output=True, check=True,
    )
    result = video_proc.process(src, workdir)
    assert result.status == "ready"
    assert result.thumbnail_path and os.path.exists(result.thumbnail_path)
    assert result.metadata["duration_seconds"] > 0


def test_nonexistent_video_fails(workdir):
    result = video_proc.process(os.path.join(workdir, "nope.mp4"), workdir)
    assert result.status == "failed"
