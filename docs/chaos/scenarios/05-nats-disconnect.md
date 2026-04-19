# Chaos 05 — NATS disconnect

## Premise

Disconnect a storage pod from NATS for 60 seconds while
uploads are in flight. The transactional outbox must queue
events + drain on reconnect with zero loss.

## Setup

- Chaos-mesh CRD: `NetworkChaos` with `action: partition`,
  source: storage pod, target: NATS service.
- k6 scenario 03-upload at 20 uploads/min — modest rate so we
  can count outbox rows exactly.

## Verification

- Count `outbox` rows with `published = false` during the
  partition — monotonically increasing.
- After reconnect, outbox publisher drains: `published = false`
  count returns to 0 within 30 s.
- Every expected `dms.version.uploaded.v1` event lands in the
  `DOC_EVENTS` stream (check consumer lag + count).
- No 5xx user-visible. Upload-complete returns 200 because the
  outbox row landed in Postgres even if NATS is away.

## Expected behaviour

- `pkg/database.OutboxPublisher` accumulates pending rows;
  the `FOR UPDATE SKIP LOCKED` loop picks up the backlog once
  `js.Publish` stops erroring.
- CloudEvents `id` field dedupes any race with the
  retry-on-reconnect path — downstream consumers idempotent.

## Fail-the-test triggers

- Any user-visible 5xx during the partition window.
- Missing event in `DOC_EVENTS` for any successful upload.
- Outbox backlog doesn't drain within 30 s of reconnect.
- Duplicate event delivery without corresponding deduplication
  — consumer-side defense should handle, but a dup signals a
  publisher bug.
