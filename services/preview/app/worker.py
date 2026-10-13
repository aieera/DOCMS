"""Celery app — shared between the worker entrypoint and tasks."""
from __future__ import annotations

import logging

from celery import Celery, Task

from app.config import settings

log = logging.getLogger(__name__)

# H-16: task_acks_late + task_reject_on_worker_lost (below) re-queue a task
# whose worker died mid-run WITHOUT counting a retry. A task that OOM-kills
# its worker (image bomb, giant page) therefore replayed forever. Every
# start increments a Redis counter that only a clean exit (return or
# raise) clears, so the counter is the number of starts that never
# finished; after MAX_WORKER_LOSSES the next delivery is dead-lettered.
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
        return {"status": "failed", "error": f"worker lost {losses} times; dead-lettered"}


celery_app = Celery(
    "vaultdms-preview",
    broker=settings.celery_broker_url,
    backend=settings.celery_result_backend,
    include=["app.tasks.preview", "app.tasks.video"],
    task_cls=WorkerLossBoundedTask,
)

celery_app.conf.update(
    task_serializer="json",
    result_serializer="json",
    accept_content=["json"],
    timezone="UTC",
    enable_utc=True,

    # Time limits — soft raises SoftTimeLimitExceeded so the task can clean
    # up; hard kills the worker process.
    task_time_limit=600,
    task_soft_time_limit=540,

    # Worker runtime controls.
    worker_concurrency=4,
    worker_max_memory_per_child=2_048_000,  # KB, per celery convention

    # Re-queue in-flight tasks if a worker dies mid-execution. The tasks
    # themselves are idempotent (keyed by (tenant, document, version)), so
    # re-delivery is safe.
    task_acks_late=True,
    task_reject_on_worker_lost=True,

    task_default_queue="preview",
    task_routes={
        "app.tasks.video.generate_hls": {"queue": "preview-video"},
    },
    task_annotations={
        "*": {
            "max_retries": 3,
            "default_retry_delay": 5,
        },
    },
)
