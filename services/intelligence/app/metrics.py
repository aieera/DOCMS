"""Prometheus metrics for the intelligence service.

Counters and histograms are process-global and safe to share across
Celery tasks (prometheus_client uses atomic operations under the hood).
The FastAPI `/metrics` route scrapes the default registry.
"""
from __future__ import annotations

from prometheus_client import Counter, Gauge, Histogram

ocr_pages_processed_total = Counter(
    "ocr_pages_processed_total",
    "OCR pages successfully processed.",
    ["engine", "language"],
)

ocr_processing_seconds = Histogram(
    "ocr_processing_seconds",
    "Per-page OCR processing time.",
    ["engine"],
    # Buckets chosen to cover fast PyMuPDF extraction (<50ms) through
    # heavy Surya passes (up to ~30s for dense scans).
    buckets=(0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 20.0, 30.0, 60.0),
)

ocr_errors_total = Counter(
    "ocr_errors_total",
    "OCR task failures, labeled by error type.",
    ["error_type"],
)

ocr_publish_failed_total = Counter(
    "ocr_publish_failed_total",
    "NATS publishes that failed after a successful ocr_results insert.",
)

# --- Wave 5 Prompt 5.3 hardening metrics ---------------------------------

ocr_documents_total = Counter(
    "ocr_documents_total",
    "OCR runs at the document level, labelled by outcome.",
    ["status"],  # completed | failed | deduped | skipped
)

ocr_pages_total = Counter(
    "ocr_pages_total",
    "Total pages OCR'd across all documents (raw count for throughput tracking).",
)

ocr_duration_seconds = Histogram(
    "ocr_duration_seconds",
    "Whole-document OCR wall-clock time, end-to-end from Celery pickup to "
    "ocr_completed event publish.",
    buckets=(1, 5, 10, 30, 60, 120, 300, 600, 1200, 1800),
)

ocr_queue_depth = Gauge(
    "ocr_queue_depth",
    "In-flight OCR tasks held by the per-tenant semaphore. Approximates "
    "broker queue depth for a single-worker-pod view.",
    ["tenant_id"],
)

ocr_dlq_total = Counter(
    "ocr_dlq_total",
    "OCR events routed to the DLQ after exhausting retries.",
    ["reason"],  # timeout | s3_error | engine_error | persist_error | poisoned
)

ocr_dedupe_hits_total = Counter(
    "ocr_dedupe_hits_total",
    "Redelivered events dropped by the dedupe table.",
)

# --- Wave 5 Prompt 5.4 — classify + NER hardening ------------------------

classify_documents_total = Counter(
    "classify_documents_total",
    "Classification runs labelled by outcome.",
    ["status"],  # completed | failed | deduped
)

classify_duration_seconds = Histogram(
    "classify_duration_seconds",
    "Whole-document classification wall clock.",
    buckets=(0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0),
)

classify_method_total = Counter(
    "classify_method_total",
    "Which tier won the classification decision.",
    ["method"],  # rules | ml | llm
)

classify_dlq_total = Counter(
    "classify_dlq_total",
    "Classification events routed to DLQ.",
    ["reason"],
)

ner_documents_total = Counter(
    "ner_documents_total",
    "NER runs labelled by outcome.",
    ["status"],  # completed | failed | deduped
)

ner_entities_total = Counter(
    "ner_entities_total",
    "Entities detected, summed across all runs.",
    ["entity_type", "is_pii"],
)

ner_duration_seconds = Histogram(
    "ner_duration_seconds",
    "Whole-document NER wall clock.",
    buckets=(0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0),
)

ner_dlq_total = Counter(
    "ner_dlq_total",
    "NER events routed to DLQ.",
    ["reason"],
)

intel_dedupe_hits_total = Counter(
    "intel_dedupe_hits_total",
    "Redelivered events dropped by classify/ner/embed dedupe.",
    ["consumer"],
)

# --- Wave 5 Prompt 5.5 — embed hardening ---------------------------------

embed_documents_total = Counter(
    "embed_documents_total",
    "Embed runs labelled by outcome.",
    ["status"],  # completed | failed | deduped | skipped
)

embed_chunks_total = Counter(
    "embed_chunks_total",
    "Total chunks embedded and upserted to Qdrant.",
)

embed_duration_seconds = Histogram(
    "embed_duration_seconds",
    "Whole-document embedding + upsert wall clock.",
    buckets=(0.5, 1, 2.5, 5, 10, 30, 60, 120, 300),
)

embed_dlq_total = Counter(
    "embed_dlq_total",
    "Embed events routed to DLQ.",
    ["reason"],
)
