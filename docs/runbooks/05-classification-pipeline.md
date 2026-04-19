# Runbook — classification + NER pipeline (Wave 5 Prompt 5.4)

**Last rehearsed:** not yet.
**On-call:** intelligence-platform team.

## What this covers

After OCR emits `dms.version.ocr_completed.v1`, the intelligence
consumer enqueues two hardened Celery tasks in parallel: `classify_document`
(3-tier rules → ML → LLM) and `detect_entities` (SpaCy + regex PII).
Each persists its result + publishes a `*.completed.v1` event.

### Completion events

- `dms.classify.completed.v1` — payload: `category`, `confidence`,
  `method` (rules|ml|llm), `model_version`, `top_3_categories`,
  `document_class` (alias for backwards compat).
- `dms.ner.completed.v1` — payload: `entity_count`, `pii_count`,
  `entity_types`, `model_version`. Actual entity rows live in
  `document_entities` keyed by `(tenant_id, version_id)`.

## Tables

- `document_classifications(tenant_id, version_id, document_id, category_key, confidence, method, model_version, top3, classified_at)` — PK `(tenant_id, version_id)`, upsert-on-conflict (last classification wins).
- `document_entities(tenant_id, id, version_id, document_id, entity_type, entity_value, start_offset, end_offset, confidence, is_pii, detected_at)` — re-extraction deletes then re-inserts within one tx.
- `intel_processed_events(tenant_id, consumer, event_id, ...)` — dedupe ledger, shared across classify/ner/embed.

## Metrics

| Metric | Type | Alert on |
|---|---|---|
| `classify_documents_total{status}` | Counter | failed rate > 5% for 5m |
| `classify_duration_seconds` | Histogram | p95 > 30s |
| `classify_method_total{method}` | Counter | LLM share jumps 2× baseline (cost spike) |
| `classify_dlq_total{reason}` | Counter | any non-zero rate |
| `ner_documents_total{status}` | Counter | same |
| `ner_entities_total{entity_type, is_pii}` | Counter | trend-only |
| `ner_duration_seconds` | Histogram | p95 > 10s |
| `ner_dlq_total{reason}` | Counter | any non-zero rate |

## Common failure modes

### Classification stuck returning 'other' with confidence 0

Tier-1 rules matched zero keywords; tier-2 ML failed silently;
tier-3 LLM failed silently. Check the warning logs in the worker for
`tier2 ML failed` / `tier3 LLM failed`. Most common cause: LLM gateway
credentials unset → fallback to rules-only.

### `persist_error` in DLQ

The task reached the upsert but Postgres rejected it. Check:
1. Migration 000002 actually ran. `SELECT * FROM pg_tables WHERE tablename='document_classifications';`
2. `app.current_tenant` is being set by the task's persist helper — it is, but if the pool is mis-configured with wrong role, RLS will reject silently.

### PII counts suddenly jump

Could mean a new test tenant inadvertently uploaded real PII. Check
`ner_entities_total{is_pii="true"}` by tenant in Grafana (label
cardinality is low — 6 entity types). If legitimate, no action. If
accidental, trigger DSR erase.

## Rollback

1. Revert `tasks/classify.py`, `tasks/ner.py`, `nats_consumer.py` to
   pre-Prompt-5.4 versions.
2. Tables `document_classifications`, `document_entities`,
   `intel_processed_events` can stay — benign if unused.
3. Consumers that subscribe to `dms.classify.completed.v1` or
   `dms.ner.completed.v1` will stop receiving events; none exist today,
   so no impact.

## Known deferred

- External LLM OTEL spans (no tracer configured).
- Per-tenant concurrency cap for classify/ner (only OCR has it today).
- Category-ID taxonomy (spec wanted `category_id` FK; we use
  `category_key` text slug — documented in remediation).
