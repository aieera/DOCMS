from __future__ import annotations
from celery import Celery
from app.config import settings

celery_app = Celery(
    "vaultdms-intelligence",
    broker=settings.celery_broker_url,
    backend=settings.celery_result_backend,
    include=[
        "app.tasks.ocr",
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
        "app.tasks.rag",
        "app.tasks.summarize",
        "app.tasks.redact",
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
        "app.tasks.embed.generate_embeddings": {"queue": "intelligence-embed"},
        "app.tasks.rag.*": {"queue": "intelligence-rag"},
    },
)
