# SeDoc web — UX improvement phase (design spec)

**Date:** 2026-09-17 · **Branch:** `neu-ui` (neumorphic UI phase complete) · **Status:** draft for review

Evidence base: a seven-lens UX audit (affordance, micro-interactions, feedback, task-flow, hierarchy, navigation, forms)
run against the live app (screenshots, light+dark, desktop+mobile) and the code. **71 findings** — 15 high, 36 medium,
20 low. High-severity findings were put through adversarial verification: **17 verdicts returned, all `real=true`, zero
refutations**; 15 of 17 also judged user-impactful. Seven high findings are *unverified* (the verifier agents hit the
session rate limit) — they are retained and marked, not dropped.

## 0. Scope boundary (IMPORTANT — read before approving)

The UI phase was strictly "class strings only". **A UX phase cannot be.** You cannot fix "every notification is a dead
end" without adding navigation, or "the upload progress tray is never mounted" without mounting it. So this phase widens
the boundary by exactly one ring:

| Allowed | Still forbidden |
|---|---|
| Client-side interaction wiring: `onClick`/`onKeyDown` handlers, `navigate()` calls, mounting existing components, query `isError` destructuring, toast/undo calls, URL search-param state | Backend/services/proto — **zero** files outside `web/` |
| Markup/ARIA structure, labels, focus order, copy | API contracts, endpoints, request/response shapes |
| Rendering new branches from data the component **already fetches** | New queries/mutations, new query keys, changed business rules |
| Styling, tokens, shadow tiers, transitions | New dependencies |

Every change must be justifiable as "the same data, presented or reachable better". If a fix would need a new endpoint
or a data-model change, it is **out of scope** and goes to §6.

**Hard constraint (unchanged):** the axe a11y gate (ADR 0120), keyboard + RTL e2e, vitest, tsc, lint, build all stay green.

## 1. Themes (what the 71 findings actually say)

1. **Dead ends & after-action voids** — the app completes an action then abandons the user. Upload succeeds → dialog
   closes, document unreachable in one click. Notifications name an artifact but cannot open it. A breadcrumb segment
   404s. A deep link is discarded at the login gate.
2. **Lying empty states** — on query failure, core screens render the *success-empty* state. The tasks inbox says
   "You're all caught up" when it actually failed to load; search renders a blank canvas; notifications and admin lists
   do the same. After the 8s toast fades, the user is misinformed with no retry.
3. **Broken interaction conventions** — the workspace browse grid requires **double-click to open** while every other
   surface opens on single click; the hover-raise shadow tier the design calls for **does not exist**, so
   `hover:shadow-neu` is a silent no-op on cards that already idle at `shadow-neu`; the raised→inset press flip exists
   on only 3 of dozens of pressables.
4. **Mobile is second-class** — the nav drawer stays open after navigating (every mobile navigation costs a second tap);
   there is **no search entry point at all** below `sm`; the tasks table's Actions column is off-canvas.
5. **Form friction & a11y debt** — required-field signalling is a disabled button whose label is ~1.7:1 contrast; ~130
   call sites render `<label>` without `htmlFor` (not programmatically associated); ~20 destructive confirmations use
   native `window.confirm()`.
6. **Hierarchy noise** — every badge carries `shadow-neu-sm`, so list rows stack shadows; the audit log renders a wall
   of identical solid-primary pills; task priority prominence is inverted (the commonest value is the loudest).

## 2. Priority tiers

### P1 — user-blocking or misleading (do first)
| # | Screen | Problem | Fix |
|---|---|---|---|
| P1.1 | tasks, search, notifications, workspaces, admin lists | Query failure renders the success-empty state ("You're all caught up") or a blank canvas — a false all-clear with no retry | Destructure `isError`/`refetch`; render the existing `ErrorState` with a Retry before the empty branch. Never show "caught up" copy unless the query actually succeeded |
| P1.2 | Workspace browse | Folder/file tiles need **double-click** to open; every other surface is single-click; no keyboard path to open | Single click opens; move selection to the existing hover checkbox / modifier-click; `Enter` opens, `Space` selects |
| P1.3 | Notifications (page, bell dropdown, dashboard activity) | Every row is a dead end — names a document/task but cannot open it | Make the row body activate: `document` → its detail route; `task` → tasks with that task focused |
| P1.4 | Upload (dashboard + browser) | No in-flight progress UI **anywhere** — a complete `UploadProgress` tray exists but is imported by nothing; the store already feeds it | Mount `<UploadProgress/>` next to `<DuplicateUploadDialog/>` in `_authenticated.tsx` |
| P1.5 | Upload failures | Failure reason is stored then never shown; no retry | Render `item.error` under the failed row + a per-row Retry |
| P1.6 | Dashboard upload | Success → dialog closes, user stranded; document costs several clicks to reach | Toast with an action (or navigate) to the destination folder/document |
| P1.7 | Document detail | The "Document" breadcrumb segment links to a routeless URL → shell-less 404 | Render structural segments as `<span>`, or point at the containing folder |
| P1.8 | Login / deep links | Deep link discarded at the auth gate; always lands on the dashboard | Carry `redirect` through login; validate it is internal; navigate there after sign-in |
| P1.9 | Mobile drawer | Stays open after navigating — every mobile navigation is two taps | Close on route change |
| P1.10 | Mobile topbar | **No search entry point at all** below `sm` | Search icon button → `/search` (and/or a drawer entry) |
| P1.11 | Dialogs (new folder, passkey, share, SMTP) | Required-field signalling = a disabled button at ~1.7:1 contrast, no markers, no inline hint | Keep the CTA enabled and validate on click with the `Input error` prop; mark required fields |

### P2 — friction & inconsistency
Hover-raise tier (`--nm-shadow-lg` + `shadow-neu-lg`) so `hover:shadow-neu` stops being a no-op (H0); raised→inset press
on all pressables incl. tiles (H1); audit-log badge wall → flat/outline variant (H14); badge `shadow-neu-sm` removed for
non-interactive pills (M31); inverted task-priority prominence (M32); five different list-container idioms unified (M34);
task actions' affordance consistency table↔kanban (M3/M4); one view-toggle paradigm (M5); approver can open the document
(M15); dashboard task row deep-links to that task (M17); Share promoted out of the overflow (M20); `window.confirm` →
`ConfirmDialog` (M10); `<label htmlFor>` association sweep (M9); mention picker keyboard navigation (M12); consistent
Enter-to-submit in dialogs (M14); document-detail tab in the URL (M23); folder shown in the document breadcrumb (M24);
viewer close uses `replace` so Back works (M25); tasks table actions reachable at 390px (M33); consistent loading
treatment (M30); silent trash delete gets a toast + undo (M26).

### P3 — polish
The 20 low findings: placeholder weight, disabled-passkey explanation, filter-chip styling, spacing rhythm, and similar.

## 3. Approach

Foundation-then-sweep, mirroring what worked for the UI phase:

1. **Shared plumbing first** — add the missing shadow tier; add a reusable "query failed" render path so P1.1 is one
   pattern applied at ~6 call sites rather than six bespoke fixes; mount the upload tray once.
2. **Then per-theme batches** — dead ends, mobile, forms, hierarchy — each an independently reviewable commit.
3. **Every batch ends green**: tsc, lint (incl. rtl/utf8/tenant), vitest, and the a11y gate.

## 4. Testing

- **Unit (vitest):** each P1 fix gets a focused test where it is testable without a router — error-branch rendering
  (`isError` → ErrorState + retry), notification row activation target, breadcrumb segment rendering, redirect-param
  round-trip, drawer-closes-on-route-change.
- **e2e:** extend the keyboard spec for single-click/Enter-to-open on tiles; keep the a11y gate green (the form-label
  association sweep should *improve* it).
- **Visual:** before/after screenshots at 390px and 1440px, light+dark, for the touched screens.

## 5. Non-goals

No IA/navigation restructure; no new features; no new dependencies; no visual re-theme (the neumorphic language stays as
shipped); no backend change of any kind.

## 6. Out of scope (needs backend/product decisions)

- Delegate-approval by user picker instead of a raw UUID (M16) — needs a user-search endpoint for that dialog.
- True undo for destructive actions (soft-delete restore exists; a general undo needs API support).
- Any notification deep-link that requires a resource the notification payload does not carry.

## 7. Decisions (ruled 2026-09-17)

1. **Scope boundary (§0) — APPROVED as written.** Client-side interaction wiring (handlers, `navigate()`, mounting
   existing components, `isError` branches, ARIA/labels) is in scope. Zero files outside `web/`; no new endpoints,
   queries, mutations, query keys, or business rules. This supersedes the "do not alter routing/state" line in the
   original pasted architecture prompt, which would have made the P1 defects unfixable.
2. **P1.2 single-click — APPROVED.** Single click opens folders/files; selection moves to the existing hover checkbox
   and modifier-click; `Enter` opens and `Space` selects for keyboard users.
3. **Accent colour correction:** the pasted prompt's `#4D7CFE` fails its own AA rule as text (~3.4:1 on the light
   canvas). The shipped AA-tuned accent (`223 75% 47%` light / `221 100% 69%` dark) is retained.
4. Still open, non-blocking: count-badge colour (task/notification counts are accent-blue, not red) — carried from the
   UI phase for a later call.
