from __future__ import annotations
import logging
import threading

from celery import Celery, Task
from celery.signals import worker_ready

from app.config import settings

log = logging.getLogger(__name__)

OCR_QUEUE = "intelligence-ocr"

# H-16: task_acks_late + task_reject_on_worker_lost (below) re-queue a task
# whose worker died mid-run WITHOUT counting a retry, so a document that
# OOM-kills its worker replayed forever. Every start increments a Redis
# counter that only a clean exit (return or raise) clears: the counter is
# the number of starts that never finished. After MAX_WORKER_LOSSES the
# next delivery is dead-lettered instead of run.
MAX_WORKER_LOSSES = 3
_ATTEMPT_TTL_SECONDS = 24 * 3600
_attempt_client = None


def _attempt_redis():
    global _attempt_client
    if _attempt_client is None:
        import redis
        _attempt_client = redis.Redis.from_url(settings.redis_cache_url)
    return _attempt_client


def attempt_key(task_name: str, task_id: str) -> str:
    return f"celery:worker-lost:{task_name}:{task_id}"


class WorkerLossBoundedTask(Task):
    max_worker_losses = MAX_WORKER_LOSSES

    def __call__(self, *args, **kwargs):
        task_id = getattr(self.request, "id", None)
        key = attempt_key(self.name, task_id) if task_id else None
        if key:
            starts = None
            try:
                r = _attempt_redis()
                starts = int(r.incr(key))
                r.expire(key, _ATTEMPT_TTL_SECONDS)
            except Exception:  # noqa: BLE001 — never block work on the counter
                log.warning("worker-loss counter unavailable for %s", key, exc_info=True)
            if starts is not None and starts > self.max_worker_losses:
                self._clear(key)
                log.error("task %s[%s] lost its worker %d times; dead-lettering",
                          self.name, task_id, starts - 1)
                return self.on_worker_lost_exhausted(args, kwargs, starts - 1)
        try:
            return super().__call__(*args, **kwargs)
        finally:
            if key:
                self._clear(key)

    @staticmethod
    def _clear(key: str) -> None:
        try:
            _attempt_redis().delete(key)
        except Exception:  # noqa: BLE001
            log.warning("worker-loss counter clear failed for %s", key, exc_info=True)

    def on_worker_lost_exhausted(self, args, kwargs, losses: int):
        """Dead-letter hook; tasks override it to record a terminal state."""
        return {"status": "failed", "reason": "worker_lost",
                "error": f"worker lost {losses} times; dead-lettered"}


celery_app = Celery(
    "vaultdms-intelligence",
    broker=settings.celery_broker_url,
    backend=settings.celery_result_backend,
    task_cls=WorkerLossBoundedTask,
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
