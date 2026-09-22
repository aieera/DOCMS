# SeDoc web — Dashboard redesign (design spec)

**Date:** 2026-09-22 · **Branch:** `neu-ui` · **Status:** approved (canvas), data sourcing corrected
**Canvas:** https://claude.ai/code/artifact/3f50ecfa-9fb0-4b23-a45f-effde9fad6b6

The user approved the four-artboard canvas ("yes i like the design we can proceed with this on our dashboard /
make sure everything is working perfect"). This spec is that design after every widget was traced to a real data
source in this repo. Three widgets changed as a result — see §2.1. Nothing ships on invented data.

## 0. Constraints (verbatim, carried from the UI and UX phases)

- "we dont want tomake any change tothelogic we need to make only the ui change"
- "**CRITICAL CONSTRAINT:** DO NOT alter, optimize, or modify any underlying DMS business logic, state
  management, routing, or API integrations. All structural logical flows must remain 100% intact."
- "I repeat we are not going to touch the logic but we are onlu going to change the design of the dms"

| Allowed | Forbidden |
|---|---|
| New presentational components under `web/src/components/dashboard/` | **Zero** files outside `web/` |
| One **additional read-only call to the existing `POST /search`** endpoint, with parameters the endpoint already accepts | New endpoints, changed request/response shapes, proto changes |
| Client-side derivation from data already returned (facet buckets, task fields) | New business rules, new mutations, writes of any kind |
| Presentational hooks (`useCountUp`, `usePrefersReducedMotion`) | New npm dependencies |
| Tailwind classes, CSS custom properties, keyframes | Changes to `globals.css` **tokens** (new keyframes only) |

`POST /search` is read-only and already called by `/search`, the command palette, and the ask page. Calling it
from the dashboard adds no endpoint, no contract change, and no write path. It is the same integration, used
again.

**Hard gate (unchanged):** tsc, `npm run lint` (eslint + rtl + utf8 + tenant), vitest, and the ADR 0120 axe
a11y gate in `web/e2e/70-a11y.spec.ts` all stay green, light **and** dark.

## 1. Goals / non-goals

**Goals**
1. A dashboard that answers *what needs me → what I have → how it's moving → who's doing it* in one screen.
2. Charts that are readable, themed, and animated without costing frame budget.
3. Every widget ships **three honest states**: loading (geometry-matched skeleton), empty (with the next
   action), failed (with Retry) — never the empty state on failure.
4. Works at 390px through 1440px+, light and dark, RTL-safe, keyboard reachable.

**Non-goals**
- No navigation/IA change. Routes, labels and the rail stay as they are.
- No new data. If a widget can't be sourced from what exists, it is not built (see §2.1).
- No replacement of `PendingSuggestionsCard` or `DashboardUploadDialog` — both are kept and re-placed.

## 2. Data sourcing — verified against the repo

Everything below was traced to code, not assumed.

| Widget | Source | Verified at |
|---|---|---|
| Documents total | `sum(getWorkspaces()[].document_count)` | `web/src/types/api.ts:29` — `Workspace.document_count` |
| Open tasks, overdue | `listMyTasks(false)` → `status`, `due_at` | `web/src/api/tasks.ts` |
| Awaiting approval | same list, `source === 'workflow'` | `web/src/api/tasks.ts:15` — `TaskSource` |
| Unread notifications | `getUnreadCount()` | `web/src/api/notifications.ts` |
| Documents added per month | `created_at` facet — **date_histogram, `calendar_interval: month`** | `services/search/internal/opensearch/facets.go:70-73` |
| Lifecycle donut | `lifecycle_state` facet (terms, size 10) | `facets.go:65` |
| File types | `doc_type` facet (terms on `mime_type`, size 20) | `facets.go:59` |
| Top contributors | `author` facet (terms on `created_by_name`, size 20) | `facets.go:61` |
| Needs your attention | tasks (overdue/urgent) + unread `getNotifications({limit:'8'})` | already on the dashboard today |

**One request feeds four widgets.** `POST /search` accepts `facets: string[]` (`handler.go:104`) and an empty
query builds `match_all` (`opensearch/query.go:90`), so a single facet-only call returns the histogram, the
lifecycle breakdown, the file types and the contributors together.

`page_size: 0` is **coerced to `DefaultPageSize`** server-side (`service/service.go:281-283`), so the "facets
only" trick doesn't work — the call uses `page_size: 1` and ignores `results`.

### 2.1 Corrections to the approved canvas

Three widgets on the canvas could not be built honestly as drawn. Each is corrected, not quietly dropped.

1. **"Storage used" KPI — removed.** There is no tenant-wide byte total a normal user can read. `Workspace`
   carries `document_count` but no size (`types/api.ts:25-41`), and `storage_by_region` lives behind
   `GET /api/v1/admin/compliance/overview` (`services/document/cmd/server/main.go:811`), which is admin-gated.
   Showing a storage figure would have meant inventing one or breaking the page for non-admins.
   **Replaced by "Unread notifications"**, which the dashboard already fetches.

2. **Per-KPI sparklines — only where a real series exists.** The canvas gave all four KPIs a sparkline. Only
   *Documents* has a genuine time series (the `created_at` histogram). Tasks, approvals and notifications have
   no historical series anywhere in the API. They get an **honest secondary figure** instead — "3 overdue",
   "oldest waiting 5 days", "2 mentions" — computed from fields already present on the rows. No decorative
   micro-charts drawn from fabricated arrays.

3. **Activity chart is monthly, not a 30-day daily line with a 7d/30d/90d toggle.** The histogram interval is
   hardcoded to `month` server-side (`facets.go:72`). Changing it would be a backend change, which is out of
   scope. The widget becomes **"Documents added — last 12 months"**, a real series with a real axis. The range
   toggle is dropped because the underlying buckets don't support it.

   The canvas's Spec board already flagged the trend line in amber as "the one gap"; this resolves it in the
   honest direction — a real monthly series rather than a fake daily one.

4. **Documents total comes from workspaces, not search `total_count`.** Search reflects the OpenSearch index,
   which lags ingestion; `document_count` is Postgres-authoritative and already fetched. The facet-derived
   widgets are therefore labelled **"of indexed documents"** so a lagging index reads as a scope note rather
   than a contradiction of the KPI.

### 2.2 Degradation

The search call is **additive and non-blocking**. If it fails or OpenSearch is down, the four facet widgets each
render their own failed state with Retry; the KPI strip, Needs-attention, quick actions and upload are
untouched. The dashboard never goes blank because one panel failed.

## 3. Layout

```
PageHeader (greeting + Upload)                     ← unchanged
KPI strip           4 up ≥lg · 2×2 sm · 1 col base
PendingSuggestionsCard                             ← unchanged, kept
Documents added (2/3)        │ Lifecycle (1/3)
Needs your attention (1/2)   │ File types (1/4) │ Contributors (1/4)
Quick actions                                      ← unchanged, moved below the fold
```

Mobile (<640px): single column; KPIs 2×2; charts keep 200px height; rows ≥48px; the lifecycle donut degrades
to a stacked proportion bar, which stays legible at 330px where a donut's labels collide.

## 4. Motion

| Element | Property | Duration |
|---|---|---|
| Card entrance | `transform: translateY(14px)` + `opacity` | 500ms, 60ms stagger |
| Sparkline / area path | `stroke-dashoffset` | 1.2s |
| Donut arcs | recharts `animationBegin` stagger | 150ms apart |
| Bars | `transform: scaleX()` from `transform-origin: inline-start` | 700ms |
| KPI numeral | count-up via `requestAnimationFrame` | 600ms |

Easing `cubic-bezier(.4, 0, .2, 1)` throughout. **Only `transform`, `opacity` and `stroke-dashoffset` animate** —
never `width`, `height` or `top`. Entrances run once per mount, not on refetch. `usePrefersReducedMotion` makes
every animated component render its final state immediately; the global `@media (prefers-reduced-motion)` block
at `globals.css:229` is the backstop, not the mechanism.

RTL: bars grow from `transform-origin: inline-start`, so they grow rightward in LTR and leftward in RTL without
a direction check. Physical-direction Tailwind classes are banned by `lint:rtl`.

## 5. Theming

Recharts takes colors as props, not classes, so chart colors are read from the live CSS custom properties via
`getComputedStyle(document.documentElement)` and rebuilt when the `.dark` class on `<html>` changes
(`MutationObserver` on `class`). This is the fix for the existing `ComplianceDashboard.tsx:3` pattern, which
hardcodes `#1E40AF` etc. and therefore does not theme.

Series colors derive from the single accent (`--primary`) at varying lightness plus `--muted-foreground`, so the
dashboard stays inside the one-accent system the neumorphic phase established.

## 6. Accessibility

1. Every chart is `role="img"` with an `aria-label` stating the trend in words ("Documents added per month,
   rising from 12 in October to 48 in September").
2. Color is never the only encoding — every series is labelled and every bar prints its figure.
3. Each chart is followed by a visually-hidden `<table>` giving the same numbers, so the data is reachable by a
   screen reader rather than announced as one opaque image.
4. KPI tiles are links with a visible `:focus-visible` ring; touch targets ≥44px.
5. Text on tinted surfaces must pass AA — the tinted-pair rule from the UI phase (`neu-tokens.test.ts`) holds.
   No `text-<color>` on `bg-<color>/NN`.

## 7. Testing

- **Unit (vitest):** derivation from facet buckets (empty, single-bucket, missing-facet, unknown lifecycle
  state); count-up honours reduced motion; each widget renders error-not-empty on failure; month labels.
- **Existing gates:** `src/test/a11y.test.tsx`, `e2e/70-a11y.spec.ts` (light + dark), keyboard + RTL specs.
- **Manual:** 390 / 768 / 1440 in both themes; OpenSearch stopped to confirm graceful per-panel degradation.

## 8. Out of scope (recorded, not done)

- A **daily** activity series — needs `calendar_interval` to be a request parameter on the search API.
- Storage-used for non-admins — needs a tenant storage total on a non-admin endpoint.
- Per-KPI trend arrows for tasks/approvals/notifications — needs historical snapshots that are not stored.
