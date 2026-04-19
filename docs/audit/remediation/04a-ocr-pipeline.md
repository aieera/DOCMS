# Remediation 04a — OCR pipeline end-to-end

**Date:** 2026-04-17
**Scope:** Connect the Celery OCR task to Postgres + NATS so extracted text
actually reaches the search index and triggers the rest of the intelligence
pipeline.
**Source finding:** `docs/audit/00-summary.md` item 10; `docs/audit/04-antipatterns.md` §pipeline.

---

## Before

- `services/intelligence/app/tasks/ocr.py` extracted page text and returned
  it from the Celery task result — nothing read that result.
- `ocr_results` table stayed empty even after thousands of uploads.
- `services/search/internal/service/indexer.go:onOCRCompleted` was already
  subscribed to `dms.version.ocr_completed.v1` but no producer ever
  published on that subject.
- Consequence: full-text search always missed scanned documents, and
  downstream classification / NER / embed / duplicate detection never
  fired (the consumer at `_on_ocr_completed` depends on that event too).

## After

- OCR task writes every page to `ocr_results` with RLS context set.
- Task emits a CloudEvents envelope on
  `dms.version.ocr_completed.v1` with the original upload's
  `correlation-id` propagated.
- Search service folds the text into OpenSearch
  (`indexer.go:onOCRCompleted` unchanged — it was always ready).
- Intelligence `_on_ocr_completed` fans the event out to classify / NER
  / embed / duplicate Celery jobs.
- Prometheus exposes OCR counters + latency histograms on `/metrics`.

---

## Sequence

```
 API client           storage           NATS             intelligence (Celery)        Postgres        search
     |                  |                 |                       |                       |              |
     | POST /uploads    |                 |                       |                       |              |
     |─────────────────▶|                 |                       |                       |              |
     |                  | upload + commit |                       |                       |              |
     |                  |───────────────▶ |  dms.version.uploaded.v1                      |              |
     |                  |                 |──────────────────────▶| _on_uploaded          |              |
     |                  |                 |                       | (consumer forwards    |              |
     |                  |                 |                       |  correlation-id)      |              |
     |                  |                 |                       |──enqueue─────────────▶| Celery queue |
     |                  |                 |                       |                       |              |
     |                  |                 |                       | process_ocr(...):     |              |
     |                  |                 |                       |  1. S3 download       |              |
     |                  |                 |                       |  2. PyMuPDF / Surya   |              |
     |                  |                 |                       |  3. _persist_pages ──▶| INSERT rows  |
     |                  |                 |                       |     (RLS tx)          | into         |
     |                  |                 |                       |                       | ocr_results  |
     |                  |                 |                       |  4. publish event    |              |
     |                  |                 |◀──────────────────────│                       |              |
     |                  |                 |  dms.version.ocr_completed.v1                 |              |
     |                  |                 |      (with correlation-id header)             |              |
     |                  |                 |──────────────────────▶| search indexer        |              |
     |                  |                 |                       |  onOCRCompleted ─────────────────────▶|
     |                  |                 |                       |   (OpenSearch PartialUpdate)          |
     |                  |                 |──────────────────────▶| intelligence consumer |              |
     |                  |                 |                       |  _on_ocr_completed    |              |
     |                  |                 |                       |  → classify + NER +   |              |
     |                  |                 |                       |    embed + duplicate  |              |
```

---

## Files changed

| File | Change |
|------|--------|
| `services/intelligence/app/tasks/ocr.py` | Rewrote `process_ocr` to persist each page to `ocr_results` inside an RLS-scoped transaction and publish a CloudEvent. Added per-page processing-time capture. Full text capped at 10 MiB (truncated on UTF-8 boundary). |
| `services/intelligence/app/nats_consumer.py` | `_on_uploaded` now extracts `correlation-id` from the NATS message header and forwards it through Celery kwargs. `_on_ocr_completed` now reads `data.content` (with `text` / `full_text` fallbacks for older envelopes). |
| `services/intelligence/app/main.py` | Added `/metrics` FastAPI route (Prometheus text format) and `close_pool()` on lifespan shutdown. |
| `services/intelligence/app/db/__init__.py`, `app/db/pool.py` | New — process-wide asyncpg pool used by Celery tasks. Lazy init, 1–4 connections, 30 s command timeout. |
| `services/intelligence/app/events/__init__.py`, `app/events/publisher.py` | New — short-lived JetStream publisher that forwards `correlation-id` as a NATS header. |
| `services/intelligence/app/metrics.py` | New — 4 Prometheus metrics (see below). |
| `services/intelligence/requirements.txt` | Added `asyncpg==0.29.0`, `prometheus-client==0.20.0`, `testcontainers[postgres]==4.7.2`. |
| `services/intelligence/tests/test_ocr_pipeline.py` | New — 4 unit tests + 2 integration tests (skipped unless Docker is up). |

---

## Event envelope

```json
{
  "specversion": "1.0",
  "id": "<uuid4>",
  "source": "dms.intelligence",
  "type": "dms.version.ocr_completed.v1",
  "subject": "version/<version_id>",
  "time": "<ISO-8601>",
  "datacontenttype": "application/json",
  "tenantid": "<tenant_id>",
  "correlationid": "<from upload event>",
  "data": {
    "tenant_id": "<uuid>",
    "document_id": "<uuid>",
    "version_id": "<uuid>",
    "page_count": 17,
    "language": "en",
    "confidence_avg": 0.94,
    "content":   "<full text, ≤10 MiB>",
    "full_text": "<same>",
    "text":      "<same>",
    "engine":    "surya"
  }
}
```

The triple-alias (`content` / `full_text` / `text`) is intentional:
- **`content`** is what `services/search/internal/service/indexer.go:170`
  reads — keep this one stable.
- `full_text` and `text` are kept for backward compatibility with the
  intelligence service's own `_on_ocr_completed` and any internal
  consumers that were written against earlier drafts of the event. The
  dispatcher tries `content` first, then falls back to the older keys,
  so future releases can drop the aliases.

Subject alignment confirmed by grep:

```
services/intelligence/app/nats_consumer.py:42: dms.version.ocr_completed.v1
services/intelligence/app/tasks/ocr.py:36:     dms.version.ocr_completed.v1
services/search/internal/service/indexer.go:54: dms.version.ocr_completed.v1
```

---

## Database write

Schema reminder (`services/document/migrations/000001_initial_schema.up.sql:739`):

- PK is `(tenant_id, id)` where `id` defaults to `gen_random_uuid()`.
- No `UNIQUE` constraint on `(tenant_id, version_id, page_number)` and
  no `updated_at` column.

`ON CONFLICT (tenant_id, version_id, page_number) DO UPDATE` from the
task prompt would therefore need a new migration. To stay self-contained,
the persist path is instead `DELETE → INSERT` inside one transaction —
same idempotency guarantee (Celery retry never leaves mixed-run rows),
no global schema change. A migration to add the unique index belongs in
a separate pass if read-time joins on `(version_id, page_number)` start
showing up in slow queries.

RLS is set per transaction:

```sql
SELECT set_config('app.current_tenant', $tenant_id, true);
DELETE FROM ocr_results WHERE tenant_id=$1 AND version_id=$2;
INSERT INTO ocr_results (tenant_id, id, version_id, page_number,
                         text_content, confidence, language,
                         bounding_boxes, processing_time_ms, engine, created_at)
VALUES ($1, gen_random_uuid(), $2, $3, $4, $5, $6, $7::jsonb, $8, $9, NOW());
```

---

## Error handling

| Failure | Behavior |
|---------|----------|
| S3 download fails, OCR engine crashes | `ocr_errors_total{error_type="task"}` incremented, Celery autoretry (max 2, exponential backoff). |
| `_persist_pages` throws | `ocr_errors_total{error_type="db_insert"}` incremented, exception re-raised → Celery retry. Event is **not** published. |
| `_publish_ocr_completed` fails | `ocr_publish_failed_total` incremented, exception swallowed, task returns `published=False`. The DB row is already committed, so a future reindex can still pick up the text. |
| MIME type isn't OCR-able | Task returns `{"status":"skipped"}` with no DB/NATS I/O. |

The publish-side behavior trades at-most-once event delivery for
durability of the side effect. A proper Python-side outbox is tracked
as a TODO — the Go outbox can't be reused as-is because the Celery
worker isn't the same process as the one holding the asyncpg pool
with the tenant GUC set during the business write. Deferred to
remediation 03d or later.

---

## Metrics

All four exposed on `/metrics` via the default Prometheus registry:

| Metric | Type | Labels | What it measures |
|--------|------|--------|------------------|
| `ocr_pages_processed_total` | Counter | `engine`, `language` | Pages successfully OCR'd. |
| `ocr_processing_seconds` | Histogram | `engine` | Per-page OCR wall time. Buckets cover PyMuPDF (<50 ms) through heavy Surya (up to 60 s). |
| `ocr_errors_total` | Counter | `error_type` | `db_insert` vs `task` (S3, engine, misc). |
| `ocr_publish_failed_total` | Counter | — | NATS publish failures after the DB commit. |

---

## Tests

`services/intelligence/tests/test_ocr_pipeline.py`:

- **Unit** (always runs):
  - `test_envelope_shape_matches_contract` — envelope has the required
    CloudEvents fields, the `data` block carries both `content` (for
    search) and `text` (for intelligence).
  - `test_publish_swallows_exceptions` — a failing NATS publish returns
    `False` instead of raising.
  - `test_publish_success_returns_true`.
  - `test_full_text_truncated_to_10mib`.

- **Integration** (skipped unless Docker is running):
  - `test_persist_pages_writes_rows` — spins a real Postgres via
    testcontainers, applies the `ocr_results` schema + RLS policies,
    runs `_persist_pages`, and reads back under the tenant GUC to
    confirm RLS admitted the rows.
  - `test_persist_is_idempotent` — running the persist path twice for
    the same version replaces rather than duplicates. Models a Celery
    retry after a transient NATS error.

The full 7-step verification from the task prompt (fresh upload →
psql check → NATS sub → search hit) can't be executed in this session
because Docker Desktop isn't running (same gate as remediation 09).
The code path is self-consistent:

- `py_compile` clean on every touched file.
- Event subject grep matches producer and consumer.
- Schema check confirms the RLS policies referenced by
  `_persist_pages` are the ones in `000001_initial_schema.up.sql`.

---

## DO-NOTs honored

- OCR engine selection untouched — the PyMuPDF→Surya fallback logic is
  the same as before.
- No new extraction types added; classify / NER / embed / duplicate
  remain their own tasks and continue to fire via
  `_on_ocr_completed` fan-out.
- Upload event schema not changed — the consumer still reads
  `tenant_id`, `document_id`, `version_id`, `content_blob_id`,
  `region_pin`, `storage_bucket`, `storage_key`, `mime_type`, plus the
  newly-threaded `correlation-id` header (which is additive — older
  producers that don't set the header still work).
