"""BUG-16 (perf) — Surya weights load at worker startup, not on the first
document.

Measured on a CPU worker in this image: `load_surya()` takes ~99 s while a
subsequent single-page OCR takes ~16 s. Whoever uploaded first paid the
whole model load inside their request. Preloading moves it to boot.

Only workers that actually consume `intelligence-ocr` may pay it — the
misc worker deliberately excludes that queue so a long OCR run can't block
classify/embed, and it must not hold ~2 GB of OCR weights either.
"""
from __future__ import annotations

from unittest import mock

from app import worker as worker_mod
from app.worker import OCR_QUEUE, _consumes_ocr_queue, _warm_models


def test_ocr_queue_detected_when_selected():
    with mock.patch.object(worker_mod.celery_app, "amqp",
                           mock.Mock(queues={"intelligence", OCR_QUEUE})):
        assert _consumes_ocr_queue() is True


def test_ocr_queue_not_detected_for_the_misc_worker():
    with mock.patch.object(worker_mod.celery_app, "amqp",
                           mock.Mock(queues={"intelligence", "intelligence-embed",
                                             "intelligence-rag"})):
        assert _consumes_ocr_queue() is False


def test_queue_introspection_failure_is_not_fatal():
    broken = mock.Mock()
    type(broken).queues = mock.PropertyMock(side_effect=RuntimeError("boom"))
    with mock.patch.object(worker_mod.celery_app, "amqp", broken):
        assert _consumes_ocr_queue() is False


def test_worker_ready_preloads_only_on_the_ocr_worker():
    with mock.patch.object(worker_mod, "_consumes_ocr_queue", return_value=True), \
         mock.patch.object(worker_mod.settings, "ocr_preload_models", True), \
         mock.patch.object(worker_mod.threading, "Thread") as thread:
        _warm_models()
    thread.assert_called_once()
    assert thread.call_args.kwargs["target"] is worker_mod.preload_ocr_models
    # Daemon thread: --pool=solo must stay free to answer `inspect ping`
    # (the container healthcheck) during the ~99s load.
    assert thread.call_args.kwargs["daemon"] is True
    thread.return_value.start.assert_called_once()


def test_worker_ready_skips_non_ocr_workers():
    with mock.patch.object(worker_mod, "_consumes_ocr_queue", return_value=False), \
         mock.patch.object(worker_mod.settings, "ocr_preload_models", True), \
         mock.patch.object(worker_mod.threading, "Thread") as thread:
        _warm_models()
    thread.assert_not_called()


def test_preload_can_be_turned_off():
    with mock.patch.object(worker_mod, "_consumes_ocr_queue", return_value=True), \
         mock.patch.object(worker_mod.settings, "ocr_preload_models", False), \
         mock.patch.object(worker_mod.threading, "Thread") as thread:
        _warm_models()
    thread.assert_not_called()


def test_preload_failure_does_not_kill_the_worker():
    with mock.patch("app.models.ocr_model.load_surya",
                    side_effect=RuntimeError("no weights")):
        worker_mod.preload_ocr_models()  # must not raise
