"""H-16 — image bombs and worker-loss replay loops are bounded.

`list(img.getdata())` + putdata copied every pixel into a Python list: a
small ~9000x9000 PNG (below Pillow's default bomb threshold) allocated
several GB, the worker was OOM-killed, and acks_late +
reject_on_worker_lost re-queued it without counting a retry — a
permanent replay loop.
"""
from __future__ import annotations

import os
import struct
import zlib
from unittest import mock

from PIL import Image

from app.processors import image as image_proc


def _png_claiming(path: str, width: int, height: int) -> None:
    """Write a tiny PNG, then rewrite its IHDR to claim width x height —
    Pillow reads the claimed size on open, nothing large is allocated."""
    Image.new("L", (8, 8), 0).save(path, "PNG")
    with open(path, "rb") as fh:
        data = bytearray(fh.read())
    # signature(8) | len(4) | 'IHDR'(4) | width(4) height(4) ... | crc(4)
    ihdr_start = 8 + 4
    body = bytearray(data[ihdr_start:ihdr_start + 4 + 13])
    body[4:8] = struct.pack(">I", width)
    body[8:12] = struct.pack(">I", height)
    data[ihdr_start:ihdr_start + 17] = body
    data[ihdr_start + 17:ihdr_start + 21] = struct.pack(">I", zlib.crc32(bytes(body)) & 0xFFFFFFFF)
    with open(path, "wb") as fh:
        fh.write(bytes(data))


def test_image_bomb_rejected(workdir):
    assert Image.MAX_IMAGE_PIXELS == 50_000_000
    for w, h in ((9000, 9000), (8000, 7000), (20000, 20000)):  # 81 MP, 56 MP, 400 MP
        src = os.path.join(workdir, f"bomb_{w}x{h}.png")
        _png_claiming(src, w, h)
        with mock.patch.object(Image.Image, "load",
                               side_effect=AssertionError("pixels must not be decoded")):
            res = image_proc.process(src, workdir)
        assert res.status == "too_large", (w, h, res.status, res.error)
        assert res.thumbnail_path is None


def test_image_preview_does_not_copy_pixels_through_python(sample_png, workdir):
    with mock.patch.object(Image.Image, "putdata",
                           side_effect=AssertionError("putdata copies through a Python list")), \
         mock.patch.object(Image.Image, "getdata",
                           side_effect=AssertionError("getdata copies through a Python list")):
        res = image_proc.process(sample_png, workdir)
    assert res.status == "ready"
    assert res.metadata["width"] == 640 and res.metadata["height"] == 480


class _FakeRedis:
    def __init__(self):
        self.store: dict[str, int] = {}

    def incr(self, k):
        self.store[k] = self.store.get(k, 0) + 1
        return self.store[k]

    def expire(self, k, ttl):
        return True

    def delete(self, k):
        self.store.pop(k, None)


def _kwargs():
    return dict(
        tenant_id="t1", document_id="d1", version_id="v1", content_blob_id="",
        mime_type="image/png", region_pin="us-east-1",
        storage_bucket="dms-us-east-1-hot", storage_key="t1/src.png",
    )


def test_worker_lost_loop_bounded(monkeypatch):
    from app import worker
    from app.tasks import preview as task_mod

    fake = _FakeRedis()
    monkeypatch.setattr(worker, "_attempt_redis", lambda: fake)
    cached = []
    monkeypatch.setattr(task_mod, "_cache_manifest", lambda url, t, v, m: cached.append(dict(m)))
    processor = mock.Mock(side_effect=AssertionError("must not process a poisoned task"))
    monkeypatch.setattr(task_mod, "_select_processor", lambda m: (processor, {}))
    monkeypatch.setattr(task_mod.s3, "head", processor)

    key = worker.attempt_key(task_mod.generate_preview.name, "task-123")
    # Three earlier deliveries started and never finished: the worker was
    # killed (OOM / hard time limit) before the task could return.
    fake.store[key] = 3

    res = task_mod.generate_preview.apply(kwargs=_kwargs(), task_id="task-123")

    assert res.successful()
    assert res.result["status"] == "failed"
    assert "worker" in res.result["error"]
    processor.assert_not_called()
    assert cached and cached[-1]["status"] == "failed"
    assert key not in fake.store  # dead-lettered; the message is acked and gone


def test_clean_finish_clears_attempt_counter(monkeypatch):
    from app import worker
    from app.tasks import preview as task_mod

    fake = _FakeRedis()
    monkeypatch.setattr(worker, "_attempt_redis", lambda: fake)
    monkeypatch.setattr(task_mod, "_cache_manifest", lambda *a: None)
    monkeypatch.setattr(task_mod.s3, "head", lambda b, k: {"ContentLength": 10})
    monkeypatch.setattr(task_mod.s3, "download_to", lambda b, k, p: open(p, "wb").write(b"x"))
    monkeypatch.setattr(task_mod, "_select_processor", lambda m: (None, {}))

    key = worker.attempt_key(task_mod.generate_preview.name, "task-ok")
    fake.store[key] = 1  # one earlier loss
    res = task_mod.generate_preview.apply(kwargs=_kwargs(), task_id="task-ok")
    assert res.result["status"] == "unsupported"   # it really ran
    assert key not in fake.store                    # a clean return resets the count
