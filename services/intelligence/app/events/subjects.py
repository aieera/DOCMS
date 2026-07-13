"""Canonical registry of every NATS subject the intelligence service emits.

Single source of truth (Wave A follow-up to the shared-outbox jam, STATE
2026-07-03 §C): task code MUST import subject constants from here instead
of defining inline literals. Three guardrails enforce the contract:

1. tests/test_no_inline_subjects.py — AST lint failing any inline
   ``dms.*.vN`` string literal in app/tasks/ (docstrings exempt).
2. pkg/events/coverage_python_test.go (Go) — parses this file and fails
   the build if any constant here is not bound to a JetStream stream in
   DefaultStreams or missing from PublishedSubjects. That is the gate
   that would have caught dms.notification.send.v1 /
   dms.translation.completed.v1 / dms.training_example.collected.v1
   before they could wedge the shared outbox drain.
3. The same Go test also text-scans all Python sources, so even a rogue
   literal outside tasks/ still gets stream-coverage checking.

Adding a subject: declare it here, bind it in pkg/events/publisher.go
DefaultStreams, and add it to pkg/events.PublishedSubjects — the Go gate
fails until all three agree.
"""
from __future__ import annotations

# OCR / quality
OCR_COMPLETED_SUBJECT = "dms.version.ocr_completed.v1"
OCR_FAILED_SUBJECT = "dms.ocr.failed.v1"
OCR_QUALITY_COMPLETED_SUBJECT = "dms.ocr.quality.completed.v1"
OCR_RETRY_REQUESTED_SUBJECT = "dms.version.ocr_retry_requested.v1"

# Classification / tagging / routing / anomalies
ANOMALY_COMPLETED_SUBJECT = "dms.anomaly.completed.v1"
AUTOTAG_COMPLETED_SUBJECT = "dms.autotag.completed.v1"
CLASSIFY_COMPLETED_SUBJECT = "dms.classify.completed.v1"
ROUTING_COMPLETED_SUBJECT = "dms.routing.completed.v1"

# Compliance / redaction
COMPLIANCE_COMPLETED_SUBJECT = "dms.compliance.completed.v1"
NOTIFICATION_SUBJECT = "dms.notification.send.v1"
REDACTION_APPLIED_SUBJECT = "dms.redaction.applied.v1"
VERSION_UPLOADED_SUBJECT = "dms.version.uploaded.v1"

# NER / embeddings / language / extraction
EMBED_COMPLETED_SUBJECT = "dms.embed.completed.v1"
FIELDS_EXTRACTED_SUBJECT = "dms.version.fields_extracted.v1"
LANGUAGE_DETECTED_SUBJECT = "dms.language.detected.v1"
NER_COMPLETED_SUBJECT = "dms.ner.completed.v1"
TRANSLATION_COMPLETED_SUBJECT = "dms.translation.completed.v1"

# Ingestion
INGESTION_PROCESSED_SUBJECT = "dms.ingestion.processed.v1"

# Model lifecycle / training data
EVALUATED_SUBJECT = "dms.model.evaluated.v1"
PROMOTED_SUBJECT = "dms.model.promoted.v1"
MODEL_TRAINED_SUBJECT = "dms.model.trained.v1"
COLLECTED_SUBJECT = "dms.training_example.collected.v1"

# Billing (ADR 0081 — LLM usage metering)
BILLING_LLM_USAGE_SUBJECT = "dms.billing.llm.usage.v1"
