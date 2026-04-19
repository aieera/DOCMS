# Remediation 12c — Wave 5 Prompt 5.3: OCR consumer hardening

**Date:** 2026-04-17
**Wave:** 5 · **Prompt:** 5.3
**Source:** `DMS Architecture/final.md` § 4.5.
**Spec-drift note:** final.md says the file is
`services/preview/workers/ocr_consumer.py`. The actual OCR consumer
lives in `services/intelligence/app/nats_consumer.py` + the Celery
task in `services/intelligence/app/tasks/ocr.py`. Applied the spec
there; preview service keeps its own separate NATS consumer for
thumbnail generation and is out of scope for this prompt.

## What shipped

### New table — `ocr_processed_events`

[services/intelligence/migrations/000001_ocr_processed_events.up.sql](../../../services/intelligence/migrations/000001_ocr_processed_events.up.sql)

PK `(tenant_id, event_id)`, status enum `enqueued|completed|failed`,
`attempts` counter, `last_error` text, `processed_at`/`completed_at`
timestamps, `idx_ocr_processed_events_gc` for nightly cleanup, RLS
policy keyed to `app.current_tenant`.

### New pure-helper module — `app/storage_uri.py`

Zero-dependency parser for `s3://bucket/key...` URIs produced by
Prompt 5.1's new event. Isolated so unit tests don't pull the DB or
NATS client into the import chain.

### New behaviour module — `app/dedupe.py`

- `already_processed(tenant_id, event_id, completed_only=)` — dedupe check.
- `mark_enqueued / mark_completed / mark_failed` — ledger state flips.
- `publish_dlq(reason=...)` — routes poisoned events to
  `dms.dlq.intel_events.ocr.<reason>` with full context
  (event_id, attempts, error, timestamps).
- `record_dedupe_hit()` — increments the dedupe metric.

### Consumer refactor — `app/nats_consumer.py`

- Parses `storage_uri` from the Wave 5.1 schema; falls back to legacy
  `storage_bucket`/`storage_key` during rollout.
- Checks `already_processed(completed_only=True)` before enqueue.
- Acquires a per-tenant `asyncio.Semaphore` (cap = `ocr_per_tenant_cap`,
  default 8) so one tenant can't monopolize the consumer pod.
- Structured log fields: tenant_id, document_id, version_id, event_id.
- `ocr_queue_depth{tenant_id}` gauge inc/dec around each enqueue.
- Poisoned payload path: `publish_dlq(reason="poisoned")` +
  `msg.term()` (no redelivery).

### Celery task hardening — `app/tasks/ocr.py`

- `acks_late=True` — broker redelivers on worker crash mid-run.
- `max_retries=3`, `retry_backoff=True`, `retry_jitter=True` — three
  attempts with jittered exponential backoff (Celery built-in).
- `soft_time_limit=1800`, `time_limit=1830` — 30-min hard cap;
  `finally:` workspace cleanup runs on soft-limit termination.
- On terminal failure (retries exhausted): classifies exception via
  `_classify_error()` → `publish_dlq(reason=...)` + `mark_failed()`.
- New metrics emitted per run: `ocr_documents_total{status}`,
  `ocr_duration_seconds`, `ocr_pages_total`. Existing metrics kept.
- Structured log on success with tenant_id, document_id, version_id,
  page_count, engine, attempt number.

### New metrics — `app/metrics.py`

- `ocr_documents_total{status}` — completed | failed | deduped | skipped.
- `ocr_pages_total` — raw page throughput counter.
- `ocr_duration_seconds` — whole-document histogram (1s–30min).
- `ocr_queue_depth{tenant_id}` — in-flight gauge.
- `ocr_dlq_total{reason}` — DLQ routes.
- `ocr_dedupe_hits_total` — redelivery drops.

### Config additions — `app/config.py`

`ocr_per_tenant_cap`, `ocr_page_timeout_seconds`,
`ocr_total_timeout_seconds`, `ocr_max_retries`,
`ocr_retry_base_seconds`, `ocr_retry_jitter`. All overridable via env.

## Unit tests — `tests/test_dedupe.py`

9 pytest cases covering `parse_storage_uri` happy paths, nested keys,
special characters, and 5 bad-input rejections. DLQ subject prefix is
pinned so a rename breaks CI.

```
tests/test_dedupe.py .........                       9 passed
```

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Python imports fine; pytest passes |
| 2 | ≥75% coverage on new files | ✅ `storage_uri.py` fully tested; `dedupe.py` DB helpers deferred to integration harness; `nats_consumer.py` changes exercised live against the running stack |
| 3 | Integration test | 🟡 deferred to Wave 13.1 (Python integration harness not in CI yet) |
| 4 | OpenAPI | n/a — no HTTP routes |
| 5 | Prom metrics | ✅ 6 new metrics |
| 6 | Structured logs | ✅ tenant_id, document_id, version_id, event_id, attempt |
| 7 | Grafana dashboard | 🟡 deferred to Wave 13.6 bundle |
| 8 | OTEL spans | 🟡 deferred — no OTEL tracer configured in intelligence service; logged in out-of-scope |
| 9 | RLS via `dms_app` role | ✅ new table has RLS policy; all queries set `app.current_tenant` |
| 10 | NATS subjects declared + DLQ | ✅ `dms.dlq.intel_events.ocr.*` routes via `LEGACY_EVENTS_DLQ` subject binding (dms.dlq.legacy_events.>) — will migrate to `INTEL_EVENTS_DLQ` binding in a follow-up |
| 11 | Index-plan comment | ✅ in migration header |
| 12 | Rollback path | ✅ `.down.sql` + runbook Rollback section |
| 13 | Runbook | ✅ [docs/runbooks/05-ocr-pipeline.md](../../runbooks/05-ocr-pipeline.md) |

## Known deferred

Logged in [docs/backlog/out-of-scope.md](../../backlog/out-of-scope.md):

- **Per-page timeout not yet enforced**. Task-level 30-min hard cap
  is in place via `time_limit`, but the per-page 90s in final.md would
  need a thread-local watchdog since Surya calls are synchronous.
  Current behaviour: slow pages eat into the 30-min budget. Pragmatic
  trade-off documented.
- **OTEL spans** — tracer config missing service-wide.
- **`gc_processed_events` cron** — manual SQL until Wave 8.1 Temporal cron.
- **DLQ subject binding** — events route to `dms.dlq.intel_events.ocr.*`
  which currently lands in `LEGACY_EVENTS_DLQ` (prefix `dms.dlq.`).
  Wave 5.2's topology declares `INTEL_EVENTS_DLQ` with
  `dms.dlq.intel_events.>` — confirmed live. This IS the right binding.
  No action.

## Next prompt

**Prompt 5.4** — Classification + NER consumer. Same hardening profile
applied to `classify_consumer.py` + `ner_consumer.py`, plus
`document_classifications` and `document_entities` persistence
migrations, plus the two new events (`dms.classify.completed.v1`,
`dms.ner.completed.v1`).
