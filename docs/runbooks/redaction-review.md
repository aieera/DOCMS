# Runbook: Redaction Review

Operational playbook for the candidate-review redaction workflow
introduced in ADR 0062. For the legal-hold ad-hoc `/redact` path see
[runbooks/legal-holds.md](legal-holds.md) instead.

## Quick map

| Stage | Where it lives |
|---|---|
| PII detection | `services/intelligence/app/tasks/ner.py` writes `document_entities` rows with `is_pii=true` |
| Candidate population | `app.tasks.redact.populate_candidates` consumes `dms.ner.completed.v1` and writes `redaction_candidates` rows in `pending` |
| Review queue | `GET /api/v1/documents/{id}/redaction-candidates` (UI: doc detail → "Review redactions" panel) |
| Per-row review | `POST /api/v1/documents/{id}/redaction/candidates/{cid}/review` `{action:"approve\|reject\|unreject"}` |
| Apply | `POST /api/v1/documents/{id}/redaction/apply` → emits `dms.redaction.apply_requested.v1` |
| Burn-in | `app.tasks.redact.apply_redactions` — PyMuPDF `apply_redactions()`, uploads new blob, creates new version |
| Source download | `GET /api/v1/documents/{id}/versions/{vid}/unredacted` — gated by `view_unredacted` |

## Day-1 onboarding for compliance reviewers

1. The reviewer's role must include `view` on the doc (cascades from
   workspace). No special role needed to *view candidates* — a
   reviewer who can read the doc can review.
2. To see the source after redaction, grant `view_unredacted` on the
   document (or its workspace) via `/admin/permissions`. Admins and
   owners pass automatically.

## Common operations

### Candidates aren't appearing for a freshly uploaded doc

- Check OCR finished: `GET /documents/{id}/versions/{vid}/ocr` should
  return 200 with text.
- Check NER ran: `document_entities` should have rows for that
  `version_id`. If not, check `intel_processed_events` for the `ner`
  consumer; replay if the row says `failed`.
- Check word_boxes were captured: `SELECT COUNT(*)::int >0
  FROM ocr_results WHERE version_id = $1 AND word_boxes::text != '[]'`.
  Without word_boxes, candidates are still created with empty
  rectangle arrays — burn-in will skip them silently. This is the
  Surya path's known limitation; rerun with `force_engine=pymupdf`
  if the doc has embedded text.
- The `populate_candidates` task is best-effort; failures DLQ to
  `dms.dlq.intel_events.populate_candidates.<reason>`.

### "Apply all" hangs in `running`

- Check `vaultdms-intelligence-worker` logs for the burn-in task:
  `docker logs vaultdms-intelligence-worker | grep apply_redactions`.
- Common cause: PyMuPDF can't open the blob (corrupt PDF). The job
  will move to `failed` after the Celery retry budget; check
  `redaction_jobs.error_message`.
- If the worker died mid-task, the job stays in `running`. Look up
  its event_id and re-emit the trigger via the outbox replay tool.

### Need to revert a redaction

- The redacted version is a real `document_versions` row; deleting it
  reverts the document's current pointer to the source. Use the
  storage service's `RestoreVersion` flow; the source blob has been
  preserved untouched.
- The `redaction_jobs` row stays as the audit trail — don't delete it.

### Unredacted download returns 403 unexpectedly

- Confirm the user has `view_unredacted` on the document (or
  workspace, or is owner/admin). Direct grant via
  `POST /api/v1/admin/permissions` with capability
  `view_unredacted`.
- The download endpoint is gated through OPA Rule 6 + an explicit
  `view_unredacted` permission lookup. A user with `view` only
  cannot see the source even if they can see the redacted version
  — by design.

## Limits + cost notes

- Storage doubles: source + redacted blobs both live forever. Quota
  bookkeeping in `tenant_usage` counts both.
- The "Apply all" admin gate triggers when >50 candidates are queued
  in one run; below that any reviewer can apply. Tunable via
  `ner_config.bulk_apply_threshold` (future; today it's a frontend
  constant in `RedactionReviewPanel.tsx`).

## Related

- ADR 0062 — design rationale + table layout
- ADR 0061 — NER pipeline that produces the candidates
- ADR 0054 — compliance findings UI (different surface; some
  reviewers will use both)
