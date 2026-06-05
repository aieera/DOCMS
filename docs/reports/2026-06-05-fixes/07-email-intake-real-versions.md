# 07 — email + intake create real document versions (OCR'd, searchable)

**Commit:** `164a32c` · **Type:** feature + 2 pre-existing fixes · **Date:** 2026-06-05

## Summary

Email ingestion (ADR 0087) and watched-folder intake (ADR 0088) previously
created document **rows** with no version blob — so ingested files never got a
version, never fired `dms.version.uploaded.v1`, and were invisible in the UI /
never OCR'd / embedded / indexed. Routed both through the shared server-side
ingest pipeline (the one Drive import uses) so each file + email body +
attachment becomes a **real document-with-version**.

## Feature change

- `ingest.Client` gained an **internal-service auth mode**: when called without a
  session token it presents `SEDOC_INTERNAL_API_KEY` + the acting identity in
  headers (email/intake are autonomous workers with no user session; they act as
  the config owner / folder creator). Drive import keeps the session-cookie path.
- **intake**: `handleFile` now calls `ingest.IngestFile` (was the row-only
  `MaterialiseFile`).
- **email**: `persistEnvelopes` records pending rows in the tx, then materialises
  body (rendered Markdown) + attachments **outside** the tx — so the heavy
  storage gRPC + presigned-PUT round-trips don't hold a DB connection.
- The dead `email/document_client.go` (257 lines) was removed.

## Two pre-existing bugs this surfaced + fixed

### Bug A — Missing JetStream streams jammed the shared outbox
`pkg/events/publisher.go` had **no stream** for `dms.storage.>` (storage emits 4
upload events) nor `dms.language.>` (lang_detect emits, once repaired). Unbound
subjects fail with "no response from stream", and the **shared** outbox publisher
(`FROM outbox WHERE NOT published ORDER BY created_at`, no service filter)
**halts the whole batch** on a publish error → permanently jams every later event
(blocking OCR + index for **any** upload once storage events accumulated). Added
`STORAGE_EVENTS {dms.storage.>}` + `dms.language.>` to `INTEL_EVENTS`. (Same bug
class as the `dms.review`/`dms.compliance` fixes already in that file.)

### Bug B — OCR skipped text files
`process_ocr` returned "not OCR-able" for non-PDF/image mimes, so email bodies
(`.md`) + dropped `.txt` files had no extracted content. Added `_is_text_mime` +
`_extract_text_file`: `text/*` (+ json/xml/csv/yaml) are read directly as one
page, so their content flows to lang_detect + embed + index.

## Verified

End-to-end: dropped a `.txt` into a watched folder → real document **with a
version** → OCR text-extraction → embedded → **top hit for a paraphrase hybrid
search**, with content snippet.

> Watch-item: the streams were created manually in dev (`natsio/nats-box`)
> because a `docker compose build connector` didn't seem to pick up the
> `pkg/events` change (build-cache quirk); `EnsureStreams` creates them on a
> clean boot — verify after `--no-cache` / fresh deploy.

## Files changed
`pkg/events/publisher.go`, `services/connector/cmd/server/main.go`,
`services/connector/internal/email/{email.go, document_client.go (removed)}`,
`services/connector/internal/ingest/ingest.go`,
`services/connector/internal/intake/intake.go`,
`services/intelligence/app/tasks/ocr.py`.
