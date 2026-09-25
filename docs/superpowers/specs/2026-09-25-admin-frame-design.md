# Admin area: a shared page frame

**Date:** 2026-09-25
**Scope:** `web/src/routes/_authenticated/admin/**` (79 route files, 69 real pages)
**Kind:** UI only. No business logic, state, routing, query, mutation or API change.

## 1. Intent

What was asked: the admin area has many sub-modules; align them, remove the
empty space, make the UX easy and the UI strong. Design only — the logic is
not to be touched.

What that means here, as agreed in brainstorming:

- **Build a shared frame first**, then migrate every admin page onto it. Deep
  redesigns of individual pages, and any rethink of admin navigation (a
  persistent admin side-nav instead of hub-and-breadcrumbs), are explicitly
  **later sub-projects**, not this one.
- **Approach A — a component frame** (`<AdminPage>` / `<AdminSection>`)
  rather than a layout route. Chosen because routes carry no `staticData`, so
  a layout route could never unify the header, which is the largest defect.
  Enforcement comes from a lint guard instead.

Success looks like: one way to be an admin page; one `h1` per page; tabs in
the header; forms in a readable measure and lists at full width; the hub
shows every section without scrolling; every existing test still passes.

## 2. Findings that drive the design

Measured on the dev server at 1920×1000 across 17 representative pages.

| Finding | Evidence |
|---|---|
| No shared page container | 14 distinct root conventions: `space-y-4`, `space-y-6`, `mx-auto max-w-{3,4,5,6,7}xl p-6`, bare `max-w-*`, none |
| Double / triple headers on merged pages | `/admin/identity` renders two tab rows, then the child's own page-scale `h1` "Users", then a description, then a banner, before content |
| Centred outliers inside full-width tabbed pages | `/admin/tenant/encryption` 896px with 382px gutters; `/admin/integrations` 1024px with 318px; `/admin/subscription` 976px — tab bar left-aligned, content centred beneath it |
| Label-to-control distance | Tenant settings: 8 feature-flag rows as 1596px cards, checkbox ~1400px from its label |
| Empty states scaled to the column, not the message | SSO empty state 1596×240 |
| Two tab styles | Canonical shadcn pill tabs everywhere; hand-rolled `border-b-2` underline buttons in `integrations/index.tsx` (lines 220–227) |
| Two card styles | `@/components/ui/card` (rounded-2xl `shadow-neu`, 73 uses) vs 8 hand-rolled `rounded-lg border border-border bg-card` |
| Hub is a brochure | 21 cards in a 3-column grid, all icons the same primary tint, Intelligence and Platform groups below the fold; the Integrations group is one card in a 3-slot row |
| Primary action detached from title | "Add user", "New connection", "Save" float at the far right of a 1596px header |

What works and is kept verbatim: the 2026-07 consolidation into `?tab=`-driven
merged pages with back-compat redirects; the hub's grouping taxonomy;
breadcrumbs; the info banners' content.

## 3. Goals and non-goals

**Goals**
1. One shared frame every admin page renders through.
2. Exactly one `h1` per page; tabs inside the header; no child page draws a
   page header.
3. Width decided by page kind, applied by the frame, never by the page.
4. The hub shows all sections at 1000px without scrolling.
5. Mechanically enforced: CI fails if an admin route bypasses the frame.
6. No regression in the nine admin e2e specs, the a11y gate or the RTL gate.

**Non-goals**
- Any change to `validateSearch`, `navigate`, redirects, route paths, queries,
  mutations, or `data-testid` values.
- Deep redesign of any page's *content* (tables, forms, dialogs).
- Admin navigation model (side-nav vs hub).
- Translating admin copy (the i18n plan was dropped separately).
- Touching `PageHeader`'s behaviour — it has 27 non-admin callers.

## 4. Design

### 4.1 `AdminPage` and `AdminSection`

Both live in `web/src/components/admin/`. Both are **pure presentation**: no
queries, no store access, no internal state. Tabs are controlled by the page.

```tsx
interface TabSpec {
  value: string
  onValueChange: (v: string) => void
  items: { value: string; label: ReactNode; testId?: string }[]
}

interface AdminPageProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode            // page-level actions only
  width?: 'measure' | 'full'     // default 'measure'
  tabs?: TabSpec                 // primary row, rendered in the header
  children: ReactNode
}

interface AdminSectionProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode            // right-aligned to the section, not the page
  children: ReactNode
}
```

Rendered structure of `AdminPage`:

```
<div data-testid="admin-page">
  <PageHeader title description actions noMargin />   ← unchanged component
  {tabs    && <Tabs …primary row…>}                    ← shadcn Tabs, controlled
  {subTabs && <Tabs …secondary row…>}
  <div data-admin-width={width}
       className="flex min-w-0 flex-col gap-6 {width==='measure' ? 'max-w-4xl' : ''}">
    {children}
  </div>
</div>
```

Rules:

1. **One `AdminPage` per route render; it owns the only `h1`.** It composes
   `PageHeader` as-is (`variant="page"`).
2. **Tabs render in the header**, beneath the description, using the canonical
   `@/components/ui/shadcn/tabs`. The frame receives `value` and
   `onValueChange`; the page keeps its `validateSearch` and `navigate` calls
   exactly as today. No URL contract changes.

   **There is no `subTabs` prop** (changed during planning — see §8). Radix
   `TabsContent` binds to the *nearest* `Tabs` root, so a frame-owned second
   root around the column would capture the page's primary-level
   `TabsContent` and render nothing, and leave the selected primary trigger's
   `aria-controls` pointing at a missing id. Identity keeps its inner
   `<Tabs>` inside each primary `TabsContent`, as today; the frame supplies
   the `h1` and the primary row above it.
3. **`AdminSection` renders an `h2`** (text-lg, the same scale `PageHeader
   variant="section"` uses today) with its actions right-aligned to the
   section. Embedded children use it; they never render `AdminPage` or
   `PageHeader`.
4. **`measure` is `max-w-4xl` (896px), start-aligned.** The frame adds no
   `mx-auto`, no padding and no second cap; `app-layout` already supplies
   `p-4 sm:p-6 lg:p-8`. `full` spans the column. The content column is a flex
   column so a child that needs height (the metadata-schema builder,
   `min-h-0 flex-1`) keeps working without a special case.
5. **The frame owns vertical rhythm** (`gap-6` between header, tabs and
   content). Pages drop their own `space-y-*` / `p-6` / `mx-auto max-w-*`
   wrappers.
6. `data-testid="admin-page"` on the root and `data-admin-width` on the
   content column, so gates target the frame rather than each page.

### 4.2 Width policy

`measure` is the default. A page earns `full` only when its **primary**
content is a table, queue, log or card grid. Forms, key/value lists and
toggle lists never go full.

Merged containers pass `width` from the tab they already track — a one-line
branch on existing state, e.g. `width={sub === 'users' ? 'full' : 'measure'}`.

#### Containers (13) — width per tab

| Container | Tabs → width |
|---|---|
| `identity` | users **full** · groups measure · permissions **full** ‖ sso measure · ldap measure · scim **full** |
| `protection` | classification **full** · watermark measure · exports (IRM) **full** |
| `records-retention` | records **full** · retention **full** |
| `legal` | holds measure · ediscovery measure |
| `audit` | log **full** · forwarding (SIEM) measure |
| `subscription` | plan measure · license measure |
| `tenant-settings` | flags measure · upload measure |
| `ai` | provider measure · ner measure · models **full** · usage **full** |
| `tagging` | catalog **full** · thresholds measure · review measure |
| `ocr` | config measure · review **full** |
| `pii-scanning` | config measure · dashboard **full** |
| `data-governance` | residency **full** · compliance **full** · certification **full** |
| `integrations/index` | esign **full** · connectors **full** · webhooks **full** · email measure · events measure · mcp measure · ipaas measure |

#### Standalone pages (18) — own `AdminPage`

| `full` | `measure` |
|---|---|
| api-keys · share-links · workflows · metadata-schema · ingestion · permission-lag · tenant/sync · platform/db-info · intelligence/anomalies · intelligence/routing-rules · intelligence/filing-analytics | bulk · capture · privacy · mfa-policy · tenant/encryption · platform/load-tests · platform/support-search |

#### Embedded children (37) — `AdminSection`, width from container

users · groups · permissions · sso · tenant/identity/ldap · scim ·
tenant/classification · tenant/watermark · tenant/irm · records · retention ·
legal-holds · ediscovery · audit-log · siem · tenant/license · settings ·
tenant/upload-policy · tenant/ai · intelligence/ner-config ·
intelligence/models · intelligence/usage · tags · intelligence/auto-tag ·
intelligence/tag-review · intelligence/ocr-config · intelligence/ocr-review ·
intelligence/compliance-config · intelligence/compliance · residency ·
connectors · webhooks · integrations/email · integrations/events ·
integrations/mcp · integrations/ipaas · (billing's `BillingPage`, wherever it
is exported from — resolved during migration by inspection)

#### Untouched (9)

Redirect shims: routing · pii · billing · integrations-hub ·
intelligence/index · intelligence/anomaly-reports · tenant/index ·
tenant/identity/index. Layout route: `integrations.tsx`.

#### Overrides where the tag census and the eye disagree

1. **Encryption → `measure`.** Two tables (rotation history) but form-led.
   Today centred at 896; same width, start-aligned.
2. **Watermark and LDAP → `measure`.** 13 and 15 inputs each; the table is
   secondary.
3. **Routing rules, Records, Retention, Classification, Sync → `full`.**
   Table-dominant with a form attached.
4. **Metadata schema → `full`**, and it needs height (see rule 4).
5. **Permission propagation → `full`.** Metric cards with charts.

### 4.3 The hub (`admin/index.tsx`) and Platform hub (`admin/platform/index.tsx`)

`AdminPage width="full"` with a two-column directory (`grid gap-6
lg:grid-cols-2`). Left column: the Tenant administration group as one `Card`
with its four sub-group headings. Right column: Integrations, Intelligence and
(platform admins only) Platform, stacked.

New component `DirectoryGroup` in `components/admin/`:

```tsx
<DirectoryGroup label description>
  {/* optional */}<DirectoryGroup.Heading>People & access</DirectoryGroup.Heading>
  <DirectoryGroup.Row to icon label desc testId? />
</DirectoryGroup>
```

A row is a 44px `Link`: icon (`text-muted-foreground`, h-4), label
(`text-sm font-medium`), description (`text-xs text-muted-foreground
truncate`), chevron (`DirectionalIcon ChevronRight`, `rtl:rotate-180`). Rows
are separated by `divide-y`. Hover `bg-muted/40`. Icons are monochrome; the
identical primary tiles go.

Kept verbatim: every `to`, label and description string (including
`/templates`), the compliance-officer filtering via
`adminPathAllowsComplianceOfficer`, the `is_platform_admin` gate, the
`aria-labelledby` group wiring.

**Find a setting** — an `<Input>` in the header's `actions` slot. A single
local `useState<string>`; rows whose label or description contains the query
(case-insensitive) stay, groups with no matches collapse, an empty result
shows "No settings match". No fetch, no store. *Marked optional:* if this is
considered more than design, drop it; nothing else depends on it.

The Platform hub renders one `DirectoryGroup` at `measure`.

### 4.4 Visual conventions applied by the migration

- Tabs: shadcn `Tabs` only. The hand-rolled underline tabs in
  `integrations/index.tsx` are replaced by a nested shadcn `Tabs` with the
  same values and the same `data-testid`s.
- Cards: `@/components/ui/card` for surfaces; the 8 hand-rolled
  `rounded-lg border border-border bg-card` become `Card`. Raised
  (`shadow-neu`) means interactive/navigational; content surfaces stay flat.
- Badges: `Badge` variants from `badge.tsx` only; no bespoke pill classes.
- Empty states and info banners inherit the content column's width.
- Bidi: English copy that stays English in the Arabic UI carries
  `dir="auto"` on its *block* (title + description together), as established
  on the settings pages.

## 5. Enforcement

`web/scripts/check-admin-frame.mjs`, wired as `"lint:admin"` and appended to
the `lint` chain beside `lint:rtl`, `lint:utf8`, `lint:tenant`. Same shape as
`check-no-physical-tw.mjs`: walk `src/routes/_authenticated/admin/**/*.tsx`,
exit non-zero on any violation, print file:line.

Rules:

1. No `<PageHeader` in any admin route file. The frame composes it.
2. No `mx-auto max-w-` in any admin route file. The frame owns width.
   (Root padding is *not* a lint rule — `p-6` inside a `Card` is legitimate
   and a grep cannot tell the two apart. It is a review item in §6.)
3. Every non-shim, non-layout route file contains `<AdminPage` (it is a
   page) or `<AdminSection` without `<AdminPage` (it is an embedded child).
   A page may also use `AdminSection` for its own internal sections —
   Encryption has four. A child must never contain `AdminPage`. A file with
   neither is a violation.

Shim heuristic (exempt from 3): file contains `throw redirect` and is under
60 lines. Layout exemption: `integrations.tsx`.

The script accepts `--report`: print violations and exit 0. Batch 1 wires it
into `lint` with `--report`; batch 5 removes the flag and it fails the build.

Opt-out marker: a line `// admin-frame: exempt — <reason>` above the
offending line, for the same reason `check-no-physical-tw` has one — to make a
reviewer stop and ask, not to silence the rule.

## 6. Migration

Batches, each shipped green before the next starts:

1. **Frame.** `AdminPage`, `AdminSection`, `DirectoryGroup`, their unit tests,
   the guard script (initially reporting, not failing).
2. **Merged containers + children (13 + 37).** Container gets `AdminPage` with
   tabs in the header and per-tab width; each child swaps `PageHeader` →
   `AdminSection` and drops its wrapper. Biggest visible win: the double
   header disappears on twelve pages at once.
3. **Standalone pages (18).** Wrap in `AdminPage`, drop wrappers, set width
   from §4.2.
4. **Hubs (2).** Directory layout.
5. **Guard flips to failing.** `lint` goes red on any straggler.
6. **Gates.** Hub, Identity, Tenant settings, Encryption, Integrations, Users
   added to `e2e/72-rtl-geometry.spec.ts` `ROUTES`; hub and Identity added to
   `e2e/70-a11y.spec.ts` in light and dark. Full 98-route × 4-width RTL sweep.

Invariants checked at every batch:

- Route paths, redirects, `validateSearch`, `navigate` calls: byte-identical
  (`git diff` limited to JSX/className lines; any change outside them is a
  review stop).
- No page-level padding survives: a migrated page's root element carries no
  `p-*` (the shell pads). Checked by eye in review, since the guard cannot
  distinguish root padding from padding inside a card.
- Every `data-testid` in the touched file still present (grep before/after).
- Heading **names** unchanged. Three e2e specs assert
  `getByRole('heading', { name: /webhooks|event streaming|email ingestion/i })`
  on embedded children; the level moves from `h1` to `h2`, the accessible
  name must not.
- Standalone child URLs (`/admin/users`, `/admin/sso`, …) still redirect into
  their container.

## 7. Verification

**Unit (vitest, `components/admin/__tests__/`)**, in the style of
`PageHeader.test.tsx`:

- `AdminPage` renders exactly one `h1`; `AdminSection` renders an `h2` and no
  `h1`.
- Tabs are controlled: clicking a trigger calls `onValueChange` with the
  value and does not change the rendered active tab on its own.
- `width` sets `data-admin-width` and the `max-w-4xl` class only for
  `measure`; the content column is `flex flex-col`.
- `subTabs` renders a second `tablist`; without it there is one.
- `DirectoryGroup.Row` renders a link whose `href` is the given `to`, and
  the filter hides non-matching rows and collapses empty groups.

**Gates (each batch):** `tsc`, `npm run lint` (incl. `lint:admin`), 760+
unit tests, `npm run build`, then Playwright: `70-a11y`, `72-rtl-geometry`,
`38-llm-admin`, `42-federated-search`, `44-ldap-admin`, `53-esign-connectors`,
`54-pades-validity-badge`, `56-customer-webhooks`, `57-event-streaming`,
`58-email-ingestion`.

**Visual:** each batch's pages screenshotted at 1920 / 1440 / 768 / 390, light
and dark, English and Arabic, and looked at — the bidi punctuation and
alignment defects found on the settings pages are invisible to every gate.

**Proof the guard can fail:** before batch 5 flips it to failing, reintroduce
a `<PageHeader` in one migrated page and confirm `lint:admin` exits non-zero.

## 8. Decisions taken in brainstorming

- Frame first; navigation model and deep page redesigns deferred.
- Component frame (A) over layout route (B) or hybrid (C).
- `measure` is start-aligned, not centred.
- `subTabs` was agreed as a frame concern in brainstorming and **reversed in
  planning**: Radix `TabsContent` resolves to the nearest `Tabs` root, so the
  frame cannot own a second root without breaking the page's primary-level
  content and its `aria-controls`. Identity draws its inner row itself.
- The five census overrides in §4.2.
- The hub filter box is included but marked optional.

## 9. Follow-ups this spec does not cover

- A persistent admin side-nav (the "Navigation first" option).
- Deep redesigns of the worst pages once they share a frame: Identity,
  Tenant settings, Encryption, Integrations.
- `reports.tsx` and `templates.tsx` outside admin carry the same duplicate
  `p-6`; same fix, separate change.
- The four pre-existing `i18n-rtl-*` e2e failures that already make
  `frontend-e2e` red on this branch (auth hydration never completes under
  their mocks; `getByLabel(/Account menu/i)` times out). Unrelated, but they
  will mask a green run until fixed.
