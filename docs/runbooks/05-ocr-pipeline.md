# Runbook — OCR pipeline hardening (Wave 5 Prompt 5.3)

**Last rehearsed:** not yet (chaos drill in Wave 13).
**On-call:** intelligence-platform team.

## What this covers

The OCR consumer subscribes to `dms.version.uploaded.v1` on `DOC_EVENTS`
and enqueues the `process_ocr` Celery task. Wave 5 Prompt 5.3 hardened
it with:

- **Dedupe table** `ocr_processed_events (tenant_id, event_id)` with 14-day
  retention. Redeliveries on `completed` events are dropped silently.
- **Per-tenant semaphore** capped at `VAULTDMS_OCR_PER_TENANT_CAP`
  (default 8). Prevents one tenant from starving others on a single
  pod.
- **acks_late** Celery setting so a worker crash mid-run triggers a
  broker redelivery rather than silent loss.
- **3 retries with jittered exponential backoff** (base 5s,
  `retry_jitter=True`). Failed enqueue path also surfaces in dedupe
  `attempts` count.
- **30-minute task hard limit** (`time_limit=1830`) with soft limit at
  1800s so `finally:` cleanup still runs.
- **DLQ routing** on terminal failure to
  `dms.dlq.intel_events.ocr.<reason>` where `reason ∈ {timeout,
  s3_error, engine_error, persist_error, poisoned}`.

## Metrics to watch

| Metric | Type | Alert when |
|---|---|---|
| `ocr_documents_total{status="completed"}` | Counter | rate drops below 90% of incoming `dms.version.uploaded.v1` rate for 10m |
| `ocr_documents_total{status="failed"}` | Counter | > 5% of total for 5m |
| `ocr_documents_total{status="deduped"}` | Counter | spike implies redelivery storm — investigate NATS |
| `ocr_duration_seconds` | Histogram | p95 > 300s (5 min) |
| `ocr_pages_total` | Counter | throughput check; compare against expected pages/min |
| `ocr_queue_depth{tenant_id}` | Gauge | saturated at cap for a single tenant > 5m = noisy neighbor |
| `ocr_dlq_total{reason}` | Counter | any non-zero rate → page on-call |
| `ocr_dedupe_hits_total` | Counter | high baseline (>10%) = broker instability |

## Common failure modes

### Upload succeeds but OCR never fires

1. **Stream empty?** `./dms-admin nats list` — is `DOC_EVENTS` receiving
   messages? If zero messages, Prompt 5.1's publisher is stuck; see
   [05-event-pipeline.md](05-event-pipeline.md).
2. **Consumer not subscribed?** `nats consumer info DOC_EVENTS intel-uploaded`
   — check `num_pending`, `delivered.consumer_seq`.
3. **Dedupe false-positive?** Check if rows exist in
   `ocr_processed_events` with `status='completed'` for this
   `event_id`. True dedup hits are fine; a bug that marks completion
   prematurely would cause silent drops.

### OCR retrying forever, no progress

- Check the worker pod logs for the specific exception. Classify the
  failure reason:
  - `s3_error` — object missing, bucket perm gone. Fix storage, retry
    will pick up.
  - `engine_error` — Surya/Paddle crash. Check GPU/model load.
  - `persist_error` — Postgres failing. Check pool, RLS, migration 001.
  - `timeout` — task hit 30m wall clock. Check document size + GPU load.
  - `poisoned` — malformed payload. Message was `term()`'d at
    consumer; check the specific event at the DLQ.

### DLQ is growing

```bash
./dms-admin nats list | grep INTEL_EVENTS_DLQ
./dms-admin nats replay --stream INTEL_EVENTS_DLQ --count 50
```

Every DLQ entry has `{original_event_id, reason, error, attempts}`.
Triage by reason:

- **Bulk same reason** → systemic (e.g. all `engine_error` = model
  crash). Fix engine, then requeue DLQ contents by republishing
  payloads to `dms.version.uploaded.v1`.
- **Mixed reasons** → per-document issues. Likely poisoned PDFs; skip.

### Per-tenant starvation

If `ocr_queue_depth{tenant_id=X}` pegs at 8 and other tenants' queues
are idle:

```bash
# Bump cap for that tenant (until we ship per-tenant overrides).
# Global cap via:
VAULTDMS_OCR_PER_TENANT_CAP=16
# Restart consumer pods.
```

Better long-term: a Temporal workflow that deprioritizes the noisy
tenant. Out of scope until a customer actually hits this.

## GC

`ocr_processed_events` rows older than 14 days are deleted by a
nightly job (implementation deferred to Wave 8.1 — Temporal cron).
Until then, operators should run manually:

```sql
DELETE FROM ocr_processed_events
 WHERE processed_at < now() - INTERVAL '14 days';
```

## Rollback

If Prompt 5.3 introduces a regression:

1. Revert the three files: `app/nats_consumer.py`, `app/tasks/ocr.py`,
   `app/metrics.py` (only the Wave 5.3 additions).
2. Leave `ocr_processed_events` table in place — removing it is a
   separate migration (`migrations/000001_ocr_processed_events.down.sql`).
3. Clear any stuck dedupe rows so redeliveries resume:
   ```sql
   UPDATE ocr_processed_events SET status='enqueued' WHERE status='completed' AND completed_at > now() - interval '1h';
   ```

## Known deferred items (logged in out-of-scope.md)

- OTEL spans per page (we log + count, no tracer config yet).
- Per-tenant cap override in DB (global env var only).
- `gc_processed_events` cron implementation (Wave 8.1).
- DLQ auto-replay after N minutes (manual only for now).
