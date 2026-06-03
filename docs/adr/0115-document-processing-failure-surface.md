# ADR 0115 — Document processing failure surface

Status: Proposed.
Date: 2026-05-21.
Depends on: existing intelligence worker pipeline
(`services/intelligence/app/tasks/{ocr,ocr_quality,classify,ner,embed}.py`)
and the document model (`services/document/internal/model/document.go`).

## Context

Today, after a file lands in storage and the `dms.version.uploaded.v1`
event fires, a fan-out of intelligence tasks runs against it: OCR,
OCR quality scoring, classification, NER, embedding, language
detection, smart-routing. Each task is a separate Celery worker
hitting its own external dependencies (Surya, SpaCy, OpenSearch,
Qdrant, an LLM tier).

When any of those tasks **silently fails**, the system has no way
to surface that to the user. Concretely:

- OCR can drop a job (`enqueued` row written, never transitioned to
  `completed`/`failed` — observed today across 5+ documents). The
  user sees "OCR running" forever; the Raw text tab is empty.
- A publish-after-OCR race can drop the
  `dms.version.ocr_completed.v1` event silently
  ([ocr.py::_publish_ocr_completed](../../services/intelligence/app/tasks/ocr.py)
  catches the exception and returns False). That blocks every
  downstream consumer (quality, classify, NER, embed) but the user
  sees no signal — just empty tabs.
- The classifier can return low-confidence on a doc whose mime is
  unrecognised. No error surface for "this doc isn't a kind of
  document we handle."
- A corrupted upload (truncated PDF, valid mime but invalid bytes)
  passes ClamAV + completes upload, then poisons every downstream
  task. The user sees the doc card but every tab is broken.

The current "No content" badge on `DocumentCard` ([components/documents/DocumentCard.tsx:30](../../web/src/components/documents/DocumentCard.tsx#L30))
is **not** a failure indicator — it surfaces `current_version_id IS NULL`
(no file uploaded yet). There is no UI state today for "file was
uploaded but processing failed."

We need:
1. A way to record per-stage processing status + failure reason at
   the document level.
2. A user-visible surface that says "processing failed because X,
   click to retry" instead of empty tabs.
3. A retry endpoint that re-emits the trigger event so the
   pipeline reruns.
4. Audit + observability so operators can chase silent drops
   without grepping worker logs.

## Decision

Add a processing-status surface on documents, capture per-stage
failure reasons in a new table, and expose retry via a single
admin-gated endpoint that replays the trigger event.

### Schema

```sql
-- New column on documents. Rolls up the per-stage state below into
-- a single value the UI can render at a glance. Computed on stage
-- transitions; the per-stage table is the source of truth.
ALTER TABLE documents
  ADD COLUMN processing_status text NOT NULL DEFAULT 'pending'
    CHECK (processing_status IN
      ('pending','running','completed','failed','partial'));

-- Per-stage status + reason. One row per (document, stage). Updated
-- by each intelligence task at start + on terminal outcome.
CREATE TABLE document_processing_stages (
  tenant_id    uuid NOT NULL,
  document_id  uuid NOT NULL,
  version_id   uuid NOT NULL,
  stage        text NOT NULL CHECK (stage IN
    ('upload','virus_scan','ocr','ocr_quality','classify',
     'ner','embed','lang_detect','route_suggest')),
  status       text NOT NULL CHECK (status IN
    ('pending','running','completed','failed','skipped')),
  attempts     int  NOT NULL DEFAULT 0,
  failure_reason  text,   -- machine-readable code (see taxonomy below)
  failure_detail  text,   -- human-readable explanation, safe to display
  started_at   timestamptz,
  completed_at timestamptz,
  PRIMARY KEY (tenant_id, document_id, stage),
  FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id)
);
-- RLS policy mirrors `documents`.
```

### Failure-reason taxonomy

Stable codes so the UI can map to localized copy and the audit log
to dashboards. Each stage may use a subset:

| Code | Meaning |
|---|---|
| `unsupported_mime` | Mime type out of scope for the stage (e.g. NER on an image). |
| `file_corrupted` | Bytes failed to parse (truncated PDF, malformed image). |
| `dependency_unavailable` | External dep down (OpenSearch, Qdrant, LLM provider). |
| `dependency_quota` | LLM rate limit / token-budget exhausted. |
| `timeout` | Task exceeded its time_limit. |
| `decrypt_failed` | Envelope-encrypted blob couldn't be decrypted for OCR/preview. |
| `event_publish_failed` | Stage completed but the downstream event couldn't be published. |
| `worker_crash` | Process died mid-task (Celery retries exhausted). |
| `policy_denied` | Tenant policy disabled this stage (e.g. NER LLM off). |
| `unknown` | Reserved for codepaths that haven't been instrumented yet. |

### API

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/api/v1/documents/{id}/processing` | List per-stage status rows + roll-up. |
| `POST` | `/api/v1/documents/{id}/reprocess` | Re-emit `dms.version.uploaded.v1` for `current_version_id`. Optional `stages: ["ocr","classify"]` body to scope. Admin-only. |
| `POST` | `/api/v1/documents/{id}/reprocess/{stage}` | Re-emit just one stage's trigger. |

Reprocess is **idempotent** at the event layer (`event_id` is fresh
each call, intel_dedupe handles concurrent re-fires). It's
**rate-limited** per `(tenant, document)` at 1 call / 30 s to prevent
runaway loops.

### Worker instrumentation

Each Celery task gets a small wrapper that:

1. Writes `document_processing_stages` row with `status='running'`,
   `started_at=now()`, `attempts=attempts+1` at task start.
2. On success: updates the row to `status='completed'`,
   `completed_at=now()`. Recomputes `documents.processing_status`.
3. On terminal failure (retries exhausted): updates row to
   `status='failed'`, fills `failure_reason` from an
   error-classification helper that maps the caught exception to
   a taxonomy code, plus `failure_detail` from `str(err)` after
   PII scrubbing.
4. On stage-skipped (e.g. mime not in scope): writes
   `status='skipped'` with a non-error reason.

The publish-after-DB-commit race is closed via the existing outbox
pattern: each stage transition writes a row in a new
`document_processing_outbox` table inside the same transaction; a
polling publisher forwards to NATS. Same pattern audit + document
services use today.

### UI

Two new surfaces:

**1. `DocumentCard` failure badge.** New variant on the existing
amber-style badge — red, label "Processing failed", click opens a
detail popover. Replaces nothing; `No content` stays for the no-
file-uploaded case.

**2. Detail page banner.** New component
`ProcessingFailureBanner` mounted in
`documents/$documentId.tsx`, sits above the tab bar when
`processing_status === 'failed'`. Shows: which stages failed, the
human-readable reason, and a **Retry** button. Admins see a
**Retry per-stage** dropdown; non-admins only see the rolled-up
retry.

**3. Per-stage detail panel** (admin-only) mounted on the existing
Activity tab — small table of stage / status / attempts / last
error. Read from `GET /documents/{id}/processing`.

### Phases

| Phase | Scope | Estimate |
|---|---|---|
| 1 | Migration + per-stage table + worker instrumentation on **OCR only**. Backfill stage rows for existing docs from `ocr_processed_events` + `ocr_results`. | 2 days |
| 2 | Reprocess endpoint + rate limit + per-stage trigger replay. | 1 day |
| 3 | UI: DocumentCard failed-badge variant, detail-page banner, retry button. | 1-2 days |
| 4 | Instrument remaining stages (classify, NER, embed, lang_detect, route_suggest, ocr_quality, virus_scan). | 2 days |
| 5 | Per-stage admin detail panel + audit log integration. | 1 day |

## Open questions

- **What's the SLA before declaring a stage "stuck"?** Today's
  fix-stuck-OCR work uses 30 minutes as the user-visible threshold.
  For the durable state machine we should make this per-stage and
  configurable (`processing_stage_timeouts` table or static
  per-stage const). **Decision: per-stage const for MVP, configurable
  in Phase 5.**

- **Should `reprocess` also re-run completed stages?** Default: no
  (rerun only failed + skipped stages). Force flag re-runs
  everything from scratch. **Decision: opt-in `force=true` re-runs
  completed; default reruns only non-completed.**

- **What happens if `reprocess` is called while a stage is still
  `running`?** Two options: (a) reject with 409 conflict, (b) cancel
  the in-flight task and re-enqueue. (a) is simpler and matches how
  the OCR re-run button already works. **Decision: (a).**

- **PII in `failure_detail`?** Worker exception messages can carry
  text snippets that include PII. **Decision: error-classifier
  helper produces sanitized `failure_detail`; raw exception goes to
  structured logs only.**

- **Reprocess permission gate.** Today's OCR re-run is
  `owner|admin|compliance_officer`. Reprocess should match. Members
  see the rolled-up status but not the retry button.

## Migration / rollback

Forward: migration adds nullable columns + a new table with safe
defaults. Existing documents start at `processing_status='pending'`
until the backfill script (Phase 1) walks them. Workers begin
writing stage rows on next task execution; backfill catches up the
historical rows.

Rollback: drop the new column + table. Workers tolerate the
absence of the table (gated by feature flag
`SEDOC_PROCESSING_FAILURE_TRACKING=true`). Existing UI keeps
working — no surface depends on the new state until Phase 3 ships.

## What changes if you do nothing

Today's behavior: OCR drops jobs silently, downstream consumers
never fire, every tab on the doc detail page shows an empty state.
Users have no recovery path short of admin DB poking + manually
re-emitting events through the outbox. The "OCR appears to be
stuck" warning shipped earlier today is the only thread of user-
visible recovery; everything else (classify, NER, embed) is
invisible to users.
