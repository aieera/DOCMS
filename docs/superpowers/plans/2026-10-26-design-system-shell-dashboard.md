# Design System, App Shell and Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restyle SeDoc to the approved dashboard mock (Geist, flat neutral palette, light + dark) by replacing the design tokens, rebuilding the app shell and rebuilding the dashboard, without changing what the product does.

**Architecture:** The tokens in `globals.css` become plain colour strings mapped 1:1 by Tailwind, so every existing `bg-card`/`text-muted-foreground` class re-themes the whole app in one commit. The shell components keep their exports and props, so routes are untouched. The dashboard page is recomposed from six small presentational components fed by the existing data hooks.

**Tech Stack:** React 18, Vite 6, Tailwind 3.4, shadcn wrappers under `web/src/components/ui/shadcn`, Recharts 2, TanStack Router/Query, i18next (`common` namespace), vitest 4 + Testing Library + jest-axe, Playwright + axe.

**Spec:** `docs/superpowers/specs/2026-10-25-design-system-shell-dashboard-design.md`

## Global Constraints

- All work in `web/`; run commands from `web/` (`npm test -- --run <file>`, `npm run lint`, `npx tsc --noEmit`, `npm run build`).
- No runtime dependency on third-party hosts: no `fonts.googleapis.com`/`gstatic` links remain in `web/index.html`; fonts come from npm packages bundled by Vite.
- Token values are the spec's hex/rgb strings exactly (spec §3 table); Tailwind maps `colors.x = 'var(--x)'` (no `hsl()`, no `<alpha-value>`).
- Only logical Tailwind utilities (`ms-`, `me-`, `ps-`, `pe-`, `start-`, `end-`); `npm run lint:rtl` enforces it.
- Every user-visible string goes through `t()` in the `common` namespace; every new key exists in both `public/locales/en/common.json` and `public/locales/ar/common.json`.
- Existing test ids and accessible names stay unless a task says otherwise: `kpi-failed`, `kpi-skeleton`, `kpi-hint`, `kpi-strip-error`, `header-upload-button`, `app-sidebar`, `user-avatar-button`, the KPI link names `"Documents: {n}, across {n} workspace(s)"`, `"Open tasks: {n}, …"`, `"Awaiting approval: {n}, …"`, `"Unread: {n}, notifications"`.
- Reduced motion: every Recharts element passes `isAnimationActive={!reduced}` from the existing `useReducedMotion` usage in `ActivityChart.tsx`.
- Each task ends with `npx tsc --noEmit && npm run lint && npm test -- --run` green and one commit; the subject line follows the existing `feat(web): …`/`refactor(web): …` style.

## Review Focus

1. A user whose display name is empty or a single word: the head must read "Good morning" with no trailing comma or "undefined" (Task 7 test).
2. A tenant with zero workspaces: the subline must use the plural form "0 workspaces" and the KPI link name must still be well-formed (Task 7 test).
3. The Ask card with whitespace-only input: Enter must not navigate; `/ask?q=` with an empty or over-long `q` must not auto-submit (Task 11 tests).
4. Dark mode set to `system` on a machine preferring dark at first paint: the `.dark` tokens must apply without a light flash, which is the existing provider's job; Task 1's snapshot test covers `.dark` token values, Task 13 covers the toggle wiring.
5. Arabic UI: Geist has no Arabic glyphs, so the self-hosted Arabic face must load for `lang="ar"` (Task 2 test asserts the `:lang(ar)` font stack).

---

### Task 1: Tokens and Tailwind mapping

**Files:**
- Modify: `web/src/styles/globals.css` (the `:root` and `.dark` blocks, lines 26–~160; the `--color-*` aliases at 81–91)
- Modify: `web/tailwind.config.js` (`theme.extend.colors`, `borderRadius`, `boxShadow`, new `screens`)
- Modify: the 10 files using `hsl(var(--x))` (listed by `grep -rl 'hsl(var(--' src`): `routes/_authenticated/reports.tsx`, `components/ui/crextio/{timer-ring,segmented-progress,metric-bar}.tsx`, `components/folders/BrowserTiles.tsx`, `components/dashboard/{LifecycleDonut,ActivityChart}.tsx`, `components/layout/{AuthHero,CursorGrid,auth-shell}.tsx`, `components/dashboard/chartTheme.ts` if it reads tokens
- Test: `web/src/styles/__tests__/tokens.test.ts` (create)

**Interfaces:**
- Produces: CSS custom properties on `:root`/`.dark` named exactly as the spec table (`--background`, `--foreground`, `--card`, `--card-foreground`, `--popover`, `--popover-foreground`, `--primary`, `--primary-foreground`, `--secondary`, `--secondary-foreground`, `--muted`, `--muted-foreground`, `--accent`, `--accent-foreground`, `--destructive`, `--destructive-foreground`, `--border`, `--input`, `--ring`, `--outline-bg`, `--chart-1`…`--chart-5`, `--sidebar`, `--sidebar-foreground`, `--sidebar-accent`, `--sidebar-accent-foreground`, `--sidebar-border`, `--shadow-xs`, `--shadow-sm`, `--shadow-md`, `--shadow-lg`, `--radius: 10px`, `--radius-sm: 6px`, `--radius-md: 8px`, `--radius-lg: 10px`, `--radius-xl: 14px`). The existing `--success`, `--warning`, `--warning-strong`, `--info` pairs are kept as hex (`#1f6b4c`/`#ffffff`, `#f59e0b`/`#1c1917`, `#92400e`, `#1447e6`/`#ffffff`) because other routes use them.
- Produces: Tailwind classes `bg-outline-bg`, `text-chart-1`…`5`, `shadow-xs|sm|md|lg` (mapped to the shadow tokens), `rounded-sm|md|lg|xl` = 6/8/10/14 px, screens `shell: '801px'` and `wide: '1181px'`.
- Produces: `--font-sans`/`--font-mono` are left for Task 2.

- [ ] **Step 1: Write the failing test** — `tokens.test.ts` loads `globals.css` as text (`import css from '../globals.css?raw'`), parses the `:root { … }` and `.dark { … }` blocks with a regex into `Record<string,string>`, and asserts:

```ts
expect(light['--background']).toBe('#ffffff')
expect(light['--muted-foreground']).toBe('#737373')
expect(light['--radius']).toBe('10px')
expect(dark['--background']).toBe('#0a0a0a')
expect(dark['--border']).toBe('rgb(255 255 255 / .10)')
expect(dark['--chart-1']).toBe('#1447e6')
expect(Object.keys(light)).not.toContain('--nm-dist')
expect(css).not.toMatch(/hsl\(var\(--/)
```

- [ ] **Step 2: Run it to verify it fails** — `npm test -- --run src/styles/__tests__/tokens.test.ts` → FAIL (`--background` is `220 22% 91%`).

- [ ] **Step 3: Rewrite the `:root` and `.dark` token blocks** with the spec §3 table values; delete every `--nm-*` variable and the `.nm-*` utility rules; rewrite the `--color-*` aliases as `var(--x)`; replace `hsl(var(--x))` with `var(--x)` in the 10 listed files and `hsl(var(--x) / 0.5)` with `color-mix(in srgb, var(--x) 50%, transparent)`.

- [ ] **Step 4: Rewrite `tailwind.config.js`**: `colors.<token> = 'var(--<token>)'` (plus `'outline-bg'` and `chart: { 1: 'var(--chart-1)', … }`), `borderRadius: { sm: 'var(--radius-sm)', md: 'var(--radius-md)', lg: 'var(--radius-lg)', xl: 'var(--radius-xl)', '2xl': '18px', '3xl': '24px', full: '9999px' }`, `boxShadow: { xs: 'var(--shadow-xs)', sm: 'var(--shadow-sm)', md: 'var(--shadow-md)', lg: 'var(--shadow-lg)', neu: 'var(--shadow-sm)', 'neu-sm': 'var(--shadow-xs)', 'neu-inset': 'none', 'neu-pressed': 'none', 'neu-lg': 'var(--shadow-md)', float: 'var(--shadow-lg)', 'float-sm': 'var(--shadow-md)' }` (the `neu-*`/`float*` names stay as aliases so other routes compile; Task 14 removes them), `theme.extend.screens = { shell: '801px', wide: '1181px' }`.

- [ ] **Step 5: Run the test, the suite and the build** — `npm test -- --run src/styles/__tests__/tokens.test.ts` → PASS; `npm test -- --run` → all green (fix any test asserting an old HSL string); `npm run build` → succeeds.

- [ ] **Step 6: Commit** — `git commit -m "feat(web): flat neutral design tokens, Tailwind maps var() 1:1"`.

### Task 2: Self-hosted fonts

**Files:**
- Modify: `web/package.json` (add `@fontsource/geist`, `@fontsource/geist-mono` and `@fontsource/ibm-plex-sans-arabic`)
- Modify: `web/index.html:8-10` (remove the three Google Fonts lines)
- Modify: `web/src/styles/globals.css` (font imports, `--font-sans`, `--font-mono`, the `:lang(ar)`/`[dir=rtl]` rule at ~line 194, base type scale)
- Modify: `web/tailwind.config.js` (`fontFamily`)
- Test: `web/src/styles/__tests__/fonts.test.ts` (create)

**Interfaces:**
- Produces: `--font-sans: 'Geist', system-ui, sans-serif`, `--font-mono: 'Geist Mono', ui-monospace, monospace`; Tailwind `font-sans`, `font-mono`; the `serif` family entry is removed. Arabic: `:lang(ar), [dir=rtl] { font-family: 'IBM Plex Sans Arabic', var(--font-sans) }`.
- Produces: base rules `h1 { font-size: 24px; line-height: 32px; font-weight: 600; letter-spacing: -0.025em }` only inside `@layer base` scoped to `.page-title` (not bare `h1`, other routes own their headings); utility classes `.text-metric` (28/32, 600, `font-variant-numeric: tabular-nums`) and `.text-caption` (12/16) in `@layer utilities`.

- [ ] **Step 1: Write the failing test** — `fonts.test.ts` reads `index.html` and `globals.css` as raw text and asserts:

```ts
expect(html).not.toMatch(/fonts\.googleapis|fonts\.gstatic/)
expect(css).toMatch(/@import ['"]@fontsource\/geist\/400\.css['"]/)
expect(css).toMatch(/:lang\(ar\)[^{]*\{[^}]*IBM Plex Sans Arabic/)
expect(css).toMatch(/--font-sans:\s*'Geist'/)
```

- [ ] **Step 2: Run it to verify it fails** — FAIL on the googleapis assertion.

- [ ] **Step 3: Install and wire the fonts** — `npm install @fontsource/geist @fontsource/geist-mono @fontsource/ibm-plex-sans-arabic@^5`; in `globals.css` add `@import '@fontsource/geist/400.css'; …/500.css; …/600.css; …/700.css; @import '@fontsource/geist-mono/400.css'; …/500.css; @import '@fontsource/ibm-plex-sans-arabic/400.css'; …/500.css; …/600.css;` at the top (before `@tailwind`), set the two font tokens, replace the Fraunces/Inter families in `tailwind.config.js`, remove the `serif` entry, delete the three `<link>` lines from `index.html`, add the `.page-title`/`.text-metric`/`.text-caption` rules.

- [ ] **Step 4: Verify the bundle is offline** — `npm run build && grep -rl "googleapis" dist/ | wc -l` → `0`; `ls dist/assets | grep -ciE 'geist|plex'` → ≥ 3 (woff2 files emitted).

- [ ] **Step 5: Run the fonts test and the suite** — both green. Any test that asserted `font-serif`/`Fraunces` is updated to the new family.

- [ ] **Step 6: Commit** — `git commit -m "feat(web): self-hosted Geist + IBM Plex Sans Arabic, no Google Fonts"`.

### Task 3: Primitive sizes and radii

**Files:**
- Modify: `web/src/components/ui/shadcn/button.tsx` (variants lines 18–41), `input.tsx:37-38`, `textarea.tsx`, `badge.tsx:15-19`, `web/src/components/ui/card.tsx:14-55`
- Create: `web/src/components/ui/shadcn/kbd.tsx`
- Test: `web/src/components/ui/__tests__/Button.test.tsx` (extend), `web/src/components/ui/__tests__/Kbd.test.tsx` (create)

**Interfaces:**
- Produces: `Button` sizes `default: 'h-9 px-4'` (36 px), `sm: 'h-8 px-3'` (32 px), `icon: 'h-9 w-9'`, `icon-sm: 'h-8 w-8'`; variants `default` = `bg-primary text-primary-foreground hover:bg-primary/90`, `outline` = `border border-border bg-outline-bg shadow-xs hover:bg-accent`, `ghost` = `hover:bg-accent`; base adds `rounded-md focus-visible:ring-1 focus-visible:ring-ring focus-visible:ring-offset-4 focus-visible:ring-offset-ring/50`.
- Produces: `Card` = `rounded-xl border border-border bg-card shadow-sm`, `CardHeader` padding `p-5 pb-4`, `CardContent` `p-5 pt-0`.
- Produces: `Input`/`Textarea` = `h-9 rounded-md border-input bg-outline-bg` (textarea: `min-h-[5rem]`).
- Produces: `Kbd` (`export function Kbd({ children, className }: { children: ReactNode; className?: string })`) renders `<kbd>` 22 px tall, `rounded-sm bg-muted px-1.5 text-[11px] font-medium text-muted-foreground`.
- Produces: `Badge` `count` variant: `h-[18px] min-w-[18px] rounded-full bg-primary px-1 text-[11px] text-primary-foreground`.

- [ ] **Step 1: Write the failing tests** — in `Button.test.tsx` add `it('renders the outline variant on the outline surface with the xs shadow')` asserting `className` contains `bg-outline-bg` and `shadow-xs`, and `it('default size is 36px tall')` asserting `h-9`; `Kbd.test.tsx`: `render(<Kbd>Ctrl K</Kbd>)` → `screen.getByText('Ctrl K').tagName === 'KBD'`.

- [ ] **Step 2: Run them to verify they fail** — `npm test -- --run src/components/ui/__tests__/` → FAIL (`shadow-neu-sm`, no `Kbd` module).

- [ ] **Step 3: Rewrite the variant maps and create `kbd.tsx`** per the Interfaces block; remove every `shadow-neu*`/`brightness` class from the five primitives.

- [ ] **Step 4: Run the tests and the suite** — green; `neu-button.test.tsx`/`neu-input.test.tsx` assert the old neumorphic classes: rewrite their assertions to the new classes (keep the behavioural cases).

- [ ] **Step 5: Commit** — `git commit -m "feat(web): primitive sizes, radii and surfaces per the design system"`.

### Task 4: Sidebar

**Files:**
- Modify: `web/src/components/layout/app-sidebar.tsx` (keep exports `AppSidebar`, `MobileSidebar`, `SidebarContent`, keep `NAV_GROUPS` and `isActive`)
- Modify: `web/src/components/layout/app-layout.tsx:36-39` (`lg:ps-[260px]` → `shell:ps-[240px]`, collapsed `shell:ps-14`)
- Modify: `web/public/locales/{en,ar}/common.json` (`sidebar.tagline`)
- Test: `web/src/components/layout/__tests__/app-sidebar.test.tsx` (create)

**Interfaces:**
- Consumes: `useUIStore().sidebarCollapsed`, `toggleSidebar`; `useAuthStore().user.role` for role gating (unchanged).
- Produces: `<aside data-testid="app-sidebar">` 240 px (`w-60`) or 56 px (`w-14`) collapsed, `bg-sidebar border-e border-sidebar-border`; brand row 56 px with `<div class="h-8 w-8 rounded-md bg-primary">` + "SeDoc" + `t('sidebar.tagline')` = "Document management"; groups rendered by `NavGroupBlock` with a 32 px label row (`text-xs font-medium text-muted-foreground`); items 32 px (`h-8 rounded-md px-2 text-sm gap-2`), active = `bg-sidebar-accent text-sidebar-accent-foreground` + `aria-current="page"`; the Settings group wrapper gets `mt-auto`; `SmartFoldersBlock` and `WorkspaceSelector` stay where they are inside the Workspace group.

- [ ] **Step 1: Write the failing test** — render `SidebarContent` inside a router memory history at `/tasks` with an `owner` user and assert: `screen.getByRole('link', { name: 'Tasks' })` has `aria-current="page"`; `screen.getByRole('link', { name: 'Admin' })` exists; `screen.getByText('Document management')` exists; with `collapsed` the link has no visible text (`queryByText('Tasks')` null) but `title="Tasks"`.

- [ ] **Step 2: Run it to verify it fails** — FAIL (no tagline).

- [ ] **Step 3: Rewrite the markup** of `BrandRow`, `NavGroupBlock`, `NavLink` and the outer `<aside>` to the Interfaces block; add `"tagline": "Document management"` to en and `"tagline": "إدارة المستندات"` to ar; update `app-layout.tsx` offsets.

- [ ] **Step 4: Run the test, suite, lint** — green (`lint:rtl` passes: only logical utilities).

- [ ] **Step 5: Commit** — `git commit -m "feat(web): sidebar rebuilt to the design system"`.

### Task 5: Header and layout

**Files:**
- Modify: `web/src/components/layout/app-topbar.tsx` (keep export `AppTopbar({ onOpenMobileNav })`; `CommandTrigger`, `MyTasksBadge`, `NotificationsDropdown`, `UserMenu` keep their behaviour)
- Modify: `web/src/components/layout/breadcrumbs.tsx:106-115` (home item = house icon + `t('header.home')`)
- Modify: `web/src/components/layout/theme-toggle.tsx` (single ghost button cycling light → dark → system, `aria-label={t('theme.toggle')}`)
- Modify: `web/src/components/layout/app-layout.tsx:43-44` (content wrapper `p-4 shell:p-6`, no `mx-auto` max width)
- Modify: `web/public/locales/{en,ar}/common.json` (`header.home`, `header.search_documents`)
- Test: `web/src/components/layout/__tests__/app-topbar.test.tsx` (create)

**Interfaces:**
- Consumes: `Kbd` from Task 3; `useTheme()` (`mode`, `resolved`, `setMode`).
- Produces: `<header>` 56 px (`h-14 border-b border-border bg-background`); left: sidebar toggle `Button variant="ghost" size="icon-sm"` with `PanelLeft` and `aria-label={t('a11y.toggle_sidebar')}` (key exists as `a11y.collapse_sidebar`/`expand_sidebar`; reuse those two), a `Separator orientation="vertical"` 16 px, `Breadcrumbs`; right: `CommandTrigger` rendered as `Button variant="outline"` 280×36 (`hidden shell:inline-flex`) containing `Search` icon, `t('header.search_documents')` = "Search documents…" and `<Kbd>{shortcutLabel('K')}</Kbd>`; `MyTasksBadge`; `NotificationsDropdown` (its count uses `Badge variant="count"`); `LanguageSelector` (`hidden shell:block`); `ThemeToggle`; `Separator`; `UserMenu` (avatar button 32 px, `data-testid="user-avatar-button"` kept).

- [ ] **Step 1: Write the failing test** — render `AppTopbar` with the existing test mocks (copy the `vi.mock` set from `widgets.test.tsx` lines 25–51) and assert: `screen.getByRole('button', { name: /search documents/i })` exists and contains a `kbd`; `screen.getByRole('button', { name: 'Toggle theme' })` toggles `document.documentElement.classList` to include `dark` after one click when the provider mode is `light` (wrap in `ThemeProvider defaultMode="light"`); `screen.getByRole('navigation', { name: 'Breadcrumb' })` contains a link named `Home`.

- [ ] **Step 2: Run it to verify it fails** — FAIL (no "Search documents…" trigger).

- [ ] **Step 3: Rewrite the header markup** to the Interfaces block; rewrite `ThemeToggle` as one button: click order `light → dark → system → light`, icon `Sun` when `resolved === 'light'` else `Moon`; add `"header": { "home": "Home", "search_documents": "Search documents…" }` to en and `{ "home": "الرئيسية", "search_documents": "ابحث في المستندات…" }` to ar.

- [ ] **Step 4: Run the test, suite, lint** — green; `ux-breadcrumbs.test.tsx` is updated if it asserted the old home label.

- [ ] **Step 5: Commit** — `git commit -m "feat(web): header and layout rebuilt to the design system"`.

### Task 6: Dashboard card frame

**Files:**
- Modify: `web/src/components/dashboard/WidgetCard.tsx` (same props)
- Test: `web/src/components/dashboard/__tests__/widgets.test.tsx` (the `WidgetCard` describes stay green)

**Interfaces:**
- Produces: `WidgetCard` renders `<section aria-labelledby>` as `rounded-xl border border-border bg-card shadow-sm`, header `px-5 pt-5 pb-3` with title `text-base font-semibold tracking-[-0.01em]` and optional subtitle `text-caption text-muted-foreground`, `action` slot end-aligned; body `px-5 pb-5`; loading/error/empty states unchanged (`aria-busy`, `role="status"`, Retry button).

- [ ] **Step 1: Run the existing WidgetCard tests** — `npm test -- --run src/components/dashboard/__tests__/widgets.test.tsx -t WidgetCard` → PASS (baseline).
- [ ] **Step 2: Rewrite the frame markup**; remove `dash-rise`/`shadow-neu`/`delayIndex` animation classes (keep the `delayIndex` prop accepted and ignored so callers compile until Task 14).
- [ ] **Step 3: Run the tests** — PASS.
- [ ] **Step 4: Commit** — `git commit -m "refactor(web): WidgetCard is the dashboard card frame"`.

### Task 7: Dashboard head and KPI cards

**Files:**
- Create: `web/src/components/dashboard/DashboardHead.tsx`, `web/src/components/dashboard/KpiCard.tsx`, `web/src/components/dashboard/greet.ts`
- Modify: `web/src/components/dashboard/KpiStrip.tsx` (use `KpiCard`; grid `grid-cols-1 shell:grid-cols-2 wide:grid-cols-4 gap-4`)
- Modify: `web/src/routes/_authenticated/index.tsx` (head replaces `PageHeader`; `greet` moves to `greet.ts`)
- Modify: `web/public/locales/{en,ar}/common.json` (`dashboard.subline_one`, `dashboard.subline_other`, `dashboard.ask_question`)
- Test: `web/src/components/dashboard/__tests__/DashboardHead.test.tsx`, `KpiCard.test.tsx` (create), `widgets.test.tsx` (`KpiTile` describes → `KpiCard`)

**Interfaces:**
- Produces: `export function greet(t: TFunction<'common'>, name: string | undefined): string` (moved verbatim from `index.tsx:77-88`).
- Produces: `export function DashboardHead({ greeting, workspaceCount, onUpload }: { greeting: string; workspaceCount: number | undefined; onUpload: () => void })` → `<h1 class="page-title">`, `<p>{t('dashboard.subline', { count })}</p>` (`subline_one`: "Here's what's happening across your {{count}} workspace.", `subline_other`: "… {{count}} workspaces."), actions: `Link to="/ask"` as `Button variant="outline"` "Ask a question" (`dashboard.ask_question`) and `Button data-testid="header-upload-button"` "Upload" (`dashboard.upload`, existing key).
- Produces: `export function KpiCard(props: KpiTileProps)` — same props as `KpiTile` (`icon, label, value, hint, hintTone, href, sparkline, isError`) plus `onClick?: () => void` (when set, renders a `<button>` instead of `Link`; used by Unread to open the notifications panel via a new `useUIStore().openNotifications()` — add `notificationsOpen: boolean`, `openNotifications`, `closeNotifications` to `uiStore.ts`, and make `NotificationsDropdown` in `app-topbar.tsx` controlled by it). Accessible name rule unchanged (`"{label}: {value}, {hint}"`); test ids unchanged; sparkline rendered as 12 bars (`Sparkline` is kept for other callers; `KpiCard` draws `<div class="flex h-6 items-end gap-0.5">` bars scaled to max).

- [ ] **Step 1: Write the failing tests** — `DashboardHead.test.tsx`: `greet(t, undefined)` returns `'Good morning'` with no comma (mock `Date` hour 9); `greet(t, 'Sulthan')` returns `'Good morning, Sulthan'`; `render(<DashboardHead greeting="Hi" workspaceCount={0} onUpload={fn} />)` shows "across your 0 workspaces"; `workspaceCount={1}` shows "1 workspace". `KpiCard.test.tsx`: `onClick` variant renders a `button` with the same accessible name; `href` variant renders a link; `isError` renders `kpi-failed`.

- [ ] **Step 2: Run them to verify they fail** — FAIL (modules missing).

- [ ] **Step 3: Implement** `greet.ts`, `DashboardHead.tsx`, `KpiCard.tsx`, the `uiStore` fields, the controlled `NotificationsDropdown`, and switch `KpiStrip` to `KpiCard` (Unread card: `onClick={openNotifications}`; Awaiting approval `href="/tasks?filter=approvals"`); add the three i18n keys to en and ar (`"subline_one": "إليك ما يحدث في مساحة العمل {{count}} الخاصة بك."`, `"subline_other": "إليك ما يحدث عبر مساحات العمل {{count}} الخاصة بك."`, `"ask_question": "اطرح سؤالاً"`).

- [ ] **Step 4: Run tests, suite, lint, tsc** — green; the `KpiTile` describes in `widgets.test.tsx` import `KpiCard` instead (same assertions).

- [ ] **Step 5: Commit** — `git commit -m "feat(web): dashboard head and KPI cards"`.

### Task 8: Documents added chart

**Files:**
- Create: `web/src/components/dashboard/DocumentsAddedChart.tsx`
- Modify: `web/src/components/dashboard/chartTheme.ts` (`series` = `['var(--chart-1)', …'var(--chart-5)']`, `grid` = `var(--border)`, `axis` = `var(--muted-foreground)`)
- Modify: `web/src/routes/_authenticated/index.tsx` (use it in place of `ActivityChart`)
- Test: `web/src/components/dashboard/__tests__/DocumentsAddedChart.test.tsx` (create; the `ActivityChart` describe in `widgets.test.tsx` moves here)

**Interfaces:**
- Consumes: `useDashboardMetrics()` (`activity: Point[]`, `isLoading`, `isError`, `isUnavailable`, `refetch`), `trendSummary(points)`, `ChartDataTable`, `WidgetCard`.
- Produces: `export function DocumentsAddedChart({ className }: { className?: string })` → `WidgetCard` titled `t('dashboard.widgets.activity_title')`, subtitle `t('dashboard.widgets.activity_range', { from, to })` ("Indexed documents per month, {{from}} – {{to}}", from/to = first/last `Point.label`), stats row `t('dashboard.widgets.last_12_months')` = "Last 12 months" + total, `t('dashboard.widgets.busiest_month')` = "Busiest month" + label of the max point; Recharts `BarChart` (`Bar dataKey="value" fill="var(--chart-1)" radius={[6,6,2,2]}` with `LabelList position="top"` for `value > 0`, `CartesianGrid strokeDasharray="4 4" vertical={false}`, axes 12 px), `role="img" aria-label={trendSummary(activity)}`, the hidden `ChartDataTable`; empty/error/unavailable copy as today.

- [ ] **Step 1: Write the failing tests** — move the three `ActivityChart` cases (failure with Retry, empty window copy, accessible table) to the new file against `DocumentsAddedChart`, add `it('shows the 12-month total and the busiest month')` with a mocked `activity` of 12 points where March is max.

- [ ] **Step 2: Run to verify they fail** — FAIL (module missing).

- [ ] **Step 3: Implement the component** and the three new i18n keys (en + ar: `"activity_range": "المستندات المفهرسة شهرياً، {{from}} – {{to}}"`, `"last_12_months": "آخر 12 شهراً"`, `"busiest_month": "الشهر الأكثر نشاطاً"`); keep `ActivityChart.tsx` in place until Task 14.

- [ ] **Step 4: Run tests, suite** — green.

- [ ] **Step 5: Commit** — `git commit -m "feat(web): Documents added bar chart"`.

### Task 9: Attention list

**Files:**
- Create: `web/src/components/dashboard/AttentionList.tsx`
- Modify: `web/src/routes/_authenticated/index.tsx` (use it; drop `PendingSuggestionsCard` from the page)
- Test: `web/src/components/dashboard/__tests__/AttentionList.test.tsx` (create)

**Interfaces:**
- Consumes: the queries and `Row` ranking from `NeedsAttention.tsx:25-62` (`listMyTasks`, `getNotifications`, `taskRank`, `taskDetail` — move these helpers into `AttentionList.tsx`), `listPendingTagSuggestions({ limit: 1 })` with query key `['pending-tag-suggestions-count']` from `PendingSuggestionsCard.tsx:21-23`, `useUIStore().openNotifications`.
- Produces: `export function AttentionList()` → `WidgetCard` titled `t('dashboard.widgets.attention_title')` with action link "View all" → `/tasks`; rows (`<li>` with a 32 px icon tile `rounded-md bg-muted`, title, subline, trailing `Button variant="outline" size="sm"`): suggestions pending → "Review" (`Link to="/admin/tagging"`, copy `dashboard.widgets.suggestions_waiting_*` existing), unread notifications count → "Open" (`onClick={openNotifications}`), then the top 3 ranked task rows → "View" (`Link to="/tasks"`); empty = `CheckCircle2` icon + `t('dashboard.widgets.attention_empty')`.

- [ ] **Step 1: Write the failing tests** — with the mocks from `widgets.test.tsx`: `it('lists pending suggestions first with a Review link to /admin/tagging')`, `it('offers Open for unread notifications and calls openNotifications')`, `it('ranks an overdue task above an urgent one')`, `it('renders the empty row when there is nothing')`.

- [ ] **Step 2: Run to verify they fail** — FAIL.

- [ ] **Step 3: Implement** the component; add en/ar keys `dashboard.widgets.review` ("Review"/"مراجعة"), `open` ("Open"/"فتح"), `view` ("View"/"عرض"), `unread_rows_one`/`_other` ("{{count}} unread notification(s)" / "{{count}} إشعار غير مقروء" / "{{count}} إشعارات غير مقروءة").

- [ ] **Step 4: Run tests, suite** — green.

- [ ] **Step 5: Commit** — `git commit -m "feat(web): Needs your attention list"`.

### Task 10: Library breakdown

**Files:**
- Create: `web/src/components/dashboard/LibraryBreakdown.tsx`
- Modify: `web/src/components/dashboard/LifecycleDonut.tsx` (export a presentational `LifecycleRing({ slices, total }: { slices: Slice[]; total: number })` next to the existing widget; ring 88 px, colours `var(--chart-n)`)
- Modify: `web/src/components/dashboard/BreakdownBars.tsx` (new prop `variant?: 'bars' | 'stacked'`; `stacked` renders the 8 px stacked bar + list with count and %; `bars` keeps today's rendering)
- Modify: `web/src/routes/_authenticated/index.tsx` (use it in place of `LifecycleDonut` + `FileTypesAndContributors`)
- Test: `web/src/components/dashboard/__tests__/LibraryBreakdown.test.tsx` (create), `LifecycleDonut.a11y.test.tsx` (point at `LibraryBreakdown`)

**Interfaces:**
- Consumes: `useDashboardMetrics()` (`lifecycle`, `fileTypes`, `contributors`, `isLoading`, `isError`, `isUnavailable`, `refetch`).
- Produces: `export function LibraryBreakdown()` → one `WidgetCard` titled `t('dashboard.widgets.library_title')` ("Library breakdown"/"تفصيل المكتبة") whose body is `grid grid-cols-1 shell:grid-cols-3 divide-y shell:divide-y-0 shell:divide-x divide-border` with three columns: Lifecycle (`LifecycleRing` + legend with counts + `lifecycle_caption`), File types (`BreakdownBars variant="stacked"`), Top contributors (avatar initial 24 px `rounded-full bg-muted`, name, count, %, 6 px `Progress`); the `ChartDataTable` for lifecycle stays.

- [ ] **Step 1: Write the failing tests** — `it('renders the three columns from one metrics call')` (mock `useDashboardMetrics` once, assert the three headings), `it('shows the failure state once with Retry')`, `it('shows contributors with share percentages')`; keep the axe case from `LifecycleDonut.a11y.test.tsx` running against `LibraryBreakdown`.

- [ ] **Step 2: Run to verify they fail** — FAIL.

- [ ] **Step 3: Implement** `LifecycleRing`, the `stacked` variant, `LibraryBreakdown`; add the `library_title` key (en + ar).

- [ ] **Step 4: Run tests, suite** — green.

- [ ] **Step 5: Commit** — `git commit -m "feat(web): Library breakdown card"`.

### Task 11: Ask card and `/ask?q=`

**Files:**
- Create: `web/src/components/dashboard/AskCard.tsx`, `web/src/lib/askLimits.ts`
- Modify: `web/src/routes/_authenticated/ask.tsx:424` (`validateSearch`), `AskPage` (submit `q` on mount once)
- Modify: `web/src/routes/_authenticated/index.tsx` (use it)
- Test: `web/src/components/dashboard/__tests__/AskCard.test.tsx` (create), `web/src/routes/__tests__/ask-query-param.test.tsx` (create)

**Interfaces:**
- Produces: `export function AskCard()` → `WidgetCard` titled `t('dashboard.ask.title')` ("Ask a question"/"اطرح سؤالاً"), subtitle `dashboard.ask.subtitle` ("Get answers from the documents you can see"/"احصل على إجابات من المستندات التي يمكنك رؤيتها"), `Label` + `Textarea rows={3}` placeholder `dashboard.ask.placeholder` ("e.g. Which documents are still in draft?"/"مثال: ما المستندات التي لا تزال مسودة؟"), footer `dashboard.ask.hint` ("Press Enter to ask"/"اضغط Enter للسؤال") + `Button` "Ask" (`dashboard.ask.submit`/"اسأل"); Enter (without Shift) or the button calls `navigate({ to: '/ask', search: { q: text.trim() } })` only when `text.trim().length > 0`; max length = `MAX_QUESTION_LEN` from a new `web/src/lib/askLimits.ts` (`export const MAX_QUESTION_LEN = <the value currently defined in ask.tsx>`), which `ask.tsx` imports instead of its local constant so the route never becomes a component dependency.
- Produces: `Route = createFileRoute('/_authenticated/ask')({ validateSearch: (s) => ({ q: typeof s.q === 'string' ? s.q.slice(0, MAX_QUESTION_LEN) : undefined }), component: AskPage })`; `AskPage` reads `Route.useSearch().q`, and in a `useEffect` with a `useRef` guard calls `send(q)` once when `q?.trim()` is non-empty, then `navigate({ search: {} , replace: true })` to clear it.

- [ ] **Step 1: Write the failing tests** — `AskCard.test.tsx`: typing "draft?" + Enter calls `navigate` with `{ to: '/ask', search: { q: 'draft?' } }`; whitespace-only + Enter does not navigate; Shift+Enter inserts a newline (no navigate). `ask-query-param.test.tsx`: rendering the route at `/ask?q=hello` calls the mocked `askQuestion` API once with `hello`; `/ask?q=` (empty) calls nothing.

- [ ] **Step 2: Run to verify they fail** — FAIL.

- [ ] **Step 3: Implement** the card, the route search validation and the mount submit; add the five `dashboard.ask.*` keys (en + ar).

- [ ] **Step 4: Run tests, suite, tsc** — green.

- [ ] **Step 5: Commit** — `git commit -m "feat(web): Ask card on the dashboard, /ask reads ?q="`.

### Task 12: Dashboard page composition

**Files:**
- Modify: `web/src/routes/_authenticated/index.tsx` (final layout; remove `QuickActions`, `WarmCard`, `PendingSuggestionsCard`, `PageHeader` imports)
- Test: `web/src/routes/__tests__/dashboard-page.test.tsx` (create)

**Interfaces:**
- Consumes: `DashboardHead`, `KpiStrip`, `DocumentsAddedChart`, `AttentionList`, `LibraryBreakdown`, `AskCard`, `DashboardUploadDialog`, `greet`.
- Produces: page = `<div class="grid grid-cols-12 gap-4">` with `DashboardHead` (`col-span-12`), `KpiStrip` (`col-span-12`), `DocumentsAddedChart` (`col-span-12 wide:col-span-8`), `AttentionList` (`col-span-12 shell:col-span-6 wide:col-span-4`), `LibraryBreakdown` (`col-span-12 wide:col-span-8`), `AskCard` (`col-span-12 shell:col-span-6 wide:col-span-4`); the drop-to-upload behaviour (`droppedFiles`) and `DashboardUploadDialog` wiring unchanged.

- [ ] **Step 1: Write the failing test** — render the page with the `widgets.test.tsx` mocks and assert the six regions by accessible name (`Documents added`, `Needs your attention`, `Library breakdown`, `Ask a question`) plus the h1 greeting and the Upload button `header-upload-button`; assert `queryByTestId('quick-action-upload')` is null.

- [ ] **Step 2: Run to verify it fails** — FAIL (quick actions still present).

- [ ] **Step 3: Compose the page** per the Interfaces block.

- [ ] **Step 4: Run tests, suite, lint, tsc, build** — green.

- [ ] **Step 5: Commit** — `git commit -m "feat(web): dashboard composed to the approved mock"`.

### Task 13: E2E and a11y re-targeting

**Files:**
- Modify: `web/e2e/01-login.spec.ts`, `70-a11y.spec.ts` (add `dashboard` light + dark + RTL cases next to the existing four pages, lines 232–280), `72-rtl-geometry.spec.ts`, `i18n-rtl-shell.spec.ts` (+ its `-snapshots`), `i18n-rtl-interactions.spec.ts`, and any of `08`, `36`–`51` whose shell steps fail
- Modify: `web/src/components/layout/__tests__/ux-breadcrumbs.test.tsx` if not done in Task 5

**Interfaces:**
- Consumes: the accessible names fixed in Tasks 4, 5, 7 (`Search documents…`, `Toggle theme`, `Home`, KPI link names).

- [ ] **Step 1: Run the suite to see what fails** — `VITE_GATEWAY_URL=http://127.0.0.1:1 npx playwright test 01 70 72 i18n-rtl 08 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51` (with the `LD_LIBRARY_PATH` from `docs/` / the web a11y gate notes) → record the failing specs.

- [ ] **Step 2: Re-target each failing selector** to the new DOM (no journey removed); add the three dashboard axe cases: `test('dashboard', …)` goto `/`, light; the `.dark` variant via the existing `forceDark` helper at lines 56–80; RTL via `localStorage.setItem('i18nextLng','ar')` as `i18n-rtl-shell.spec.ts` does. Regenerate the `i18n-rtl-shell` snapshots with `--update-snapshots` and eyeball the diff.

- [ ] **Step 3: Run the full Playwright suite** — `npx playwright test` → all green.

- [ ] **Step 4: Screenshots** — `npx playwright screenshot` (or a one-off spec) at 1440×900, 1180×900 and 390×844 in light, dark and RTL → save under `docs/superpowers/plans/assets/2026-10-26-dashboard/` (9 PNGs) for the PR.

- [ ] **Step 5: Commit** — `git commit -m "test(web): E2E and axe re-targeted to the new shell and dashboard"`.

### Task 14: Remove dead code and hand back

**Files:**
- Delete: `web/src/components/dashboard/{ActivityChart,KpiTile,NeedsAttention,FileTypesAndContributors}.tsx` (`KpiStrip.tsx` stays; `LifecycleDonut.tsx` keeps only `LifecycleRing`), `web/src/components/intelligence/PendingSuggestionsCard.tsx` if no other importer (`grep -rn PendingSuggestionsCard src`), the `crextio` directory if no other importer, `web/src/components/layout/{CursorGrid,PointerFx}.tsx` only if unused
- Modify: `web/tailwind.config.js` (drop the `neu-*`/`float*` shadow aliases once `grep -rn 'shadow-neu\|shadow-float' src` is empty — otherwise leave them and list the files in the hand-back), `globals.css` (drop `.dash-rise` and the ripple rules if unused)
- Modify: `web/src/components/dashboard/__tests__/widgets.test.tsx` (remove describes of deleted components)
- Create: `docs/superpowers/plans/2026-10-26-design-system-shell-dashboard-handback.md`

- [ ] **Step 1: Find remaining importers** — `grep -rn "crextio\|ActivityChart\|KpiTile\|NeedsAttention\|FileTypesAndContributors\|PendingSuggestionsCard" src --include=*.tsx --include=*.ts` → delete only what has no importer outside the deleted set.
- [ ] **Step 2: Delete, then** `npx tsc --noEmit && npm run lint && npm test -- --run && npm run build` → green; `grep -rn "shadow-neu\|dash-rise\|nm-" src | wc -l` → recorded in the hand-back.
- [ ] **Step 3: Write the hand-back**: files on other routes that still use hard-coded colours or the alias shadows (the `grep` output), the screenshots, the SLO/axe results, what step 2 of the redesign (next screens) should pick up.
- [ ] **Step 4: Commit** — `git commit -m "chore(web): remove neumorphic and Crextio leftovers; redesign step-1 hand-back"`.
