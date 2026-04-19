"""HLS transcoding task — produces an .m3u8 + segmented .ts files.

Runs on the `preview-video` queue so CPU-heavy encoding doesn't starve
lightweight thumbnail workers.
"""
from __future__ import annotations

import json
import logging
import os
import shutil
import subprocess
import tempfile
from datetime import datetime, timezone

from app.config import settings
from app.s3_client import preview_bucket, preview_key, s3
from app.worker import celery_app

log = logging.getLogger(__name__)


@celery_app.task(
    name="app.tasks.video.generate_hls",
    bind=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 2},
    retry_backoff=True,
    retry_backoff_max=300,
)
def generate_hls(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    region_pin: str,
    storage_bucket: str,
    storage_key: str,
):
    """Transcode a video to HLS (6s segments, single 720p rendition) and
    upload the manifest + segments to the preview bucket. Keeps the
    rendition count minimal — adaptive bitrate is a follow-up."""
    workdir = tempfile.mkdtemp(prefix="hls-")
    src = os.path.join(workdir, "source")
    hls_dir = os.path.join(workdir, "hls")
    os.makedirs(hls_dir, exist_ok=True)

    try:
        s3.download_to(storage_bucket, storage_key, src)

        playlist = os.path.join(hls_dir, "index.m3u8")
        segment_pattern = os.path.join(hls_dir, "seg_%04d.ts")

        cmd = [
            "ffmpeg", "-y", "-i", src,
            "-vf", "scale=-2:720",
            "-c:v", "libx264", "-preset", "veryfast", "-crf", "22",
            "-c:a", "aac", "-b:a", "128k",
            "-hls_time", "6",
            "-hls_playlist_type", "vod",
            "-hls_segment_filename", segment_pattern,
            playlist,
        ]
        proc = subprocess.run(cmd, capture_output=True,
                              timeout=max(settings.ffmpeg_timeout_seconds, 300),
                              check=False)
        if proc.returncode != 0:
            log.error("ffmpeg hls rc=%d stderr=%s",
                      proc.returncode, proc.stderr[:500].decode(errors="ignore"))
            raise RuntimeError(f"ffmpeg hls rc={proc.returncode}")

        bucket = preview_bucket(region_pin)
        s3.ensure_bucket(bucket)

        uploaded: list[str] = []
        for fname in sorted(os.listdir(hls_dir)):
            local = os.path.join(hls_dir, fname)
            k = preview_key(tenant_id, document_id, version_id, f"hls/{fname}")
            ctype = "application/vnd.apple.mpegurl" if fname.endswith(".m3u8") else "video/mp2t"
            s3.upload_file(local, bucket, k, ctype)
            uploaded.append(k)

        manifest = {
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "bucket": bucket,
            "playlist_key": next((k for k in uploaded if k.endswith("index.m3u8")), None),
            "segment_keys": [k for k in uploaded if k.endswith(".ts")],
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "status": "ready",
        }
        return json.loads(json.dumps(manifest))  # ensure JSON-clean return

    finally:
        shutil.rmtree(workdir, ignore_errors=True)
