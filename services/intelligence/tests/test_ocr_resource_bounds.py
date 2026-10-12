"""H-16 — OCR rasterisation is budgeted and worker-loss replays are bounded.

`page.get_pixmap(dpi=150)` ran with no page/pixel budget (unlike
capture/pipeline.py): a tiny PDF declaring a huge MediaBox OOM-killed the
worker, and acks_late + reject_on_worker_lost re-queued it forever.
"""
from __future__ import annotations

import io
import os
import struct
import zlib
from unittest import mock

import fitz
import pytest
from PIL import Image

from app.tasks import ocr


def _giant_page_pdf(path: str) -> None:
    doc = fitz.open()
    doc.new_page(width=14400, height=14400)  # 200 in square: ~900 MP at 150 dpi
    doc.save(path)
    doc.close()


def test_ocr_giant_page_terminal(tmp_path):
    src = str(tmp_path / "giant.pdf")
    _giant_page_pdf(src)
    with mock.patch.object(fitz.Page, "get_pixmap",
                           side_effect=AssertionError("must not rasterize an over-budget page")):
        with pytest.raises(ocr.TerminalOCRError) as ei:
            ocr._ocr_pdf_pages(src, "printed")
    assert ei.value.reason == "too_large"


def test_ocr_too_many_pages_terminal(tmp_path):
    from app.capture.pipeline import MAX_PAGES
    src = str(tmp_path / "many.pdf")
    doc = fitz.open()
    for _ in range(MAX_PAGES + 1):
        doc.new_page(width=72, height=72)
    doc.save(src)
    doc.close()
    with mock.patch.object(fitz.Page, "get_pixmap",
                           side_effect=AssertionError("must not rasterize")):
        with pytest.raises(ocr.TerminalOCRError) as ei:
            ocr._ocr_pdf_pages(src, "printed")
    assert ei.value.reason == "too_large"


def test_ocr_giant_image_terminal(tmp_path):
    src = str(tmp_path / "bomb.png")
    Image.new("L", (8, 8), 0).save(src, "PNG")
    data = bytearray(open(src, "rb").read())
    body = bytearray(data[12:29])
    body[4:8] = struct.pack(">I", 9000)
    body[8:12] = struct.pack(">I", 9000)
    data[12:29] = body
    data[29:33] = struct.pack(">I", zlib.crc32(bytes(body)) & 0xFFFFFFFF)
    open(src, "wb").write(bytes(data))
    with mock.patch.object(Image.Image, "load", side_effect=AssertionError("must not decode")):
        with pytest.raises(ocr.TerminalOCRError) as ei:
            ocr._ocr_image(src, "printed")
    assert ei.value.reason == "too_large"


def test_ocr_too_large_maps_to_displayable_reason():
    from app.processing_stages import ocr_failure_reason
    assert ocr_failure_reason(ocr.TerminalOCRError("too_large", "x")) == "file_corrupted"


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
        tenant_id="t1", document_id="d1", version_id="v1", content_blob_id="b1",
        region_pin="us-east-1", storage_bucket="dms-us-east-1-hot",
        storage_key="t1/src.pdf", mime_type="application/pdf", event_id="e1",
    )


def test_worker_lost_loop_bounded(monkeypatch):
    from app import worker

    fake = _FakeRedis()
    monkeypatch.setattr(worker, "_attempt_redis", lambda: fake)
    dlq = mock.AsyncMock()
    failed = mock.AsyncMock()
    stages = []
    monkeypatch.setattr(ocr, "publish_dlq", dlq)
    monkeypatch.setattr(ocr, "mark_failed", failed)
    monkeypatch.setattr(ocr, "record_stage", lambda **kw: stages.append(kw))
    s3 = mock.Mock(side_effect=AssertionError("a poisoned task must not run again"))
    monkeypatch.setattr(ocr, "_s3", s3)

    key = worker.attempt_key(ocr.process_ocr.name, "ocr-task-1")
    fake.store[key] = 3  # three deliveries started and never finished

    res = ocr.process_ocr.apply(kwargs=_kwargs(), task_id="ocr-task-1")

    assert res.successful()
    assert res.result["status"] == "failed"
    s3.assert_not_called()
    dlq.assert_awaited_once()
    assert dlq.await_args.kwargs["reason"] == "worker_lost"
    assert dlq.await_args.kwargs["version_id"] == "v1"
    failed.assert_awaited_once()
    assert stages[-1]["status"] == "failed"
    assert stages[-1]["failure_reason"] == "worker_crash"
    assert key not in fake.store


def test_clean_failure_clears_attempt_counter(monkeypatch):
    """A task that raises (rather than killing its worker) is a normal
    failure; it must not accumulate toward the worker-loss budget."""
    from app import worker

    fake = _FakeRedis()
    monkeypatch.setattr(worker, "_attempt_redis", lambda: fake)
    monkeypatch.setattr(ocr, "record_stage", lambda **kw: None)
    monkeypatch.setattr(ocr, "publish_dlq", mock.AsyncMock())
    monkeypatch.setattr(ocr, "mark_failed", mock.AsyncMock())

    class _S3:
        def download_file(self, *a):
            raise RuntimeError("s3 down")

    monkeypatch.setattr(ocr, "_s3", lambda: _S3())
    key = worker.attempt_key(ocr.process_ocr.name, "ocr-task-2")
    res = ocr.process_ocr.apply(kwargs=_kwargs(), task_id="ocr-task-2")
    assert not res.successful()
    assert key not in fake.store
