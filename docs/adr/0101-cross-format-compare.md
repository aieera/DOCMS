# ADR 0101 — Cross-format document compare

Status: Accepted (Phase 1 backend + minimal frontend shipped)
Date: 2026-05-19
Related: §18 Feature 8 of the blueprint; ADR 0057 (OCR quality),
ADR 0067 (annotations), the existing `services/document/internal/
service/redact.go` which already uses `github.com/sergi/go-diff`.

## Context

Users need to diff two documents that aren't necessarily in the same
format — a PDF contract vs its DOCX redline, the same MSA in 2023 vs
2024, a TXT export vs the original. §18 Feature 8 asks for one
endpoint that converts both inputs to canonical text and produces a
side-by-side diff.

The infrastructure for this is mostly already in place:

- Every uploaded PDF/image goes through OCR (Surya) → `ocr_results.
  text_content`. So canonical text is one query away.
- Non-OCR formats (TXT/HTML/CSV/code) get text-extracted by the
  intelligence service's `tasks/extract.py` and stored in
  `extracted_text` rows.
- DOCX is converted by the `preview` service (LibreOffice) and OCR'd
  from the resulting PDF — also lands in `ocr_results`.
- `github.com/sergi/go-diff/diffmatchpatch` is already in the
  document service's go.mod (redaction uses it).

So Phase 1 is just an HTTP endpoint that joins those existing pieces.

## What is shipped now (Phase 1)

- This ADR.
- `services/document/internal/handler/compare_handler.go`:
  - `POST /api/v1/compare`
  - Body: `{doc_a_id, version_a_id?, doc_b_id, version_b_id?, granularity?}`
  - Returns:
    `{doc_a: {id, title, text_chars}, doc_b: {...}, diff: {operations: [...]}, summary: {added, removed, changed_blocks}}`
  - Pulls text from `ocr_results` (concatenated by page_number); falls
    back to "no canonical text available" if neither side has been
    OCR'd. Caller's tenant is enforced via RLS.
- `web/src/api/compare.ts` typed client.
- `web/src/components/documents/CompareDialog.tsx` — picks a second
  doc, calls the endpoint, renders paragraph-level diff in a
  side-by-side layout.
- "Compare with…" action on the doc detail page header.

## What is **not** shipped now

- **Word-level / semantic-summary toggle.** Phase 1 ships
  paragraph-level (line-based) diff. diff-match-patch can do
  character-level diff and a "semantic cleanup" pass, but the
  side-by-side renderer for character-level needs a more complex
  layout than I'm building in Phase 1. Toggle UI is wired with the
  word-level and semantic options disabled + tooltipped as "coming
  soon."
- **Table-aware diff.** If a contract has a markdown-/HTML-style
  table in both versions, today's diff treats every cell as a line
  and produces a noisy result. Phase 2 normalizes tables before
  diffing.
- **Format-specific normalization** (collapse whitespace, smart-quote
  → ascii, NBSP → space, trailing-newline elision). Today: minimal
  normalization (lowercase comparison off, just trim trailing
  whitespace per line). Phase 2 adds a normalization pre-pass that
  is config-driven so different contract templates can tune it.
- **Caching.** Each compare runs the diff from scratch. For two
  multi-page documents this can take 1-3 seconds. Phase 2 adds a
  `compare_results` cache keyed on `(version_a, version_b, granularity)`.
- **PII/redaction-aware diff.** Today the canonical text is the raw
  OCR output. If a doc has been redacted, the diff shows the redacted
  black-bar substitutions as character changes. ADR 0086 (redaction)
  defines the unredacted-bytes path; combining the two is Phase 2.
- **Playwright** (multi-doc compare flow).

## Why no new schema

Comparisons are read-only operations. There's nothing to persist —
the API returns the diff on demand. Phase 2's cache table is the
first time a comparison gets a DB row, and that's an optimization, not
a correctness requirement.

If/when "save this comparison and email it to me" lands, that's a
separate scope (compare-report artifact) and it ships with its own
table.

## Text source resolution

The endpoint resolves canonical text in this order, per side:

1. `version_a_id` (or `_b_id`) was supplied → use that version
   directly.
2. Otherwise, look up `documents.current_version_id`.
3. Try `ocr_results` for that version (concatenate by `page_number`
   ascending, joined with `\n\n`).
4. Fall back to the future `extracted_text` table (Phase 2) for
   non-OCR formats.
5. If nothing is available, return 422 with `{side: "a"|"b", reason:
   "no_canonical_text"}` so the frontend can surface the right
   actionable error.

Subgraph capped at 200k characters per side. Beyond that, diff time
grows quadratically and the renderer is unusable anyway; respond with
`{truncated: true}` so the UI can show a partial-diff warning.

## API shape

```http
POST /api/v1/compare
Content-Type: application/json
Cookie: dms_session=...

{
  "doc_a_id":     "<uuid>",
  "version_a_id": "<uuid>"   // optional; defaults to current_version
  "doc_b_id":     "<uuid>",
  "version_b_id": "<uuid>",  // optional
  "granularity":  "paragraph" // "paragraph" only in Phase 1; "word"|"semantic" reserved
}

200 OK
{
  "doc_a": { "id": "...", "title": "...", "version_id": "...", "text_chars": 42123 },
  "doc_b": { "id": "...", "title": "...", "version_id": "...", "text_chars": 41855 },
  "granularity": "paragraph",
  "diff": {
    "operations": [
      { "op": "equal",  "text": "..." },
      { "op": "delete", "text": "..." },
      { "op": "insert", "text": "..." }
    ]
  },
  "summary": {
    "added_chars":   1402,
    "removed_chars": 1670,
    "changed_blocks": 23
  },
  "truncated": false
}
```

## Frontend — Phase 1 viewer

`CompareDialog.tsx`:
- Modal opened from the doc detail page header's "Compare with…"
  button.
- Right panel: doc picker (search-as-you-type against `/search`,
  same component the share dialog uses).
- After both selected → POST `/compare` → render side-by-side panes.
- Each pane displays the SAME diff result, one filtered to
  equals+removes (left) and equals+inserts (right). Removed runs
  are highlighted red on the left; inserts green on the right.
- Footer: `{added_chars} added · {removed_chars} removed · {changed_blocks} blocks`.

The viewer is intentionally minimal — Phase 2 swaps in the
word-level renderer with synchronized scrolling.

## Open questions deferred

- License gating: is compare an enterprise feature? Probably yes;
  hook into `feature_flags.compare` (ADR 0095) when license
  enforcement expands beyond doc-service.
- Cross-tenant compare for platform admins (ADR 0069 support
  search). Should work but needs a tenant-bypass parameter; left
  out until a real support case asks for it.
- Compare history. Save "this was diffed by X on date Y" for audit?
  Probably yes for contracts but not for arbitrary docs. Defer.
