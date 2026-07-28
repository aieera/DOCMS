# Upload, Auto-Tagging & Document Viewer UX — Design

Date: 2026-07-26
Status: approved in conversation (dashboard upload dialog scope, "both" tagging
strategy, compact-toolbar viewer layout); this document is the written record.

## Problem

1. The dashboard's "Upload" quick action is a plain link to `/workspaces`
   (`web/src/routes/_authenticated/index.tsx:220`) — it uploads nothing and
   never asks for a destination. Users expect a real upload entry point.
2. Auto-tagging (ADR 0052) runs end-to-end, but the default
   `auto_apply_threshold` of 0.95 sends nearly all AI tags to the pending
   `tag_suggestions` queue, which is buried below the fold on the document
   page. To users the whole feature reads as "manual tagging only".
3. The document viewer wastes its first ~250px on low-density chrome (integrity
   band, right-rail button stack), hides Tags below the fold, and defaults the
   PDF preview to the `watermarked` rendition even when none exists
   (`web/src/components/viewer/DocumentPreview.tsx:176`), greeting users with
   an empty "No watermarked preview" box.

## Scope

Frontend-heavy; the only backend change is one Python default. No new
endpoints — every needed API already exists:

- `POST /uploads/predict` (`web/src/api/predictiveFiling.ts`) — folder + tag
  suggestions from filename/mime/workspace.
- `GET/REVIEW /documents/{id}/tag-suggestions` (`web/src/api/intelligence.ts`).
- `listPendingTagSuggestions` — tenant-wide pending suggestions.
- `useUpload(workspaceId, folderId)` + existing progress UI.
- `getWorkspaces`, folder listing APIs.

## 1. Dashboard smart upload dialog

New `DashboardUploadDialog` component, opened by the **existing** Upload
quick-action card (rewired from `href: '/workspaces'` to an `onClick`; the
card's copy finally becomes true — it also accepts a dropped file).

Flow:
1. Drop zone / file picker (multi-file allowed, same limits as workspace
   upload).
2. Workspace select (required) — existing `getWorkspaces`.
3. Folder picker (optional; default workspace root).
4. On file+workspace chosen → call `predictFiling`; render:
   - suggested folder as a chip ("📁 Invoices · 92%") — click applies it to
     the folder picker;
   - suggested tags as pre-ticked chips — untick to drop.
5. Upload via `useUpload`; report folder-suggestion accept/reject via
   `sendFilingFeedback` (closes the predictor's learning loop).
6. Success state: link to the uploaded document + note "AI is analyzing —
   more tags may be suggested shortly."

Edge cases: no workspaces → empty state linking to workspace creation;
predict call failing is non-blocking (dialog works without suggestions).
Multi-file batches: `predictFiling` is called for the FIRST file only; its
folder suggestion applies to the whole batch (one destination per batch),
and suggested tags apply per that first file only — subsequent files rely
on the async pipeline. Keeps the dialog simple; per-file prediction is a
possible follow-up.

## 2. Tagging: threshold + surfacing ("both")

- Default `auto_apply_threshold` drops **0.95 → 0.85** in
  `services/intelligence/app/tasks/auto_tag.py` `DEFAULT_CONFIG` and in the
  admin Intelligence panel's displayed default. Tenants with a saved
  `auto_tag_config` row keep their explicit value.
- Dashboard gains an "AI tag suggestions waiting: N" card (data:
  `listPendingTagSuggestions`), linking to the existing review surface
  (Admin → Tags queue).
- `DocumentCard` in the workspace browser gets a "✨ N suggested" badge;
  clicking opens a popover with accept/reject chips backed by
  `reviewTagSuggestions`. No badge when zero pending.

## 3. Document viewer — compact toolbar layout

Route: `web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx`
(1.7k lines; the redesign may extract header/rail into components, no logic
rewrites).

- **Header (one band, two rows):** row 1 — file icon, name, status badge,
  integrity chip ("✓ verified" / "not verified", click = verify action);
  row 2 — uploader · date · version · mime, and a right-aligned icon toolbar:
  Download, Share, Compare, Manage access, Create task, then Declare-as-record,
  WORM lock and other rare actions inside an overflow "⋯" menu. The standalone
  integrity band and the right-rail button stack are removed.
- **Right rail becomes an info rail:** Tags (with ✨ suggestion chips,
  one-click accept/reject — the existing `TagSuggestionsPanel` moves up here),
  key details (size, language pill, version count), presence, Comments.
- **Preview fallback fix:** `PdfPreviewSwitcher` starts in `original` mode
  when the watermarked rendition reports zero pages; the Watermarked/Original
  toggle stays for documents that have both. (Bug-class fix, ships first.)
- Keep all existing tabs and their content unchanged.

## Testing

- Vitest: dialog (workspace required, folder optional, chip applies folder,
  feedback fires, predict-failure path), dashboard suggestions card, document
  card badge/popover, preview fallback (no watermarked pages → original
  renders; both present → watermarked default), header toolbar renders all
  actions incl. overflow.
- Python: test pinning the 0.85 default.
- A11y: new/changed surfaces pass the existing axe gate (ADR 0120); toolbar
  buttons keep accessible names.

## Out of scope

- No backend pipeline changes beyond the default threshold.
- No new endpoints.
- The invoice↔delivery-note auto-relationship feature (separate parked
  design).
