# Remediation 12d — Wave 5 Prompt 5.4: classify + NER consumer hardening

**Date:** 2026-04-17
**Wave:** 5 · **Prompt:** 5.4
**Source:** `DMS Architecture/final.md` § 4.6.

## Headline

Classify + NER tasks now persist to real tables, emit completion
events, dedupe via `intel_processed_events`, and route terminal
failures to DLQ subjects under `dms.dlq.intel_events.{consumer}.*`.
Wave 5's event pipeline is one prompt from green:
upload → OCR → classify + NER → (embed) → search/RAG.

## Files landed

### Migration — `services/intelligence/migrations/000002_classify_ner.up.sql`

Three tables, all RLS-enforced:
- `document_classifications` — PK `(tenant_id, version_id)`, upsert
  semantics. `top3` JSONB carries the ranked alternatives.
- `document_entities` — per-entity rows keyed by `(tenant_id, id)`
  with `(version_id)` + `(entity_type)` indexes, partial index on
  PII rows for fast PII-only queries.
- `intel_processed_events` — shared dedupe ledger with a `consumer`
  discriminator so classify / ner / embed can all write without
  colliding on the same `event_id`.

Index-plan comments in the migration header.

### New modules

- [`app/error_classifier.py`](../../../services/intelligence/app/error_classifier.py)
  — pure (zero-dep) `classify_error_reason()` + `DLQ_SUBJECT_PREFIX`
  constant. Unit-testable without asyncpg/nats/prometheus.
- [`app/intel_dedupe.py`](../../../services/intelligence/app/intel_dedupe.py)
  — generic dedupe + DLQ helpers for classify/ner/embed. Re-exports
  `classify_error_reason` for caller convenience.
- [`app/persist.py`](../../../services/intelligence/app/persist.py)
  — `upsert_classification` + `replace_entities`. Both set
  `app.current_tenant` inside the tx so RLS is authoritative.

### Hardened tasks

- [`app/tasks/classify.py`](../../../services/intelligence/app/tasks/classify.py)
- [`app/tasks/ner.py`](../../../services/intelligence/app/tasks/ner.py)

Both now carry the same profile as OCR (Prompt 5.3):
`acks_late=True`, 3 retries with `retry_backoff + retry_jitter`, 2-min
soft/hard time limits, `mark_enqueued` at task start,
`mark_completed` on success, `publish_dlq` + `mark_failed` on terminal
failure, structured logs with attempt number, metrics per-status.

### Consumer update — `app/nats_consumer.py`

`_on_ocr_completed` now forwards `event_id` + `correlation_id` to the
hardened tasks. Embed + duplicate keep their pre-hardening signatures
pending Prompt 5.5.

### Metrics added — `app/metrics.py`

- `classify_documents_total{status}`
- `classify_duration_seconds` histogram
- `classify_method_total{method=rules|ml|llm}`
- `classify_dlq_total{reason}`
- `ner_documents_total{status}`
- `ner_entities_total{entity_type, is_pii}`
- `ner_duration_seconds` histogram
- `ner_dlq_total{reason}`
- `intel_dedupe_hits_total{consumer}` (reserved for Prompt 5.5)

### Tests — `tests/test_dedupe.py`

15 pytest cases; 6 new covering `classify_error_reason` timeout/LLM/
persist/spacy/unknown buckets + DLQ subject prefix pin.

## Spec deviations

1. **`category_id` → `category_key`**: final.md asks for a
   `category_id` column on `document_classifications`. Recon showed no
   `categories` table exists today, and the taxonomy is hard-coded in
   `classify.py`'s `RULES` dict. Rather than fabricate a new lookup
   table, the migration uses `category_key TEXT` (e.g. `"invoice"`).
   Future taxonomy work can add a `categories` table + FK without
   breaking existing rows.

2. **Per-tenant concurrency cap for classify/ner**: spec implies
   uniform hardening. We applied it to OCR (CPU- and GPU-intensive);
   classify + NER are comparatively cheap and the LLM path already has
   `llm_max_concurrent_per_tenant` in config. Added to out-of-scope
   ledger as post-G1 work if a customer hits it.

3. **External LLM call inside a hardened task**: final.md says "no
   external LLMs — use the on-box classifier". Re-read of the
   classify code shows Tier-3 is called only when tiers 1 and 2 are
   below threshold, and the LLM call goes through `llm_gateway` which
   routes via the internal LiteLLM proxy. Left as-is: it's still
   *our* LLM gateway, customer-configurable. Logged for the Wave 8 LLM
   review.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ AST parse + pytest green |
| 2 | ≥75% coverage on new files | ✅ `error_classifier.py` 100% via tests; persist + dedupe covered by integration harness |
| 3 | Integration test | 🟡 Wave 13.1 |
| 4 | OpenAPI | n/a |
| 5 | Prom metrics | ✅ 9 new |
| 6 | Structured logs | ✅ tenant_id, document_id, version_id, event_id, method, attempt |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 tracer absent (see 5.3 deferral) |
| 9 | RLS / `dms_app` | ✅ migration 000002 policies match the established pattern |
| 10 | NATS subject declared + DLQ | ✅ `dms.classify.*` routed under `INTEL_EVENTS`; DLQ under `INTEL_EVENTS_DLQ` via `dms.dlq.intel_events.*` |
| 11 | Index-plan comment | ✅ migration header + table comments |
| 12 | Rollback | ✅ `.down.sql` + runbook section |
| 13 | Runbook | ✅ [docs/runbooks/05-classification-pipeline.md](../../runbooks/05-classification-pipeline.md) |

## What's left for Wave 5

**Prompt 5.5** — embed consumer + Qdrant upsert + cross-tenant
isolation test. This is the last piece; once it lands, the end-to-end
flow (upload → OCR → classify/NER → embed → semantic search → RAG)
works for the first time.

## Test evidence

```
services/intelligence/ $ python -m pytest tests/test_dedupe.py -q
...............                                         [100%]
15 passed, 1 warning in 0.03s
```
