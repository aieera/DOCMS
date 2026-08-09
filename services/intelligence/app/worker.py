from __future__ import annotations
import logging
import threading

from celery import Celery
from celery.signals import worker_ready

from app.config import settings

log = logging.getLogger(__name__)

OCR_QUEUE = "intelligence-ocr"

celery_app = Celery(
    "vaultdms-intelligence",
    broker=settings.celery_broker_url,
    backend=settings.celery_result_backend,
    include=[
        "app.tasks.ocr",
        "app.tasks.ingest",
        "app.tasks.classify",
        "app.tasks.extract",
        "app.tasks.ner",
        "app.tasks.embed",
        "app.tasks.duplicate",
        "app.tasks.auto_tag",
        "app.tasks.smart_route",
        "app.tasks.compliance_scan",
        "app.tasks.lang_detect",
        "app.tasks.translate",
        "app.tasks.ocr_quality",
        "app.tasks.anomaly_detect",
        "app.tasks.training_collector",
        "app.tasks.model_retrain",
        "app.tasks.model_evaluate",
        "app.tasks.rag",
        "app.tasks.summarize",
        "app.tasks.redact",
        # ADR 0104 Phase 2 — clause detection. Without this include the
        # embed→detect chain dies at dispatch: the worker logs a
        # KeyError for the unregistered task name and detection never
        # runs (found dead in the Task 6 live e2e).
        "app.tasks.clause_match",
    ],
)

celery_app.conf.update(
    task_serializer="json",
    result_serializer="json",
    accept_content=["json"],
    timezone="UTC",
    enable_utc=True,
    task_time_limit=900,
    task_soft_time_limit=840,
    worker_concurrency=2 if settings.ocr_gpu else 4,
    worker_max_memory_per_child=4_096_000,
    task_acks_late=True,
    task_reject_on_worker_lost=True,
    task_default_queue="intelligence",
    task_routes={
        "app.tasks.ocr.process_ocr": {"queue": "intelligence-ocr"},
        # WS3 pre-commit ingestion reuses the OCR engine, so it runs on the
        # OCR worker pool (where the models are loaded).
        "app.tasks.ingest.process_ingestion": {"queue": "intelligence-ocr"},
        "app.tasks.embed.generate_embeddings": {"queue": "intelligence-embed"},
        "app.tasks.rag.*": {"queue": "intelligence-rag"},
    },
)


def _consumes_ocr_queue() -> bool:
    """True when this worker was started with the OCR queue selected.

    `-Q` narrows `app.amqp.queues` to the selected set, so the misc worker
    (which deliberately excludes `intelligence-ocr` so a 30-minute Surya
    run can't block classify/embed) never pays the 2 GB model load.
    """
    try:
        return OCR_QUEUE in set(celery_app.amqp.queues)
    except Exception:  # noqa: BLE001 — introspection must never block boot
        log.exception("could not resolve consumed queues; skipping OCR preload")
        return False


def preload_ocr_models() -> None:
    """Warm the Surya weights so the first document doesn't pay for them.

    Runs on a daemon thread: with --pool=solo the consumer thread must stay
    free to answer `celery inspect ping` (the container healthcheck) while
    the ~99s load runs. load_surya() takes a lock, so a task that arrives
    mid-load simply waits for the same instance instead of loading twice.
    """
    try:
        from app.models.ocr_model import load_surya
        log.info("preloading surya OCR models at worker startup")
        load_surya()
        log.info("surya OCR models preloaded")
    except Exception:  # noqa: BLE001 — a failed warm-up must not kill the worker
        log.exception("OCR model preload failed; falling back to lazy load")


@worker_ready.connect
def _warm_models(**_kwargs) -> None:
    if not settings.ocr_preload_models or not _consumes_ocr_queue():
        return
    threading.Thread(
        target=preload_ocr_models, name="ocr-model-preload", daemon=True,
    ).start()
