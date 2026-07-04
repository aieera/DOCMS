# ADR 0120 — Accessibility gate (axe-core in Playwright) + WCAG AA fixes

- **Status**: Accepted
- **Date**: 2026-07-03
- **Relates to**: the Wave 13.4 Playwright harness (mocked-backend specs
  in `web/e2e`, gated by the `frontend-e2e` CI job)

## Context

The web app had no automated accessibility checking and had accumulated
AA violations: a theme whose muted text missed 4.5:1 by a hair
everywhere, 45%-opacity sidebar headers, white-on-emerald primary
buttons at 3.25:1, amber-on-amber badges at 1.74:1, icon-only buttons
without accessible names, a hand-rolled dialog that ignored Escape, and
an actions menu that could not be opened from the keyboard at all.

## Decision

### 1. Gate: `e2e/70-a11y.spec.ts` + `e2e/71-keyboard.spec.ts`

`@axe-core/playwright` scans (WCAG 2.0/2.1 A+AA tags) run over the core
flows POPULATED with mocked data — login, workspaces home, workspace
browse (docs+folders), search results, tasks inbox, saved searches,
templates gallery + editor dialog, reports builder with results. ANY
violation fails the test with a per-node summary. `71-keyboard`
verifies the keyboard contract: skip link (focus-visible + jumps to
`#main-content`), login by typing+Enter, task completion (the "approve"
surface) by focus+Enter, Escape closing dialogs, and the keyboard
alternative to drag-and-drop (actions menu → "Move to folder…";
dnd-kit's KeyboardSensor on the grip is the second path). Both specs
run in the existing `frontend-e2e` CI job (it executes the whole
`web/e2e` suite), so regressions can't land.

Spec pattern hardening: each spec registers a LOWEST-priority
`**/api/v1/**` abort catch-all before its mocks — on dev machines with
a live gateway, an unmocked poll (e.g. bare `/api/v1/notifications`)
otherwise reaches the real backend, 401s, and the app's interceptor
logs the test out mid-run (the source of long-standing local flakes in
older specs; adopting the same catch-all there is a follow-up).

### 2. Contrast: token-level fixes (light theme, measured by the gate)

- `--muted-foreground` 46%→39% lightness (was 4.38:1 — an AA hair-miss
  on every hint/label/tab in the app).
- `--primary` 43%→29%: same emerald hue, dark enough that white-on-
  primary passes even in the `hover:bg-primary/90` state, and
  primary-as-text passes on light surfaces.
- `--success` 40%→31% (text usage).
- New `--warning-strong` (35 90% 27%): the AA-safe TEXT tone for amber
  on tinted badges; `--warning` stays the surface tone paired with
  `--warning-foreground`. Badge `warning`/`in_review` variants use it.
- Sidebar section headers `/45`→`/70` opacity; `text-muted-foreground/70`
  usages (11 files) promoted to the full token — opacity fractions of a
  minimum-passing token are automatic violations.
- The topbar search input inherited a border-tone text color →
  explicit `text-foreground`.

### 3. Real behavioral bugs the gate caught (fixed)

- `DocumentActionsMenu`'s trigger called `e.preventDefault()` in its
  click handler; Radix composes child handlers with
  `checkForDefaultPrevented`, so **Enter/Space never opened the menu**
  (pointerdown masked it for mouse users). Now `stopPropagation()` only.
- `CreateTaskDialog` was a raw fixed overlay: no `role="dialog"`, no
  `aria-modal`, no label, **no Escape handling**. All added; a full
  focus trap (and migrating it to the shared Dialog) is a follow-up.
- `BrowserDetailsPanel` crashed on `workspace.document_count
  .toLocaleString()` when the field is absent (unguarded, unlike its
  sibling line) — the whole browse page white-screened.
- Icon-only buttons (template delete, editor chevron/removes, report
  filter add/remove) gained `aria-label`s; editor inputs that relied on
  placeholders gained labels.
- Global `prefers-reduced-motion` backstop in `globals.css` (individual
  `motion-reduce:` variants remain the first line).

### 4. Already present, verified rather than built

Skip link + `#main-content` landmark (app shell), dnd keyboard
alternatives (KeyboardSensor + MoveDocumentDialog), form labels on the
primary auth/search surfaces.

## Consequences

- The palette is slightly darker in light mode; dark-mode tokens were
  NOT adjusted (the gate scans light mode) — auditing dark mode is a
  follow-up.
- Scans cover the enumerated core flows; the document detail route and
  admin pages are not yet scanned (mock surface is heavy) — extend
  incrementally.
- WCAG items that need human judgment (screen-reader narration quality,
  focus order sensibility beyond mechanics) remain manual-test
  territory; the gate covers the machine-checkable layer.
