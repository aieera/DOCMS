# Follow-up: auto-tag / NER extraction data quality (not layout)

**Found during:** the 2026-08-01 document-detail layout review (autotag-test-invoice.pdf).
**Type:** intelligence/extraction quality — backend, not a UI change.
**Priority:** medium. The noise is highly visible because auto-tags get prime real
estate in the document rail.

## Problem

The Tags panel surfaces clearly wrong extractions on the test invoice:

- `effective_date: 3704 0044 0532` — a chunk of the IBAN tagged as a date.
- `party_name: iban` and `party_name: vat` — label words captured as party names.

These are low-confidence / mis-typed entities being promoted to suggested tags.

## Options (backend / intelligence service)

1. **Confidence threshold** before an auto-tag is surfaced at all — drop suggestions
   below a tuned score so obvious garbage never reaches the rail.
2. **"Needs review" state** for low-confidence tags — surface them in a muted,
   explicitly-uncertain state instead of as normal suggestions, so a human confirms.
3. **Type-aware validation** — e.g. reject `effective_date` values that don't parse as
   dates; reject `party_name` values that match known label tokens (iban/vat/tin…).

## Where

`services/intelligence` (OCR/AI worker) — the NER/auto-tag pipeline that emits tag
suggestions consumed by `web/src/components/intelligence/TagSuggestionsPanel.tsx`.

## UI note

The layout pass (Phase 1) only moved these suggestions into a single **Tags** rail
section. A cheap UI-side mitigation (hide/soften below a confidence cutoff in
`TagSuggestionsPanel`) is possible if the backend fix is deferred — ask before doing it,
since it changes what reviewers see.
