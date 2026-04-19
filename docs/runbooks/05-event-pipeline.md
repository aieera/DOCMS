# Runbook — Event pipeline (Wave 5)

**Last rehearsed:** not yet (Wave 13 chaos drill will cover this).
**On-call:** document-platform team.

## What this runbook covers

The `dms.version.uploaded.v1` event is the canonical trigger for the
intelligence pipeline: OCR → classification → NER → embedding →
Qdrant upsert → search reindex → preview thumbnail. If this event
fails to emit, or consumers fail to drain it, uploaded documents
never become searchable.

## Where the event comes from

Per **ADR 0021**, the event is emitted inside the `CreateVersion`
transaction in the **document** service (`services/document/internal/service/documents.go`).
`storage.CompleteUpload` only writes the blob; it does NOT emit this
event.

Subject on the wire: `dms.version.uploaded.v1` (includes `.v1`).
Stream binding: `DOC_EVENTS` — see Prompt 5.2 for the authoritative
stream topology.

## Metrics to watch

- `document_events_published_total{subject="dms.version.uploaded.v1"}`
  — counts outbox inserts. Should climb 1:1 with successful uploads.
- `outbox_publish_latency_seconds` (from `pkg/database`) — time from
  outbox insert to NATS ack. Alert if p95 > 5s over 10m.
- `<consumer>_events_processed_total{subject="dms.version.uploaded.v1"}`
  — per consumer. If the document-side counter climbs but a consumer
  doesn't, the consumer is stuck, not the publisher.

## Common failure modes

### Upload completes but OCR never runs

1. Check the outbox: `SELECT count(*) FROM outbox WHERE event_type = 'dms.version.uploaded.v1' AND published = false;` — if rows are accumulating, the outbox publisher is stuck or NATS is down.
2. If rows drain but OCR idle: `nats consumer info DOC_EVENTS intel-uploaded` — check pending/ack counts and any `num_redelivered` spikes.
3. If consumer shows no messages: confirm stream `DOC_EVENTS` actually binds `dms.version.>`. Fix via Prompt 5.2's bootstrap if missing.

### `lookup blob uri` errors in CreateVersion logs

The same-tx `SELECT … FROM content_blobs` failed. Usually means the
caller passed a `content_blob_id` that doesn't exist for the tenant
(client bug) or RLS blocked the read (role mis-set). Check
`app.current_tenant` is set inside the tx; check the client's
CompleteUpload actually returned the blob id you're passing to
CreateVersion.

### `storage_uri` is empty in the event

Means the content blob had empty `storage_bucket` or `storage_key`.
Storage service's CompleteUpload must populate both; the schema enforces
NOT NULL, so an empty value indicates a default-string write. Check
the storage service's scan-result handling path; quarantined blobs may
skip the normal bucket path.

## What can go wrong and how to fix

| Symptom | First check | Fix |
|---|---|---|
| Upload 200 OK but doc never appears in search | outbox row published=false | Restart outbox publisher; check NATS conn |
| Event published, OCR silent | `nats consumer info DOC_EVENTS intel-uploaded` | Restart intelligence; check Python worker logs |
| Event payload missing fields | `document_events_published_total` climbing; consumer ACK-then-error | Inspect payload of latest outbox row; check `CreateVersion` logs for `lookup blob uri` errors |
| Duplicate OCR runs | Consumer dedupe table `ocr_processed_events` | Wave 5.3 adds this; until then dedup is weak |

## Escalation

- P1: event not flowing for > 5 minutes in prod → page document-platform oncall.
- P0: cross-tenant data leak indicator in event payloads (e.g. wrong `tenant_id`) → page security + document-platform, halt ingestion.

## Rollback

Wave 5 Prompt 5.1 renamed the event from `dms.version.created.v1` to
`dms.version.uploaded.v1`. If a consumer hard-breaks on the new
subject and needs the old one back:

1. Revert `services/document/internal/service/documents.go` CreateVersion emit to the old subject + payload.
2. Redeploy document service.
3. Any `.v1.uploaded` events already in DOC_EVENTS stay in the stream until retention expires; they won't harm reverted consumers because those consumers weren't listening for them in the first place.
