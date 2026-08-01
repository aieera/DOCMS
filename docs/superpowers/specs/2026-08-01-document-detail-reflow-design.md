# Document detail / preview page reflow — design

**Status:** approved design, pre-implementation
**Date:** 2026-08-01
**File:** `web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx` (~1692 LOC) + new small components under `web/src/components/documents/detail/`

## Problem

The document detail page's right `<aside>` crams the primary action **icons and the `DeclareRecordButton` into one compact horizontal card**. When the declare picker/empty-state expands to a multi-line card it **overflows and overlaps the icon toolbar**. More broadly the page lacks a consistent, organized layout, and the preview area shows a bare "Inline preview isn't available for text/markdown" even for renderable text.

## Goal

A clean, professional, fully-organized layout: actions in the header, a consistent card-stack sidebar (Records in its own card — fixes the overflow), and an intentional preview area that renders text/markdown inline. Reuse existing components; **no backend change**.

## Design

### 1. Header (identity + actions)
File icon · name · lifecycle badge · `mime · size · uploaded by…`, with a right-aligned **icon action toolbar**: download · share · manage-access · workflow · `⋯` menu. **Declare-as-record is removed from the icon row** (root cause of the overflow) and moves to a sidebar Records card.

### 2. Preview area (main)
MIME-dispatched (existing pdf/image/video branches unchanged), plus:
- `text/markdown` → fetch the version download URL (`/api/v1/documents/{id}/versions/{ver}/download`) as text and render with **react-markdown** (already a dependency) in a styled, scrollable prose container.
- `text/plain` and other `text/*` → same fetched text in a monospaced `<pre>` scroll box.
- Non-renderable types → a polished **empty-state card**: file-type icon + "Preview isn't available for `<mime>`" + primary **Download** + secondary **Open Text tab** when extracted text exists.
- Tab bar keeps its scroll-snap; active state tightened for clarity.

New component `TextPreview` ({ url, mime }) owns the fetch + render + its own loading/error states, so the page file doesn't grow.

### 3. Sidebar — one card pattern, three sections
A shared `SectionCard` ({ title, children, actions? }) renders a labeled header + body. The sidebar is a vertical stack of three:
- **Details** — status · type · size · versions · MIME, then Version history / Comments links.
- **Records** — the full-width `DeclareRecordButton` (declare / status / vital / freeze). Full width, so the picker + empty-state fit with no overlap.
- **Integrity & retention** — Verify integrity + WORM lock.

The action icons move OUT of the sidebar (now in the header). Sidebar **stacks below the preview on small screens** (existing `lg:grid-cols-[…]` grid); full dark-mode via tokens (`border-border`, `bg-card`, `text-muted-foreground`).

### 4. Refactor (keep the file focused)
Extract into `web/src/components/documents/detail/`: `SectionCard`, `DetailsCard`, `RecordsCard`, `IntegrityCard`, `TextPreview`. The page composes them; each is small and independently testable. This is targeted extraction of the pieces we're already changing — not a rewrite of the whole 1692-LOC file.

## Guardrails (YAGNI)
No new tabs, no backend/API changes, all existing functionality preserved. Reuse `Card`, `react-markdown`, existing viewers, `DeclareRecordButton`, `CommentsPanel`, `PageHeader` conventions, `useAppMutation`, `sonner`.

## Verification
Per increment: `tsc --noEmit` clean, `eslint` 0 errors, relevant `vitest` green. A component test for `TextPreview` (markdown renders; non-text falls back) and for the sidebar cards' presence.
