# ADR 0079 — Redaction Review Queue + Burn-in via PyMuPDF

Date: 2026-05-05
Status: Accepted

## Context

Two redaction surfaces exist in SeDoc today and they aren't the same
shape:

1. **Legal-hold ad-hoc redact** ([redaction_handler.go](../../services/document/internal/handler/redaction_handler.go))
   — admin draws regions in the UI and clicks "Redact". Single round
   trip, one `document_redactions` row, status enum `queued |
   applied | failed`. Used when compliance flags a single document
   under hold and needs the regions blacked out before sharing.
2. **Blueprint §6.7 candidate-review redaction** — NER detects PII
   spans, the system pre-fills a review queue, an admin walks through
   approve / reject per candidate, then "Apply all" burns every
   approved span in one pass. Used for steady-state DLP.

The first is shipped. The second is what this ADR adds.

## Decision

Two new tables disjoint from `document_redactions`:

- **`redaction_candidates`** — one row per PII span in `pending` /
  `approved` / `rejected` / `applied`. Auto-populated by a sibling
  Celery task (`app.tasks.redact.populate_candidates`) consuming
  `dms.ner.completed.v1`; only `is_pii=true` entities cross over,
  with their character offsets resolved against `ocr_results
  .word_boxes` to produce per-candidate rectangle arrays.
- **`redaction_jobs`** — one row per "apply-all" pass. Snapshots
  the approved candidates at apply-time and links a source version
  to the redacted version produced by the burn-in worker.

Three new endpoints alongside the legacy `/redact`:

- `GET  /api/v1/documents/{id}/redaction-candidates` — review queue
- `POST /api/v1/documents/{id}/redaction/candidates/{cid}/review`
  — single-row state machine (approve / reject / unreject)
- `POST /api/v1/documents/{id}/redaction/apply` — emits
  `dms.redaction.apply_requested.v1`; the worker burns every
  approved candidate, uploads the resulting PDF as a fresh
  `content_blob` + `document_versions` row, fires
  `dms.version.uploaded.v1` so OCR / NER re-run on the redacted
  output.
- `GET  /api/v1/documents/{id}/versions/{vid}/unredacted` — gated
  download of the source version when the caller has the
  `view_unredacted` capability.

### Why a new version, not in-place mutation

Two reasons:

- **Audit**: the source PDF must remain accessible to compliance
  staff under the `view_unredacted` capability. An in-place burn
  destroys forensic value.
- **Idempotency**: re-running the burn (operator clicked twice,
  retry from worker crash) produces another version, never
  corrupting the previous one.

The redacted version becomes the document's `current_version_id`,
the source row stays in `document_versions` with the same blob it
always had, and `redaction_jobs.source_version_id` keeps the link.

### `view_unredacted` capability

Added to the OPA policy alongside the existing `view`/`edit`/etc.
hierarchy with rank 5 (between `view`=10 and the deny rules) — a
deliberate floor so a user with `view_unredacted` doesn't
automatically inherit `edit` (which they shouldn't, that defeats
the purpose). Not in the default `view` cascade. Owner / admin
still passes via Rule 6 (`user_role` shortcut) so administrators
are never locked out of an audit.

### Burn-in technique

`page.add_redact_annot(rect)` for every approved rectangle, then
`page.apply_redactions()`. PyMuPDF physically removes the underlying
text (not just an overlay), so OCR'ing the result will not surface
the original PII. The §6.7 acceptance test re-runs OCR on the
redacted PDF and asserts the original SSN string is absent.

### Why not extend `document_redactions`

It has different lifecycle semantics — that table tracks one operation
per "draw and burn" gesture. Forcing the candidate-review model into
the same row shape would require either nullable columns growing
without bound or a discriminator column branching every reader. Two
tables with clear names is cleaner; the existing path stays untouched.

## Consequences

**Pro**

- Two independent redaction workflows, each well-suited to its use case.
- Source preservation by construction — no path that mutates a blob
  in-place.
- Re-OCR on the redacted output validates the burn was effective; a
  poorly-burned redaction surfaces immediately as PII findings on the
  new version.

**Con**

- Storage cost ~doubles for redacted documents (source + redacted blob
  both live forever).
- Two redaction tables to navigate during audits. Mitigated by the
  table comments + this ADR.

## Out of scope (follow-ups)

- Image-only documents. PyMuPDF burn-in works on PDFs; redacting an
  image (TIFF, JPEG) requires rasterize-then-overlay which is a
  separate worker path. Tracked separately.
- Cross-tenant / customer-managed redaction policies (e.g. "always
  redact phone numbers, never review"). The ADR 0078 `ner_config
  .llm_entity_types` knob is the right home for that follow-up.
