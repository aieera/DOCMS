# Design system, app shell and dashboard — redesign (step 1 of the UI/UX redesign)

Date: 2026-10-25 · Status: approved in conversation, pending written review · Base: `main` @ 76ce9f95

## 1. Goal

Make SeDoc look like the approved dashboard mock ("SeDoc dashboard", Geist + flat neutral palette, light and dark) without changing what the product does. This step delivers the three things every later screen depends on: the **design tokens**, the **app shell** (sidebar + header) and the **dashboard** rebuilt to the mock. Other routes keep working and inherit the new tokens; they are restyled screen by screen in later steps as designs arrive.

Success: the dashboard matches the mock at 1440×900 in light and dark; the shell matches on every route; Arabic/RTL still mirrors correctly; the axe gate, 863 unit tests and the Playwright journeys stay green (re-targeted, never deleted); no new runtime dependency on third-party hosts.

## 2. Scope

**In:** `web/src/styles/globals.css` tokens, `web/tailwind.config.js`, self-hosted Geist, `web/index.html` font links, `components/layout/{app-layout,app-sidebar,app-topbar,theme-toggle}`, `components/dashboard/*`, `routes/_authenticated/index.tsx`, `routes/_authenticated/ask.tsx` (reads `?q=`), the shadcn primitives' size/radius defaults, i18n keys (en + ar), unit tests and E2E specs that touch the shell or dashboard, `web/docs`/ADR note.

**Out (later steps):** every other route's content, the Admin frame, the login/auth shell, mobile app, e-mail templates, the Crextio kit except where the dashboard used it (`WarmCard` in Quick actions), new data/API work.

## 3. Design system

### Tokens (`globals.css`, `:root` and `.dark`)
Values are hex/rgb exactly as in the mock; Tailwind maps them 1:1 (`colors.background = 'var(--background)'`, etc.), so existing `bg-card`, `text-muted-foreground`, `border-border` classes keep working.

| token | light | dark |
|---|---|---|
| background / foreground | #ffffff / #0a0a0a | #0a0a0a / #fafafa |
| card, popover (+ -foreground) | #ffffff / #0a0a0a | #171717 / #fafafa |
| primary / primary-foreground | #171717 / #fafafa | #e5e5e5 / #171717 |
| secondary, muted, accent (+ -foreground) | #f5f5f5 / #171717 | #262626 / #fafafa |
| muted-foreground | #6f6f6f | #a1a1a1 |
| destructive / destructive-foreground | #d6000a (mock #e7000b, darkened at the same hue for AA; see below) / #ffffff | #ff6467 (mock #e7000b; see below) / #0a0a0a |
| border, input | #e5e5e5 | rgb(255 255 255 / .10), rgb(255 255 255 / .15) |
| ring | #737373 (mock #a1a1a1; see below) | #737373 |
| outline-bg (outline buttons, inputs) | #ffffff | rgb(255 255 255 / .045) |
| chart-1…5 | #f54900 #009689 #104e64 #ffb900 #fe9a00 | #1447e6 #00bc7d #fe9a00 #ad46ff #ff2056 |
| sidebar, sidebar-foreground, sidebar-accent, sidebar-accent-foreground, sidebar-border | #fafafa #0a0a0a #f5f5f5 #171717 #e5e5e5 | #171717 #fafafa #262626 #fafafa rgb(255 255 255 / .10) |
| shadow-xs / sm / md / lg | 0 1px 2px 0 rgb(0 0 0/.05) · 0 1px 3px 0 rgb(0 0 0/.1),0 1px 2px -1px rgb(0 0 0/.1) · … | darker variants as in the mock |
| radius | 10px (sm 6, md 8, lg 10, xl 14, full 9999) | same |

The neumorphic `--nm-*` variables, the `.nm-*` utilities and the `crextio` display font (Fraunces) are removed. Existing `hsl(var(--x))` usages are migrated to `var(--x)` (tokens become colour strings, not HSL triples) — the Tailwind mapping change is global and mechanical.

### Type
Geist Sans for UI, Geist Mono for code/ids, **self-hosted**: the `@fontsource/geist` and `@fontsource/geist-mono` packages (OFL) provide the woff2 files (`geist` on npm ships only Next.js font modules); `globals.css` declares `@font-face` from the package assets so Vite bundles them. No `fonts.googleapis.com` links remain in `index.html`. Scale: page h1 24/32 semibold −0.025em; card title 16/20 semibold −0.01em; body 14/20; caption 12/16; metrics 28/32 semibold with `font-variant-numeric: tabular-nums`.

### Primitives (shadcn wrappers)
Button: default 36 px, sm 32 px, radius 8, `primary` = `--primary` on `--primary-foreground`, `outline` = 1 px `--border` on `--outline-bg` with `--shadow-xs`, `ghost` transparent; hover = `--accent`; focus-visible = 1 px ring + 4 px `ring/50` halo. Card: radius 14, 1 px `--border`, `--shadow-sm`, padding 20/24. Input/textarea: 36 px, radius 8, `--input` border, `--outline-bg`. Kbd: 22 px, radius 4, `--muted`. Badge count: 18 px pill, `--primary`.

### Theme
The existing `ThemeProvider` (light/dark/system, `.dark` on `<html>`, localStorage) is the single mechanism; the header toggle calls it. Dark is shipped with this step.

### Accessibility and RTL
Text token pairs meet WCAG AA at body size in both themes (muted-foreground light: 5.0:1 on background, 4.6:1 on muted; dark: 7.7:1 on background, 5.9:1 on muted). The mock's light muted-foreground #737373 (4.35:1 on #f5f5f5) is lifted to #6f6f6f so muted text also passes AA on muted surfaces.

Amended in the final fix wave (the pairs are enforced in `theme-contrast.test.ts`):
- **Focus ring** (non-text, WCAG 1.4.11, 3:1): light `--ring` is #737373, 4.7:1 on background and card (the mock's #a1a1a1 was 2.6:1). Dark #737373: 4.2:1 on background, 3.8:1 on card.
- **Light destructive** is #d6000a (the mock's #e7000b at the same hue, slightly darker): error text 5.4:1 on card and 4.5:1 on its own 10% tint (#e7000b gave 4.0:1 there), 5.0:1 on muted; white text on it 5.4:1.
- **Dark destructive** is #ff6467 with a dark `--destructive-foreground` #0a0a0a. Error text: 6.2:1 on card, 6.9:1 on background, 5.2:1 on muted, 5.5:1 on its own 10% tint (the mock's #e7000b was 3.8:1 on card). Text on red (buttons, badges, confirm dialogs): 6.9:1. No dark red can serve both as error text on the card (luminance at least 0.214) and under white text (at most 0.183; white on #ff6467 is 2.9:1), so dark text on a light red is the dark-theme treatment. Logical properties (`ms-`, `pe-`, `start-`) everywhere; icons mirrored where the current code mirrors them. The axe gate (ADR 0120) is the acceptance test.

## 4. App shell

**Sidebar** (`app-sidebar.tsx`): 240 px, `--sidebar` background, 1 px `--sidebar-border` end edge. Brand row 56 px: 32 px tile (`--primary`, radius 8, SeDoc mark) + "SeDoc" 14/18 semibold over "Document management" 12/16 muted. Nav groups Workspace (Dashboard, Workspaces, Shared with me, Ask, Clauses, Reports*), Integrations (Integrations*), Inbox (Tasks, Trash), Settings (My settings, Admin*) — * role-gated exactly as today; group label 32 px / 12 px medium muted; item 32 px, 16 px icon, 8 px gap, radius 8, 14 px; active = `--sidebar-accent` bg + `aria-current="page"`; hover = same bg. Settings group sits at the bottom (`margin-top: auto`). Smart folders and the workspace selector keep their current position and behaviour inside the Workspace group. Collapsed mode (icons only, 56 px) stays; mobile ≤ 800 px uses the existing `MobileSidebar` sheet.

**Header** (`app-topbar.tsx`): 56 px, 1 px bottom border, `--background`. Left: 32 px ghost toggle (panel-left icon), 16 px vertical divider, breadcrumb (`Home` with house icon › current page, 14 px, muted → foreground on the current item). Right: search trigger 280×36 (outline, magnifier, "Search documents…", `Ctrl K` kbd; hidden ≤ 800 px; opens the existing command palette/search dropdown), tasks 36 px ghost, notifications 36 px ghost with count badge (existing panel), language 36 px ghost with label + chevron (existing `LanguageSelector`; hidden ≤ 800 px), theme toggle 36 px ghost (moon/sun), divider, 32 px avatar button (initial on `--muted`; existing account menu).

**Layout** (`app-layout.tsx`): `main` scrolls; content padding 24 px (16 px ≤ 800 px); no max-width container on the dashboard (12-col grid uses the full width).

## 5. Dashboard (`routes/_authenticated/index.tsx`)

Grid: 12 columns, 16 px gap; rows: head · KPIs · chart+attention · library+ask. Breakpoints: ≤ 1180 px KPIs span 6, chart/library full width, attention/ask span 6; ≤ 800 px everything full width.

1. **Head**: h1 "Good morning/afternoon/evening, {first name}" (existing `greet()`), subline "Here's what's happening across your {n} workspaces." (n from the workspaces query; singular/plural via i18n); actions: *Ask a question* (outline → `/ask`) and *Upload* (primary → existing `DashboardUploadDialog`).
2. **KPI cards** (span 3 each, `<a>`-cards with hover border): Documents (count + "Across {n} workspaces" + 12-month sparkline bars) → `/workspaces`; Open tasks ("Assigned to you") → `/tasks`; Awaiting approval ("Nothing pending" / "{n} waiting") → `/tasks?filter=approvals`; Unread ("Notifications") → opens the notifications panel. Data: existing `useDashboardMetrics`, `tasks`, `notifications` hooks; loading = skeleton, error = the current `kpi-failed` state (test ids kept).
3. **Documents added** (span 8): title, subline "Indexed documents per month, {from} – {to}", stats "Last 12 months {total}" and "Busiest month {name}", Recharts `BarChart` (chart-1 bars, radius 6/2, dashed horizontal gridlines, value label above non-zero bars, 12 px axis labels, `aria-label` summary) + the existing hidden `ChartDataTable` for screen readers. Reduced motion respected.
4. **Needs your attention** (span 4): list rows (32 px icon tile, title, subline, trailing outline button): AI tag suggestions pending → "Review" (`/admin/tagging` or the existing review route), unread notifications → "Open" (panel), overdue tasks / approvals waiting → "View"; empty row with check icon "No overdue tasks or approvals waiting". "View all" → `/tasks`.
5. **Library breakdown** (span 8, three columns divided by 1 px borders; stacked ≤ 800 px): Lifecycle donut (88 px ring, chart-1…, legend with counts, caption), File types (8 px stacked bar + list with count and %), Top contributors (avatar initial, name, count, %, 6 px progress). Data: existing facets from `useDashboardMetrics`.
6. **Ask a question** (span 4): icon tile, title, "Get answers from the documents you can see", label + 3-row textarea (placeholder "e.g. Which documents are still in draft?"), footer "Press Enter to ask" + primary *Ask* button; Enter or the button navigates to `/ask?q=<text>`; `/ask` gains a `validateSearch` for `q` and submits it on mount.

The current `QuickActions` (WarmCard) and the standalone `PendingSuggestionsCard` are removed from the page; their functions live in Upload/Ask (head) and the attention list.

## 6. Code structure

- `web/src/styles/globals.css` — tokens, fonts, base; no component CSS.
- `web/src/components/layout/` — `app-sidebar.tsx`, `app-topbar.tsx`, `app-layout.tsx` rewritten in place (same exports, same props) so routes are untouched.
- `web/src/components/dashboard/` — `KpiCard.tsx` (replaces `KpiTile`), `DocumentsAddedChart.tsx` (replaces `ActivityChart`), `AttentionList.tsx` (replaces `NeedsAttention`), `LibraryBreakdown.tsx` (wraps `LifecycleDonut`, `BreakdownBars`, contributors), `AskCard.tsx` (new), `DashboardHead.tsx` (new); `WidgetCard.tsx` becomes the mock's card frame. `metrics.ts`, `useDashboardMetrics.ts`, `chartTheme.ts`, `ChartDataTable.tsx`, `Sparkline.tsx` are kept (presentation-free).
- Each component: one purpose, props in, no data fetching except the page-level hooks passed down; unit tests next to them.

## 7. i18n

Every new string goes through `t()` in the `common` namespace (`dashboard.*`, `sidebar.*`, `header.*` keys), added to `en.json` and `ar.json` (Arabic translations supplied in the same commit; the i18n catalogue check must pass).

## 8. Testing

- Unit (vitest): token mapping renders (snapshot of computed CSS variables light/dark), each dashboard component with loading/empty/error/data states, `greet()`, `/ask` picks up `?q=`.
- a11y: axe gate on dashboard in light, dark, RTL.
- E2E: 01-login (lands on dashboard, selectors updated), 08 and 36–51 shared shell steps (nav, search trigger, notifications), 70-a11y, 72-rtl-geometry, i18n-rtl-shell — re-targeted to the new DOM/test ids; no journey deleted.
- Visual: screenshots at 1440×900, 1180 and 390 px, light/dark/RTL, attached to the PR.
- Gates: `tsc`, lint, 863+ tests, build, Playwright, the vite dev-proxy coverage test.

## 9. Migration order (one commit each, reviewable alone)

1. Tokens + Tailwind mapping + `hsl()` migration (whole app changes colour; nothing else) · 2. Geist self-hosted, Google Fonts links removed · 3. Primitive sizes/radii · 4. Sidebar · 5. Header + layout · 6. Dashboard head + KPI cards · 7. Chart · 8. Attention list · 9. Library breakdown · 10. Ask card + `/ask?q=` · 11. i18n ar · 12. E2E re-targeting · 13. Remove dead Crextio/neumorphic code.

## 10. Risks

- Global token change exposes hard-coded colours on other routes → they are tolerated this step (they still render; listed in the hand-back) and fixed per screen later.
- Geist via npm must be verified to bundle offline (no CDN) — checked in step 2.
- Chart replacement changes the shape of existing `ActivityChart` tests → rewritten with the component.
