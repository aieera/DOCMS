# Document detail page — layout v2 (structural rework)

**Status:** approved design (user UX review + two confirmed forks), incremental implementation
**Date:** 2026-08-01
**File:** `web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx` (+ e2e nav updates)
**Supersedes:** the single "More details & intelligence" accordion and the Info/Records/Tags sidebar sub-tabs from the 2026-08-01 reflow round.

## Source

A detailed hands-on UX review of the document page (autotag-test-invoice.pdf) across
Preview/Text/Entities/Compliance. This spec records the agreed fixes and the two
direction forks the user chose.

## Confirmed decisions

- **Tab bar:** collapse the 11 flat tabs into **5 top-level groups with sub-tabs** —
  Document (Preview · Text · Redaction) · Analysis (Q&A · Entities · Relationships) ·
  Compliance · Collaboration (Workflow · Comments · Signatures) · History (Activity).
- **Right rail:** **individually collapsible sections** with persisted open/closed
  state, ordered by frequency — Details, Tags, Custom Fields, Tasks, Language,
  Retention. Replaces the Info/Records/Tags sub-tabs.

## Problems → fixes (from the review)

1. **Three nested scrollbars** (page `<main>`, rail, PDF iframe) desync the rail from
   the document. → Phase 2: viewport-locked shell; only the viewer pane and rail
   scroll internally; reset rail scroll to top on tab change. Isolated change.
2. **Tab strip hides ~5 of 11 tabs with no affordance**; clicking Entities pushed
   Preview off-screen. → 5 groups + sub-tabs (above); flatter→hierarchical.
3. **Sticky wrong things**: title+tabs scroll away, Watermarked/Original stays pinned.
   → pin condensed title + tab strip; move Watermarked/Original into the viewer's own
   toolbar (with zoom/page).
4. **Action toolbar = 5 unlabelled icons in the rail top.** → move to the header beside
   the filename; promote **Download** to a labelled button, rest into a `⋯` menu.
5. **Duplication**: Download (icon + preview footer), Comments (tab + Info link),
   Version history (row + link), Tags (Info block + Tags tab). → one home each:
   Details + Tags in the rail; Version history and Comments are tabs only.
6. **Rail accordion grab-bag** ("MORE DETAILS & INTELLIGENCE"). → split into individual
   persisted collapsible sections, ordered by use. Move "No one else here" presence into
   the header near the avatar.
7. **Status badges are read-only decorations.** → clickable deep links: Critical →
   Compliance (show hit count), OCR → Text, English → translation panel.
8. **Small wins**: raise breadcrumb truncation threshold; preview fit-to-width to remove
   dead white space; "Preview not displaying?" only on load failure.

## Out of scope (separate track)

**Auto-tag data quality** — `effective_date: 3704 0044 0532` (IBAN chunk),
`party_name: iban/vat` (label words) are extraction errors, not layout. Backend
intelligence task: confidence threshold before auto-tags surface, or a "needs review"
state for low-confidence tags. Logged separately.

## Implementation order (each verified: tsc + eslint + vitest; committable alone)

Phase 1: (a) tab groups → (b) rail sections → (c) actions to header → (d) dedup →
(e) badge deep links → (f) small wins. Phase 2: scroll shell (on its own).

## Guardrails

No backend/API change in Phase 1. Preserve every `data-testid="tab-<key>"` (e2e depends
on them); add `tabgroup-<key>`. Reuse existing components (`Card`, `Accordion`,
`DeclareRecordButton`, viewers, dialogs). Verify per increment.
