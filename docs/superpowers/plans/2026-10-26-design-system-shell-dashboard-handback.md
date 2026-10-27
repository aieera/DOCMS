# Redesign step 1 hand-back: design system, shell, dashboard

For the engineer doing redesign step 2 (the next screens). Spec: `docs/superpowers/specs/2026-10-25-design-system-shell-dashboard-design.md`. Plan: `docs/superpowers/plans/2026-10-26-design-system-shell-dashboard.md`. Branch `feat/ui-redesign-dashboard`, worktree `.worktrees/ui-redesign`. All paths below are under `web/` unless they start with `docs/`.

**State:** the final whole-branch review (`.superpowers/sdd/2026-10-26-design-system-shell-dashboard/final-review.md`) found 2 Critical and 11 Important issues. The final fix wave implemented every ruling; section 4 lists each item with its commit. Sections 1-3 describe what is in the tree.

## 1. What step 1 shipped, and where

**Tokens and theme** (light and dark; flat surfaces, 1px borders, a four-step shadow scale):
- `src/styles/globals.css`: CSS variables for both themes (hex values, not HSL triplets, so `hsl(var(--x))` must not be written anywhere), the `@fontsource` imports, `.page-title`, `.text-metric`, `.text-caption`, the `.dash-grow` bar animation.
- `tailwind.config.js`: colour utilities built by `token()` as `color-mix(in srgb, var(--x) calc(<alpha-value> * 100%), transparent)`; `shadow-xs/sm/md/lg`; `shadow-focus-halo` and `shadow-focus-halo-destructive`; radius scale; screens `shell` (801px) and `wide` (1181px); the legacy `shadow-neu*` / `shadow-float*` aliases (section 5.1).
- Tests: `src/styles/__tests__/{tokens,fonts,theme-contrast}.test.ts` (token set, font loading, AA contrast pairs in both themes).

**Fonts**: Geist Sans and Geist Mono (`@fontsource/geist`, `@fontsource/geist-mono`) and IBM Plex Sans Arabic (`@fontsource/ibm-plex-sans-arabic`), self-hosted and bundled by Vite. The Google Fonts links are gone from `index.html`. Files: `package.json`, `src/styles/globals.css`, `index.html`.

**Primitives**: `src/components/ui/shadcn/{button,badge,input,textarea,progress}.tsx`, new `src/components/ui/shadcn/kbd.tsx`, `src/components/ui/card.tsx`. Tests in `src/components/ui/__tests__/` (`button-surfaces`, `input-surfaces`, `Button`, `Kbd`, `Progress`).

**Sidebar and layout**: `src/components/layout/{app-layout,app-sidebar}.tsx` (rail at 801px and up, collapsible, Settings pinned at the bottom, smart folders inside the Workspace group), `src/store/uiStore.ts` (`notificationsOpen` / `openNotifications`, excluded from persistence).

**Header**: `src/components/layout/{app-topbar,breadcrumbs,theme-toggle}.tsx`, `src/components/shared/LanguageSelector.tsx` (sidebar toggle button, 280x36 search trigger with `Kbd` from 1181px and a search icon button below that, notifications bell, theme toggle showing the current mode, language control with its label from 1181px). The mobile navigation sheet carries the language control in a footer row.

**Dashboard** (`src/routes/_authenticated/index.tsx` composes it): `src/components/dashboard/`
- `DashboardHead.tsx`, `greet.ts`: greeting, workspace count, Upload button (also the drop target for files, opening `DashboardUploadDialog`).
- `KpiStrip.tsx`, `KpiCard.tsx`: four KPI cards (documents, open tasks, awaiting approval, unread).
- `DocumentsAddedChart.tsx`: 12-month bar chart.
- `AttentionList.tsx`: "Needs your attention" (pending AI tag suggestions, unread notifications, top 3 open tasks; owns `taskRank`, `taskDetail`, `RANK_ICON`, `OPEN_STATUSES`).
- `LibraryBreakdown.tsx`, `LifecycleDonut.tsx` (now only `LifecycleRing`), `BreakdownBars.tsx`, `chartTheme.ts`, `ChartDataTable.tsx`: lifecycle ring, file-type stacked bar, top contributors.
- `AskCard.tsx`, `src/lib/askLimits.ts`: ask box that hands the question to `/ask` in router history state (`askQuestion`), never in the URL.
- `WidgetCard.tsx` (the card frame all widgets share), `metrics.ts`, `useDashboardMetrics.ts`.
- Related routes: `src/routes/_authenticated/ask.tsx` (asks a history-state handoff once, then clears it; `?q=` only prefills the composer), `src/routes/_authenticated/tasks.tsx` (`validateSearch` for `?tab=my|created|approvals`).
- `src/components/ai/AnswerMarkdown.tsx` (shared by Ask, doc Q&A and the chat panel) renders Markdown images as alt text plus a plain link, never an `<img>`.

**Touched for the new tokens only**: `src/components/layout/{auth-shell,AuthHero,CursorGrid}.tsx`, `src/components/folders/BrowserTiles.tsx`, `src/routes/_authenticated/reports.tsx` (`hsl(var(--card))` became `var(--card)`).

**E2E**: `e2e/01-login.spec.ts`, `e2e/70-a11y.spec.ts` re-targeted to the new shell and dashboard.

**Removed at the end of step 1 (Task 14, commit `chore(web): remove neumorphic and Crextio leftovers`)**:
- `src/components/ui/crextio/` (whole kit), `src/components/intelligence/PendingSuggestionsCard.tsx`
- `src/components/dashboard/{ActivityChart,KpiTile,NeedsAttention,FileTypesAndContributors,Sparkline}.tsx` and the `LifecycleDonut` widget export
- `.dash-rise` / `.dash-draw` and their keyframes in `globals.css`
- i18n keys (en and ar): `dashboard.overview`, `dashboard.quick_actions`, `dashboard.qa.*`, `dashboard.widgets.{activity_subtitle,lifecycle_subtitle_*,attention_subtitle,attention_mentions_error,suggestions_review}`, top-level `search_placeholder`
- unused `delayIndex` prop on `KpiCard`; `shadow-neu-inset` on the `BreakdownBars` track
- test files renamed: `neu-button.test.tsx` to `button-surfaces.test.tsx`, `neu-input.test.tsx` to `input-surfaces.test.tsx`, `neu-tokens.test.ts` to `theme-contrast.test.ts` (content unchanged)

## 2. Verification

| Check | Result |
|---|---|
| `npx tsc --noEmit` | clean |
| `npm run lint` (eslint plus the rtl, utf8, tenant scripts) | exit 0; the warnings that remain are on files this step did not touch |
| `npx vitest run` | After the final fix wave: 134 files, 1209 tests, all pass. At Task 14: 133 files, 1146 tests. Before Task 14: 135 files, 1177 tests. The 31 tests removed in Task 14 belong to deleted components: PendingSuggestionsCard 4, `ui/crextio` 4, `widgets.test.tsx` 23. Coverage of surviving code in `widgets.test.tsx` (offline first mount, locale grouping, facets unavailable, reserved height) was retargeted to `LibraryBreakdown` and `AttentionList`, not dropped |
| `npm run build` | passes |
| Playwright on the built preview (re-run after the final fix wave) | 116 passed, 14 pre-existing skips, 0 failed |
| Dashboard axe (WCAG 2.1 AA) | 0 violations in light, dark and RTL at 1440, 1180 and 390 px, and light at 820 px (final fix wave) |
| `grep -rn "shadow-neu\|dash-rise\|nm-" src \| wc -l` | 207 lines. 204 are `shadow-neu*` aliases on other routes and components; 3 are negative assertions in tests (`not.toHaveClass('dash-rise')`, `not.toContain('--nm-dist')`, `not.toMatch(/--nm-/)`) |

Screenshots, `docs/superpowers/plans/assets/2026-10-26-dashboard/` (re-captured after the final fix wave):

| | Light | Dark | RTL (Arabic) |
|---|---|---|---|
| 1440 px | [dashboard-1440-light.png](assets/2026-10-26-dashboard/dashboard-1440-light.png) | [dashboard-1440-dark.png](assets/2026-10-26-dashboard/dashboard-1440-dark.png) | [dashboard-1440-rtl.png](assets/2026-10-26-dashboard/dashboard-1440-rtl.png) |
| 1180 px | [dashboard-1180-light.png](assets/2026-10-26-dashboard/dashboard-1180-light.png) | [dashboard-1180-dark.png](assets/2026-10-26-dashboard/dashboard-1180-dark.png) | [dashboard-1180-rtl.png](assets/2026-10-26-dashboard/dashboard-1180-rtl.png) |
| 820 px (rail expanded; the I1 band) | [dashboard-820-light.png](assets/2026-10-26-dashboard/dashboard-820-light.png) | | |
| 390 px | [dashboard-390-light.png](assets/2026-10-26-dashboard/dashboard-390-light.png) | [dashboard-390-dark.png](assets/2026-10-26-dashboard/dashboard-390-dark.png) | [dashboard-390-rtl.png](assets/2026-10-26-dashboard/dashboard-390-rtl.png) |

**Running Playwright locally** (Linux, no root, no passwordless sudo):
1. Chromium is already installed under `~/.cache/ms-playwright`; its system libraries are not. Extract them into a local prefix instead of installing: in an empty directory run `apt-get download libatk1.0-0t64 libatk-bridge2.0-0t64 libatspi2.0-0t64 libcups2t64 libasound2t64 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libxkbcommon0 libpango-1.0-0 libcairo2 libnss3 libnspr4 libxrender1 libx11-6 libx11-xcb1 libxcb1 libxext6 libxi6 libxtst6 libxcursor1 libxss1 libglib2.0-0t64 libpangocairo-1.0-0 libcairo-gobject2 libdrm2 libexpat1 libfontconfig1 libfreetype6` (Ubuntu 24.04 `t64` names), then `for d in *.deb; do dpkg -x "$d" extracted/; done`.
2. `export LD_LIBRARY_PATH=<that directory>/extracted/usr/lib/x86_64-linux-gnu`.
3. Build and serve with the gateway pointed at a dead port, so the preview server's `/api` proxy hits a dead port (the specs mock their API calls with `page.route`): `VITE_GATEWAY_URL=http://127.0.0.1:1 npm run build`, then run the specs with `VITE_GATEWAY_URL=http://127.0.0.1:1 CI= npx playwright test` (the `webServer` in `playwright.config.ts` runs `npm run preview` on 4173 and reuses a server already there). CI also runs the built preview bundle, not the dev server.
4. The app locale under test is the `dms_locale` cookie, not `localStorage`.

## 3. Decisions that deviate from the spec or plan

From the ledger (`.superpowers/sdd/2026-10-26-design-system-shell-dashboard/progress.md`), every `Ruling:` line. The pre-flight scan found no contradictions between tasks (its `Ruling:` line says "none needed"). The ledger has 52 lines containing `Ruling`: that pre-flight line plus 51 decisions. The 51 appear below as 48 bullets; three bullets merge two ledger lines each (Task 1 muted-foreground, Task 3 focus halo, Task 11 Enter rule). The final review added six `Final: Ruling` lines (58 in all), listed at the end of this section. Task 13's two rulings were implemented in the final fix wave (section 4).

**Tokens (Task 1)**
- Colour utilities use the `color-mix(... <alpha-value> ...)` wrapper instead of a bare `var(--x)`. A bare `var()` drops every `/NN` opacity modifier (about 600 uses in 159 files). Same colour at full opacity; one line in `token()` to change.
- `--muted-foreground` was first kept at the mock's #737373 (4.35:1 on `--muted` #f5f5f5, under AA) with an `it.fails` test, then changed to #6f6f6f in light (at least 4.5:1 on #f5f5f5, about 5.0:1 on white). Spec section 3 amended. The axe gate outranks one hex of the mock.
- `shadow-md`/`shadow-lg`, the dark shadow values and the dark success/warning/info hex are implementer choices: the mock is not in the repo, and the light hues fail AA on dark.

**Fonts (Task 2)**
- Geist comes from `@fontsource/geist` and `@fontsource/geist-mono` instead of the `geist` package, which ships only `next/font` modules with a hard peer dependency on `next` (about 330 MB). Spec section 3 amended.
- Bare `code`, `kbd`, `pre`, `samp` stay monospace under `:lang(ar)`; the brief's rule would have removed monospace from about 49 unclassed uses.
- `.page-title` gets `letter-spacing: 0` under RTL and Arabic: the -0.025em is a Latin choice and would compress joined Arabic letters.

**Primitives (Task 3)**
- Kbd radius is 4px (spec section 3) over the plan's `rounded-sm` (6px), a plan transcription error.
- Focus is a 1px ring plus a 4px-spread `ring/50` halo composed in `box-shadow` (about 3px visible band), matching the mock's outer extent. The halo is extracted to the `focus-halo` and `focus-halo-destructive` shadow keys so later controls do not copy the literal.
- Kbd forwards `...props` (the header uses it inside a button and may need `aria-hidden`).
- Card padding is 20px (`p-5`); Card has no callers outside tests, and dashboard cards take their padding from `WidgetCard`.

**Sidebar (Task 4)**
- Task 4 owns the `lg` to `shell` breakpoint switch for sidebar visibility, the layout offset and the topbar's open-navigation button, so the three never disagree.
- `SmartFoldersBlock` moves inside the Workspace group (spec section 4); pinning Settings with `mt-auto` would otherwise put the smart folders below it.
- The spec's sidebar "workspace selector" does not exist today, so there was nothing to keep.

**Header (Task 5)**
- The collapse control lives in the header (spec section 4), not the brand row: a desktop `PanelLeft` button (`hidden shell:inline-flex`) toggles collapse and the existing mobile button (`shell:hidden`) opens navigation. The brand-row toggle is removed and its tests were rewritten against the header.
- The pre-existing `shadow-neu` classes in `app-topbar.tsx` were removed in this task.
- The header search stays an `<input>` styled as the 280x36 outline trigger, because a `<button>` would break type-to-search. The test asserts the input by accessible name, not role `button`.
- The language-control restyle was folded into this task (plan omission; the spec binds).
- The Ctrl+K and `/` focus-a-hidden-input bug at 641-800px was promoted to the fix round: this task had widened the dead band from under 640 to under 801.
- `Badge` renders a `<span>` root so count badges inside buttons are valid phrasing content.

**Dashboard frame (Tasks 6-7)**
- `WidgetCard` body carries `px-5 pb-5` on the same element as each widget's `bodyClassName` min-height (border-box), so widgets set their min-heights to include the 20px bottom padding and do not grow when data arrives.
- `notificationsOpen` is excluded from the persisted UI store (`partialize`), so the panel never reopens on reload.
- The Awaiting approval card links to `/tasks?tab=approvals` and the tasks route gained `validateSearch` for `tab`; the spec said `?filter=approvals`, but the page's concept is a tab and no `filter` param exists.
- Arabic plural keys ship all six CLDR forms wherever a count is pluralised. With only `_one`/`_other`, i18next falls back to English for 0, 2 and 3-99 in `ar`. This applies to every later plural key.
- Closing the notifications menu opened from the Unread card returns focus to the header bell (Radix trigger); the menu belongs to the bell.
- The Awaiting approval hint keeps the existing copy ("Nothing pending", "waiting since today", "oldest waiting {{count}}d") over the mock's "{n} waiting", which would repeat the numeral.

**Documents-added chart (Task 8)**
- The chart `aria-label` is built in the component from `dashboard.widgets.activity_aria`, so `metrics.ts` stays presentation-free and `trendSummary` is untouched. Spec section 7 outranks the brief's "use `trendSummary`".
- Arabic copy for the three new keys uses the block's existing tanween form.

**Attention list (Task 9)**
- `taskRank`, `taskDetail` and `RANK_ICON` moved into `AttentionList.tsx` and are exported (no duplication while `NeedsAttention` existed).
- `NotificationsDropdown` resets `notificationsOpen` on unmount (an effect cleanup calling `closeNotifications`), so a logout while the panel is open never reopens it on the next mount (carried over from a Task 7 minor).
- `taskDetail`'s English literals ("Overdue ...", "Due ...", "Approval step", "No due date", "Urgent", "High") go through `t()` in en and ar.
- No subtitle on the card (spec section 5 names none; the old "unread mentions" copy is no longer true). The overdue tint is dropped: overdue is signalled by the Clock icon and the "Overdue" detail.
- The empty row keeps "Nothing needs you right now" over the spec's narrower copy, because the list shows any open task.

**Library breakdown (Task 10)**
- Lifecycle colours are mapped by state key, not slice index: five chart tokens for the five common states and neutrals for the two rarest. File-type segments past five share one neutral "Other". A state keeps its colour across tenants.
- Column dividers use logical `shell:border-s` instead of `shell:divide-x`, which is a left border in Tailwind 3 and the wrong edge under RTL.
- The shared `Progress` primitive was fixed (fills from the inline start under RTL, forwards `value` to Radix for `aria-valuenow`, clamps 0-100, drops `shadow-neu-inset`); it had no other consumers.
- The "Other" segment is derived from the facet total (remainder = total minus the five shown), so it appears with real data.
- Arabic count nouns use pluralised keys, and the Arabic comma comes from the catalogue, in both the stacked list and the contributors rows.

**Ask card (Task 11)**
- `/ask` auto-submits once per mount (a `useRef` guard) and then clears `?q=` with a replace navigation, so reload and Back never re-ask. **Superseded** by the final review's C2 ruling (below): a URL never asks anything.
- Enter submits only when `!shiftKey && !isComposing && keyCode !== 229` (Arabic and CJK IMEs commit with Enter; the `keyCode` clause amends the first version of the rule).
- `WidgetCard` gains an optional `icon` start slot in the header, rendered only when supplied.
- A visible label `dashboard.ask.label` ("Your question") sits above the textarea; spec section 5 lists a label and the plan supplied no copy.
- The footer row is `mt-auto` so it pins when the card stretches beside the Library card; the textarea has `dir="auto"`.

**Dashboard composition (Task 12)**
- Cards in a grid row stretch to equal height (`h-full`) so chart/attention and library/ask align at 1181px and up.
- `grid-flow-dense` is kept: at 801-1180px the spec pairs attention and ask in one row with the library full width. Cost: at mid widths the Ask card is visually before the Library card but after it in tab order.
- Drop-to-upload regressed when the QuickActions Upload card was removed (it was the only `onDrop` target). The head's Upload button is now the drop target (dragover highlight; drop opens `DashboardUploadDialog` with `initialFiles`).

**Verification task (Task 13): implemented in the final fix wave (section 4)**
- `LanguageSelector` appears inside the mobile sidebar sheet (800px and below) because the spec hides it in the header at that width and the sheet would otherwise leave phones with no language switch.
- Attention row titles wrap (`line-clamp-2`) instead of truncating on one line.
- The same ruling said "Search on phones is not a regression (header search was already hidden under 640px on main)". That premise was wrong: below 640px the dashboard's QuickActions had a **Search** card (`dashboard.qa.search_label`), and 641-800px had the header search. The final review's C1 ruling corrects it.

**Task 14**
- The commit trailer stays "Claude Opus 5.5" on all subagent commits (the session's standing instruction); earlier commits are not amended.

**Final review (fix wave)**
- C1: a header search icon button opens `/search` wherever the search input is hidden (QuickActions had the only phone Search entry).
- C2: questions auto-submit only from in-app navigation state, never from the URL; `?q=` only prefills; `AnswerMarkdown` renders model-output images as non-fetching links. This deviates from spec section 5 item 6 ("submits it on mount" from `?q=`): security outranks it.
- I1: the search input and the language label show only from 1181px (an icon button below). Cost: a smaller search affordance at 801-1180px.
- I3: light `--ring` #737373 and `--destructive` adjusted for AA; spec section 3 amended. Dark (a follow-up ruling): `--destructive` #ff6467 with `--destructive-foreground` #0a0a0a, i.e. dark text on a light red.
- I4: the theme toggle shows Monitor in system mode and states the mode in its name.
- I2, I5-I11 and the minors M2, M7, M8, M9 as listed in `final-fix-brief.md`; M1, M3, M4, M6 stay deferred to step 2 (section 5.8); the review's declined-to-judge items stay out of scope (section 5.9).

Choices made by the Task 14 implementer (not ledger rulings):
- The `shadow-neu*` / `shadow-float*` aliases stayed in `tailwind.config.js` at merge time; they were removed afterwards (section 5.1).
- `Sparkline.tsx` was deleted with `KpiTile` (its only importer), and `.dash-draw` with it. `.dash-grow` stays: `BreakdownBars` still uses it.
- `segment.home` stays in both catalogues: `humanizeSegment('')` in `src/lib/segmentLabels.ts` builds the key as `segment.${key}` with `key = 'home'` for the root segment, so a static grep cannot prove it unused.
- `LifecycleDonut.tsx` keeps its file name though it now exports only `LifecycleRing`, to avoid churn in `LibraryBreakdown` and its tests.
- `trendSummary` in `metrics.ts` has no production caller any more; it is left in place with its tests.

## 4. Done in the final fix wave

Every item the final review raised, with the commit that closed it. Base `bd19f2b4`.

**Critical**
- C1, phones had no route to document search: a 36px ghost search icon button (`wide:hidden`, named "Search documents…") opens `/search`. `fb7627e0`.
- C2, `/ask?q=` asked a crafted link's question with one click, and an answer could leak through a Markdown image: the Ask card hands over in router history state (`askQuestion`, typed by augmenting `@tanstack/history`'s `HistoryState`); `/ask` asks it once per handoff (keyed on the history entry, so a second in-app ask while mounted is honoured, and one arriving mid-stream waits) and replaces the entry with the state cleared; `?q=` only prefills the composer and is dropped from the URL; `AnswerMarkdown` renders images as alt text plus a nofollow link, never an `<img>`. `fbdb3e16`.

**Important**
- I1, the header overflowed at 801-930px: the 280px search box is `hidden wide:flex` and the language label `hidden wide:inline`. At 820px the right cluster is 285px wide and the header no longer overflows. `fb7627e0`.
- I2, `cn()` dropped `text-caption`: `lib/cn.ts` registers `text-caption`/`text-metric` (font size) and `shadow-focus-halo*` (shadow) with `extendTailwindMerge`. KPI hints render at 12/16. `8d8803a0`.
- I3, contrast: light `--ring` #737373 (4.7:1 on white); light `--destructive` #d6000a (4.5:1 on its own 10% tint, 5.4:1 on white). New pairs in `theme-contrast.test.ts`; spec section 3 amended. `e4db35d0`. Dark: `--destructive` #ff6467 (error text 6.2:1 on the card, was 3.8:1) with `--destructive-foreground` #0a0a0a (6.9:1 on red), because no dark red serves both as error text and under white text; `KpiCard`'s `dark:text-foreground` workaround is gone, so the overdue hint is red in dark too. Commit `fix(web): dark error red meets AA with dark text on red`.
- I4, the theme toggle: Sun, Moon, and Monitor for system; name and tooltip "Theme: {mode}" from one key (`theme.toggle_with_mode`). `cf7d9849`.
- I5, phones had no language switch: `LanguageSelector` in a footer row of the mobile sheet. `fb7627e0`.
- I6, attention titles truncated: `line-clamp-2`. `155c8451`.
- I7, the Review link landed on the Catalog tab: `search={{ tab: 'review' }}`. `155c8451`.
- I8, the unread row counted the first inbox page: the number comes from `['notifications','unread-count']`. `155c8451`.
- I9, "Other" took a palette colour: it is coloured by its key (`OTHER_KEY`). `155c8451`.
- I10, `activity_aria` had one form: pluralised on `count`, with the figure in `total` (en two forms, ar six). `82b1f2e1`.
- I11, English literals in failure and loading states: `FACETS_UNAVAILABLE_KEY`, WidgetCard's error, loading and empty copy, KpiCard's "unavailable" name, all in en and ar. `82b1f2e1`.

**Minor**
- M2, stale `theme-color` meta: #ffffff for light and #0a0a0a for dark, per scheme. `e4db35d0`.
- M7, this hand-back corrected (this commit).
- M8, catalogue parity: `src/i18n/__tests__/catalogue-parity.test.ts`. `82b1f2e1`.
- M9, dropped `AttentionList` assertions restored (no raw type code, no `markAsRead`, the notifications-half Retry). `155c8451`.

Checked and not pending: the Task 12 `act()` wrap around the header Upload clicks is already in `src/routes/__tests__/-dashboard-page.test.tsx`.

## 5. Leftovers for step 2

### 5.1 Legacy shadow aliases (removed 2026-10-27)

Done after the merge, on `chore/remove-old-ui`: every `shadow-neu*` / `shadow-float*` use outside tests was renamed to the flat scale with the same rendering (`neu` to `shadow-sm`, `neu-sm` to `shadow-xs`, `neu-lg` and `float-sm` to `shadow-md`, `float` to `shadow-lg`; `neu-inset` and `neu-pressed` were `none` and were dropped), and the aliases were deleted from `tailwind.config.js`. The tests that assert the old names never return were kept. The old-UI design documents (neumorphic UI, the UX phase built on it, the previous dashboard redesign, and the old-design admin frame) were removed from main; the `neu-ui` and `admin-frame` branches were kept unchanged at the user's request, and the new UI also lives on the `new-ui` branch. Restyling the other routes is still step 2; it now starts from the flat scale only.

### 5.2 Hard-coded colours outside tests

`grep -rnE "#[0-9a-fA-F]{3,8}\b|rgb\(|hsl\(" src --include=*.tsx`, tests excluded: 64 lines in 13 files. Most are data colours (graph, chart, annotation and user palettes) rather than theme colours, but none of them follows dark mode.

| File | Lines | What |
|---|---|---|
| `src/components/folders/BrowserTiles.tsx` | 19 | inline SVG file-type tiles (folder gradient and fills, white glyphs, per-type label colours such as `#EB5757`) |
| `src/components/documents/RelationshipsGraph.tsx` | 10 | Cytoscape.js node and edge style object |
| `src/components/shared/WorkflowGraph.tsx` | 8 | workflow node state fills and borders (amber, blue, violet, green `#10b981`, red `#ef4444`), plus `var(--color-border, #e2e8f0)`-style fallbacks |
| `src/routes/_authenticated/reports.tsx` | 7 | chart series palette (`CHART_COLORS`, `SERIES`), chosen for colour-vision-deficiency separation |
| `src/components/layout/CursorGrid.tsx` | 4 | canvas fallback colours for the auth background (auth shell, kept) |
| `src/components/viewer/ImageAnnotationLayer.tsx` | 4 | annotation stroke/fill colours (`#2563eb`, `#f59e0b`) |
| `src/routes/_authenticated/admin/tags.tsx` | 3 | tag colour picker swatches (user data, not theme) |
| `src/components/admin/ComplianceDashboard.tsx` | 2 | Recharts bar/pie fills (`COLORS`) |
| `src/components/documents/CollaborativeEditor.tsx` | 2 | per-user cursor palette (user data) |
| `src/components/documents/RealtimePresence.tsx` | 2 | per-user presence colours (user data) |
| `src/components/layout/AuthHero.tsx` | 1 | hero illustration shadow ellipse `rgb(0 0 0 / .15)` (auth shell, kept) |
| `src/components/viewer/PDFViewer.tsx` | 1 | annotation marker line `borderTop: '2px solid #dc2626'` |
| `src/routes/_authenticated/admin/tenant/watermark.tsx` | 1 | watermark colour default (`#888888`; user data) |

Also: `BrowserTiles.tsx` sets `fontFamily="'Plus Jakarta Sans', system-ui, sans-serif"` on SVG text; that face is not shipped.

### 5.3 Typography

- `font-serif` has no users left: the Crextio kit was the only one, and `tailwind.config.js` defines no `serif` family (`src/styles/__tests__/fonts.test.ts` asserts it).

### 5.4 Browser floor and CSP

- The `color-mix()` colour wrapper in `tailwind.config.js` (and the `focus-halo` shadows) require Chrome and Edge 111, Safari 16.2, Firefox 113. Older browsers cannot parse the declaration, so every colour utility is lost there. No ADR in `docs/adr` records this floor; add one before release, or revisit the wrapper, if it is not acceptable.
- The CSP still ships report-only (`pkg/middleware/secheaders.go`, `SEDOC_CSP_ENFORCE=false` by default), so its `img-src 'self'` is not enforced. The C2 fix does not depend on it; enforcing it is a deployment decision.

### 5.5 Known gaps and pre-existing issues

- **RTL screenshot baseline.** The stale pre-redesign `rtl-dashboard-chromium-win32.png` was removed on 2026-10-27; no baseline is committed, so `e2e/i18n-rtl-shell.spec.ts` records a `skipped-assertion` annotation on every platform. Add a baseline of the new dashboard per platform to enable the diff.
- **`npm run i18n:check` does not work.** `package.json` points at `scripts/check-i18n-keys.mjs`, which does not exist. Add the script or remove the entry. Key parity between en and ar is now covered by `src/i18n/__tests__/catalogue-parity.test.ts` (M8).
- **Month names follow the browser locale, not the app language.** `MONTH_LABEL` in `src/components/dashboard/metrics.ts` is `new Intl.DateTimeFormat(undefined, ...)`, so the documents-added chart shows English month names under Arabic when the browser is English (pre-existing).
- **Arabic "5 يوم".** `dashboard.kpi.oldest_waiting` in `public/locales/ar/common.json` is a single string ("أقدم انتظار {{count}} يوم") with no plural forms, so 5 reads "5 يوم" where Arabic needs "5 أيام". Give it the six CLDR forms like the other counts (pre-existing on main).
- **Empty band** at the bottom of the Library and attention cards at some widths.

### 5.6 Dead code left in place

- `trendSummary` in `src/components/dashboard/metrics.ts` (tests only).
- `BreakdownBars` default (non-stacked) variant: the only production caller passes `variant="stacked"`. `.dash-grow` in `globals.css` is used only by that default variant.
- `delayIndex` on `WidgetCard` and `BreakdownBars`: accepted and ignored (a `WidgetCard` test pins that).
- `RANK_ICON[1]` in `AttentionList.tsx`: rank 1 is never produced.
- File and test names that now describe less than they hold: `LifecycleDonut.tsx` (ring only), `LifecycleDonut.a11y.test.tsx` (tests `LibraryBreakdown`), `src/components/ui/__tests__/neu-combobox.test.tsx`.

### 5.7 Deferred minor items from the ledger, by area

Items the ledger tagged for Task 14 are done (orphaned keys, `ActivityChart`, test renames, `KpiCard` `delayIndex`, stale `NeedsAttention` comment, `search_placeholder`) and are not repeated; `segment.home` was kept (section 3).

**Tokens, CSS, fonts (Tasks 1-2)**
- The `hsl(var(` guard test covers `globals.css` only (a grep of `src` finds none today).
- Redundant `.dark` alias redefinitions in `globals.css` (lines 164-165 after the final fix wave; 160-161 at `bd19f2b4`); a `globals.css` parser duplicated in two test files.
- `shadow-neu-inset` / `shadow-neu-pressed` to none changed about 96 call sites at the time (plan-mandated); check them on screenshots when restyling each route.
- The auth shell (`AuthHero`, `auth-shell`) gradients are now flat or inverted; the auth shell was out of scope for step 1.
- 12 pre-existing jsdom canvas warnings in the vitest output.
- Tests do not pin `@layer` placement of `.text-metric` / `.text-caption`; literal weight counts (`toBe(9)`) are brittle; the order of the Arabic guard rules is unasserted.
- `.font-sans` defeats the Arabic font rule (1 use, `clauses/index.tsx` around line 392); redefining `--font-sans` under `:lang(ar)` would be more robust.
- `font-light` (300) and Arabic bold (700) resolve to the nearest shipped weights.
- The RTL `.page-title` rule needs an ancestor with `dir` or `lang` (`html` has both today); the fonts scan covers `src/**` only.

**Primitives (Task 3)**
- Badge halo is dead CSS on a non-focusable element; the count-badge test does not pin removal of `px-2`, `py-0.5`, `text-xs`.
- Outline focus drops `shadow-xs` (the halo shares `--tw-shadow`).
- `CardFooter` `p-6` became `p-5` (not in the brief; 0 callers).
- Visible halo band is about 3px (the ring paints over the 4px spread); visual QA may want 5px.
- Select chevron keeps `opacity-50` on a muted colour; the shared `select.tsx` base still has `shadow-neu-inset` and `rounded-lg`.

**Sidebar and header (Tasks 4-5)**
- The "hides smart folders when collapsed" test may pass vacuously (asserts before the query resolves).
- Collapsed groups gained a `border-t` hairline (unrequested); `bg-sidebar` is applied twice (pre-existing); collapsed tooltips are native `title` (not shown on keyboard focus).
- The search form re-spells the outline surface without `hover:bg-accent`; the avatar relies on `--secondary` equalling `--muted`; the search accessible name contains "..." (spec copy); the `getClientRects` test stub is coarse. (The theme tooltip's ": " join and the missing system-icon test were fixed with I4.)
- The header **overflowed** (not "was tight") between 801 and about 930px with the rail expanded; fixed by I1 (section 4).

**Dashboard (Tasks 6-12)**
- Task 6: header spacing `mb-4` to `pb-3` shifts content up about 4px.
- Task 7: `KpiCard` `search` is typed `Record<string,string>` and bypasses route types; the workspaces query options are duplicated in `index.tsx` and `KpiStrip.tsx` (a `useWorkspaces` hook exists); first-name extraction in `index.tsx` is untested; the Arabic plural test only checks key existence; the inline store reset in `widgets.test.tsx` belongs in `afterEach`.
- Task 8: no reduced-motion test (the entrance stagger is gone by design).
- Task 9: the card waits on the optional suggestions query for admin roles; a failed suggestions probe shows the empty row; the min-height test is weak.
- Task 10: light `chart-4` and `chart-5` (draft, in_review) are only about 1.24:1 apart; the unknown-state `var(--border)` colour is nearly invisible (legend and table carry the data); a narrow 3-up with 7 states grows about 12px; the lifecycle caption is announced 3 times; `CountShare`'s `percent` shadows a module helper; the sr-only count is unformatted (1234567); per-row rounding can sum to 99 or 101; the Arabic visible figure keeps a Latin "%".
- Task 11: a numeric `?q=` is dropped by the string-only `validateSearch` (not reachable from the UI); tests must use role queries ("Ask a question" appears 4 times).
- Task 12: the "logical utilities" test name oversells; the chart plot is a fixed 220px inside a stretched card; dropping a file elsewhere on the page still triggers the browser default (unchanged from before); the drop target is one button instead of a card.

**Task 14 (dropped with `NeedsAttention`)**
- The `AttentionList` assertions (no raw type code, no `markAsRead`, notifications-half Retry) were restored in the final fix wave (M9).
- `KpiCard`'s flat-series and zero-series guards are untested.

**E2E and axe (Task 13)**
- The RTL axe case does not wait for KPI cards or file-type rows (a late skeleton could be scanned); the img check is loose; the cookie URL is hard-coded (same as the existing spec).
- At 390px the chart label thinning skips some month ticks.

### 5.8 Minors the final review kept deferred

- **M1.** `index.tsx` `grid-flow-dense`: at 801-1180px the Ask card is visually before the Library card but after it in tab order. Putting Ask before Library in the DOM and restoring the 1181px+ placement with `wide:order-*` would remove the mismatch.
- **M3.** Links styled only `text-primary hover:underline` (12 sites, e.g. `search.tsx`, `NotificationsPanel.tsx`, the header's "See all") are now about 1.1:1 against body text. Restyle in step 2 (WCAG 1.4.1 where inline).
- **M4.** The `shadow-neu-inset` to none sweep removed no control's only affordance, but `admin/metadata-schema.tsx` (`border-0 bg-muted`, a 1.09:1 fill against white) is the weakest; check it in step 2.
- **M6.** Light lifecycle draft (`chart-4`) and in_review (`chart-5`) are about 1.24:1 apart and hard to tell apart in the ring; remapping one state to `chart-2` or `chart-3` in `chartTheme.ts` is a one-line change.

### 5.9 Declined to judge in the final review (out of scope)

- The mobile drawer stays open after tapping a nav link (pre-existing; no close-on-navigate on main).
- `LanguageSelector` hides Arabic unless `VITE_INCOMPLETE_LOCALES=true` (a pre-existing product decision).
- Hard-coded colours (section 5.2) (the 211 `shadow-neu*` alias uses were renamed and the aliases removed afterwards, section 5.1).
- `--border`/`--input` #e5e5e5 on white (1.26:1 input boundary): the spec's mock token and the shadcn convention; a spec-owner call.
- Browser-locale number and month formatting (`Intl.*(undefined)`): pre-existing and project-wide.
- Auth shell visuals (spec section 2).
- `select.tsx` 40px with `bg-muted` against Input 36px, and the global Textarea min-height change from 60 to 80px: primitive restyle per spec; call sites not audited (step 2).
- Enforcing the CSP (`SEDOC_CSP_ENFORCE`): deployment configuration (section 5.4).
- Dropping a file outside the Upload button navigates the tab to the file (pre-existing).
- Physical `rounded-tl-sm` on the Ask chat bubbles (pre-existing).
- Arabic grammar of individual strings beyond plural mechanics (needs a native reviewer).
- The intelligence service's general prompt-injection resistance (backend; only the client-side one-click trigger was judged, as C2).

### 5.10 Observations from the final screenshots

- At 801-1180px the attention and Ask cards share a row, so each is about 260px wide at 820px: attention titles clamp at two lines ("2 AI tag suggesti…") and the card title truncates ("Needs your atte…"). Not a regression (they were cut to one line before); a layout question for step 2.

