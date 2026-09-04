# SeDoc web — Neumorphic UI redesign (design spec)

**Date:** 2026-08-29 · **Branch:** `neu-ui` (cut from `Dms_ui` @ ec15714) · **Status:** draft for review

## 0. Decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Visual language | **Neumorphism** — soft "extruded" surfaces via dual shadows, near-monochrome, generous rounding. |
| Contrast strategy | **Neumorphic surfaces, high-contrast content.** Chrome/containers/controls are tactile & soft; text and icons stay crisp and AA+. |
| Accent | **Cool blue**, AA-tuned as both text and button fill. |
| Theme | **Light + dark** neumorphic palettes; the theme toggle is re-enabled. |
| Rollout | **Single pass** across the whole app, driven from tokens + shared primitives so all 124 routes shift together, then a component sweep. |
| Logic | **UI only.** No API calls, routes, state shape, query keys, handlers, or business rules change. Re-enabling the theme toggle is UI infrastructure, not business logic. |
| Responsiveness | **Mobile-first, fits any device.** Shadows scale down on small screens; touch targets ≥44px; RTL preserved. |

**Hard constraint:** the repo's axe accessibility gate (ADR 0120, `web/e2e/70-a11y.spec.ts`, WCAG 2.1 AA) and the keyboard + RTL e2e specs must stay green. Neumorphism's low-contrast tendencies are contained by the "high-contrast content" rule and the a11y guardrails in §3.

## 1. Goals / non-goals

**Goals**
1. One neumorphic visual foundation (tokens + shadow model + primitives) that re-skins the app without per-screen rework for most screens.
2. Every shared UI primitive (27 shadcn + ~14 loose + 8 crextio) reads as neumorphic.
3. The app shell (rail, top bar, canvas) is the flagship neumorphic surface.
4. All screens with bespoke `shadow-`/`border-`/card styling (~59 files) swept to the new system.
5. Light and dark neumorphic themes, user-switchable.
6. Fully responsive; all existing gates (vitest, tsc, lint incl. `lint:rtl`/`lint:utf8`/`lint:tenant`, Playwright a11y/keyboard/RTL) green.

**Non-goals**
- No IA/navigation change (unlike the reverted Dropbox attempt — nav structure, routes, labels stay as they are today).
- No logic, data, or endpoint changes.
- No new dependencies.
- No new features; this is a re-skin.

## 2. Design tokens

All tokens are HSL triplets in `:root` (light) and `.dark` (dark) in `web/src/styles/globals.css`, consumed via `hsl(var(--x) / <alpha-value>)`. Neumorphism adds a **shadow-token layer** on top of the existing shadcn color tokens.

### 2.1 Color tokens (target values; Task 1's contrast test enforces AA — adjust minimally if any pair misses)

**Light**
- `--background` / `--card` / `--popover` / `--muted` / `--secondary` / `--accent` / `--sidebar`: the SAME soft grey canvas `#e3e7ee` → `220 22% 91%` (neumorphic surfaces share the canvas color; depth comes from shadow, not fill). `--muted` may be a hair darker (`220 20% 88%`) for wells.
- `--foreground` / `*-foreground` (card/popover/secondary/accent/sidebar): near-black slate `#1f2733` → `217 25% 16%` (~11:1 on canvas).
- `--muted-foreground`: `#465063` → `218 17% 34%` (≥4.5:1 on canvas and on muted).
- `--primary`: cool blue tuned so white-on-primary ≥4.5:1 AND primary-as-text ≥4.5:1 on the grey canvas — start `#1e50d2` → `222 75% 47%`; `--primary-foreground` `0 0% 100%`.
- `--destructive` `#b3261e`→`3 71% 41%`; `--success` `#1d6b4c`→`157 58% 27%`; `--warning` surface `38 92% 50%` with `--warning-foreground` dark and `--warning-strong` `35 90% 27%`; `--info` = primary family. All `-foreground` white where the token is a solid fill.
- `--border` / `--input`: faint — `220 16% 80%` (a real but subtle 1px line kept under inset shadows for non-text contrast on fields).
- `--ring`: the accent, full strength.
- `--radius`: `1.25rem` (20px) — neumorphism is generously rounded.

**Dark** (`.dark`)
- canvas/card/popover/muted/secondary/accent/sidebar: `#2a2e35` → `220 12% 19%`; muted well `220 12% 16%`.
- `--foreground`: `#e6e9ef` → `220 25% 92%`.
- `--muted-foreground`: `#a7afbd` → `219 14% 70%` (≥4.5:1 on dark canvas).
- `--primary`: lighter blue `#5b8cff` → `221 100% 68%` (white or dark-on-it per contrast); `--primary-foreground` dark `220 30% 12%`.
- `--border`/`--input`: `220 10% 30%`. `--ring`: accent.

### 2.2 Shadow tokens (the neumorphic core)

Added to `:root` and `.dark`:
```
--nm-dist: 6px;              /* offset; scaled down on small screens */
--nm-blur: 14px;
--nm-light: <light highlight color>;   /* light: near-white #f7f9fc; dark: #34394? lighter than canvas */
--nm-dark:  <shadow color>;            /* light: #b9c2d0; dark: #16181c */
--nm-shadow:        var(--nm-dist) var(--nm-dist) var(--nm-blur) var(--nm-dark), calc(-1*var(--nm-dist)) calc(-1*var(--nm-dist)) var(--nm-blur) var(--nm-light);
--nm-shadow-sm:     3px 3px 7px var(--nm-dark), -3px -3px 7px var(--nm-light);
--nm-shadow-inset:  inset var(--nm-dist) var(--nm-dist) var(--nm-blur) var(--nm-dark), inset calc(-1*var(--nm-dist)) calc(-1*var(--nm-dist)) var(--nm-blur) var(--nm-light);
--nm-shadow-pressed: inset 3px 3px 7px var(--nm-dark), inset -3px -3px 7px var(--nm-light);
```
Small-screen scale-down (in globals.css): `@media (max-width: 640px){ :root{ --nm-dist:4px; --nm-blur:9px } }`.
Reduced-motion is already handled globally; neumorphism adds no essential motion.

### 2.3 Tailwind config (`tailwind.config.js`)
Add to `theme.extend.boxShadow`:
```
neu: 'var(--nm-shadow)',
'neu-sm': 'var(--nm-shadow-sm)',
'neu-inset': 'var(--nm-shadow-inset)',
'neu-pressed': 'var(--nm-shadow-pressed)',
```
Keep the existing semantic color mappings. `--radius` bump flows through the existing `borderRadius` scale.

## 3. Accessibility guardrails (keep CI green)

1. **Text/icon contrast** — every text token pair ≥4.5:1 (≥3:1 for large text), asserted by `src/styles/__tests__/neu-tokens.test.ts` in light AND dark.
2. **Focus** — `:focus-visible` shows a solid 2–3px `--ring` (accent) outline with offset; never rely on shadow change alone.
3. **Inputs & controls** — inset shadow PLUS a 1px `--input` border so the control boundary meets non-text contrast (WCAG 1.4.11); disabled state is visually distinct beyond shadow.
4. **State** — active/selected/pressed communicated by inset shadow AND an accent cue (text/icon color or a 3px accent bar), not shadow alone.
5. **Hit area** — interactive controls ≥44×44px on touch.
6. The a11y gate scans light and (re-enabled) dark; both must pass.

## 4. Component system

### 4.1 Primitives (the propagation layer)
Re-skin, preserving every prop/variant/behavior:
- **shadcn (27):** button, input, textarea, select, dialog, alert-dialog, sheet, drawer, popover, dropdown-menu, context-menu, navigation-menu, tabs, accordion, badge, avatar, progress, scroll-area, separator, tooltip, calendar, combobox, command, date-picker, checkbox (within confirm dialogs), confirm-dialog, typed-confirm-dialog.
  - Button variants → neumorphic: `default`(raised accent), `secondary`(raised surface), `outline`(flat + border), `ghost`(flat, raised on hover), `destructive`(raised, destructive text/fill); pressed = `active:shadow-neu-pressed`.
  - Input/textarea/select trigger → `shadow-neu-inset` + faint border; focus adds ring.
  - Overlays (dialog/sheet/drawer/popover/dropdown/tooltip) → raised `shadow-neu` on the canvas color, big radius, no hard border.
  - Tabs/segmented → inset track, raised active thumb.
- **loose (14):** card (raised, shadow-neu, no border, canvas fill), DataTable, EmptyState, ErrorState, Skeleton (shimmer on inset well), Spinner, SearchInput (inset), PasswordInput (inset), ViewModeToggle (segmented), FileIcon, CommandPalette, Dialog(legacy), form, label.
- **crextio (8):** warm-card, headline-metric, metric-bar, segmented-progress, task-list, timer-ring, calendar-week, vertical-bar-chart → neumorphic surfaces + accent data color.

### 4.2 Shell
- **Canvas** (`app-layout`): the soft grey base; content area is the canvas (not a raised panel-on-navy like today). Remove the navy backdrop/curved-corner treatment.
- **Rail** (`app-sidebar`): same canvas color; active item = inset "pressed into the surface" + accent text/icon + accent bar; hover = subtle raise. Collapse control is a neumorphic icon button.
- **Top bar** (`app-topbar`): flat on the canvas with a subtle bottom separation (soft shadow, not a hard border); search field is an inset pill; icon buttons are raised, press to inset.

### 4.3 Bespoke sweep (~59 files)
Replace `shadow-sm/md/lg/xl`, arbitrary `shadow-[…]`, hard `border` cards, and `bg-card`-on-`bg-background` contrasts with the neumorphic primitives/utilities. Priority: dashboard, workspace/folder tiles, document viewer chrome, admin hub cards, tasks/workflows, search, settings.

## 5. Responsive

Mobile-first; verify at 360 / 768 / 1024 / 1440. Rail → existing mobile drawer under `lg`. Grids collapse to 1–2 cols on phones. Shadow offset/blur scale down < 640px (§2.2). Touch targets ≥44px. Horizontal scroll only inside `overflow-x-auto` wrappers (tables, tab bars). RTL: logical CSS only (enforced by `lint:rtl`); shadows are direction-symmetric so they need no flip.

## 6. Testing / gates

- **Unit:** `neu-tokens.test.ts` (contrast light+dark, radius, shadow vars present); primitive smoke tests stay green (behavior unchanged).
- **e2e:** a11y gate re-scans light + dark on core routes; keyboard + RTL specs pass against the built bundle on :4173.
- **Visual:** before/after screenshots at 360/768/1440, light+dark, for `/`, `/workspaces`, a document, `/admin`, `/search`.
- **Build:** `tsc --noEmit`, `npm run lint`, `npm test -- --run`, `npm run build` all clean (pre-existing unrelated `DocumentActionsMenu.test` "Copy aria-disabled" failure excepted).

## 7. Rollout plan (one pass, foundation-first)

1. Tokens + shadow layer + tailwind + re-enable theme (light+dark) + contrast test.
2. Primitives batch A (button, card, input, textarea, select, badge, checkbox).
3. Primitives batch B (overlays, tabs, accordion, avatar, progress, tooltip, separator, scroll-area, nav/context menus).
4. Shell (layout, sidebar, topbar, breadcrumbs) + theme toggle in the top bar/user menu.
5. Loose + crextio primitives.
6. Bespoke-shadow sweep across the ~59 screens/components.
7. Responsive + RTL + dark pass.
8. Gates, visual capture, spec finalize.

## 8. Open questions — resolved (Task 10)

- **Accent blue, final value.** `--primary: 223 75% 47%` light (`#1e50d2`), `--primary: 221 100% 69%` dark (`#5b8cff`). Both AA as text and as a solid-fill button (`neu-tokens.test.ts` — `primary/background` and `primary-foreground/primary`, ≥4.5:1 in both themes). Note from the Task 10 gate sweep: `text-primary` on a `bg-primary/NN` translucent tint is a *separate* pairing the token test doesn't cover, and it can drop under 4.5:1 once the tint lightens (dark) or darkens (light) the effective background enough — three call sites (`BrowserTreeSidebar` selected item, the document-detail sub-tab, `admin/users`' "Transfer ownership" link on a tinted info panel) needed `text-foreground` instead of `text-primary` for exactly this reason. Same finding applies to `text-success`/`text-destructive` as plain label text (see below) — treat any *colored text on a translucent same-color tint* combo as needing its own contrast check, not an inherited pass from the plain-token result.
- **Count-badge recolor: red → blue.** Accepted design decision — notification/task count badges use `bg-primary`/`text-primary-foreground` (cool blue) rather than a red/destructive fill, keeping "you have N items" a neutral-attention cue distinct from the destructive-red vocabulary reserved for actual errors/danger states.
- **Categorical-legend carve-outs — accepted, out of the neumorphic monochrome sweep.** These stay on their own established palettes rather than being pulled onto the neutral/accent token set, because their whole job is per-category *differentiation*, which a single-hue system can't provide:
  - Chart/data-viz series colors (crextio `vertical-bar-chart`, dashboard KPI charts).
  - Workspace accent colors (per-workspace color tags in the sidebar/switcher).
  - NER entity-type colors (intelligence entity highlighting).
  - Tag colors (user-assigned document tags).
- **Chart/data-viz palette beyond the accent.** Resolved by the carve-out above — charts keep their existing categorical palette; only their surrounding chrome (card, axes, gridlines, tooltips) took the neumorphic surface treatment.
- **`text-success`/`text-destructive`/`text-info` as plain label text — a token-test gap, not a token defect.** `neu-tokens.test.ts` only verifies `<x>-foreground` on solid `<x>` fill (e.g. white-on-red buttons) and `primary` on `background`; it never asserted `success`/`destructive`/`info` as directly-rendered text. The Task 10 axe sweep found `text-destructive` alone is only ~3.65:1 against the dark canvas (below 4.5) regardless of any background tint, and `text-success`/`text-destructive` badges at higher tint opacities (`/15`) missed AA in light. Fixed at the call sites the gate exercises (`Badge`'s `active`/`success`/`disposed` variants, `UserTable`'s inline suspended-status pill) by keeping the colored border/tint for the visual cue and rendering the label in `text-foreground`. Any other `text-success`/`text-destructive`/`text-info` label-text usage elsewhere in the app should get the same treatment if/when a route exercising it is added to the a11y gate.
