"""Celery app — shared between the worker entrypoint and tasks."""
from __future__ import annotations

from celery import Celery

from app.config import settings

celery_app = Celery(
    "vaultdms-preview",
    broker=settings.celery_broker_url,
    backend=settings.celery_result_backend,
    include=["app.tasks.preview", "app.tasks.video"],
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
