# RTL development howto

Practical reference for engineers shipping UI in a codebase that
serves both LTR and RTL languages. Builds on ADRs 0106 (i18n
foundation), 0107 (Tailwind logical utilities), and 0108 (RTL
interaction polish).

If you are touching any UI in this repo, **read the "Default rules"
section** before anything else.

---

## Default rules

1. **Use logical utilities in className strings**. Physical
   utilities (`ml-*`, `mr-*`, `pl-*`, `pr-*`, `left-*`, `right-*`,
   `text-left`, `text-right`, `border-l`, `border-r`, `rounded-l`,
   `rounded-r`) are banned. `npm run lint` enforces this via
   `scripts/check-no-physical-tw.mjs`. The logical equivalents:

   | Don't use      | Use instead   |
   |---|---|
   | `ml-N` / `mr-N`        | `ms-N` / `me-N` |
   | `pl-N` / `pr-N`        | `ps-N` / `pe-N` |
   | `left-N` / `right-N`   | `start-N` / `end-N` |
   | `text-left` / `text-right` | `text-start` / `text-end` |
   | `border-l` / `border-r`    | `border-s` / `border-e` |
   | `rounded-l-*` / `rounded-r-*` | `rounded-s-*` / `rounded-e-*` |

2. **Don't import horizontal directional icons directly**. Instead
   of:

   ```tsx
   import { ChevronRight } from 'lucide-react'
   <ChevronRight className="h-4 w-4" />
   ```

   use:

   ```tsx
   import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
   <DirectionalIcon name="ChevronRight" className="h-4 w-4" />
   ```

   `DirectionalIcon` auto-flips horizontally in RTL so a "next"
   arrow visually follows reading order. Vertical icons
   (`ChevronUp`, `ChevronDown`, `ArrowUp`, `ArrowDown`) are
   direction-independent — keep importing them straight from
   `lucide-react`.

3. **For runtime direction logic**, read from `useDirection()`:

   ```tsx
   import { useDirection } from '@/hooks/useDirection'
   const dir = useDirection()  // 'ltr' | 'rtl'
   ```

   Never read `document.documentElement.dir` directly inside a
   component. The hook is reactive — components re-render when the
   user flips locales; the DOM attribute isn't reactive.

4. **Modal body copy uses `text-start`**. Multi-line paragraphs in
   dialogs/popovers should align to the start edge of the surface,
   not center, not the trailing edge.

---

## When physical direction is actually correct

A handful of cases are physical-by-meaning and MUST NOT flip:

- **Music / video transport icons** — rewind, fast-forward, prev-track,
  next-track. These represent direction of time, not reading order.
- **CodeMirror / Monaco gutters** — code is always LTR even in an
  Arabic document context.
- **Scientific notation, IBANs, phone numbers, IP addresses** —
  rendered LTR regardless of paragraph direction. Use `<bdi>` to
  bidi-isolate.
- **Specific arrow icons in chart axes / cytoscape graphs** — the
  arrowhead points along the edge regardless of locale.

Opt out at the call site with a comment that survives review:

```tsx
// physical-direction: intentional — rewind is always backwards in time
<DirectionalIcon name="ArrowLeft" flip={false} />

// physical-direction: intentional — left margin on the LTR code editor
<div className="ml-2 cm-editor">…</div>
```

The check-no-physical-tw lint also accepts the marker on the line
above the offending line. Use it sparingly; the point of the marker
is to make the reviewer slow down, not to silence the rule.

---

## Patterns that LOOK like RTL concerns but aren't

- **Flex / grid `gap-*`** — gap is inherently direction-aware via
  CSS logical properties. No swap needed.
- **`flex-row-reverse`** — physical reverse. If you want
  direction-aware reverse, drop `flex-row-reverse` entirely; the
  default flex direction tracks `dir` via `flex-direction: row`'s
  inline behaviour.
- **SVG `<text-anchor="start">` / `"end"`** — these are CSS-logical
  in SVG and behave correctly under `dir="rtl"`. Never use
  `text-anchor="left"`.
- **Keyboard nav** — Up/Down in dropdowns/listboxes is unaffected
  by direction. Left/Right typically isn't used for those at all.
  Carousels and date pickers are the rare exception.

---

## Tools

| Command | What it does |
|---|---|
| `npm run lint:rtl` | Static check: no physical-direction utilities outside the marker allowlist. Runs as part of `npm run lint`. |
| `npm run test:e2e -- i18n-rtl-shell.spec.ts` | Confirms `<html dir>` flips + sidebar / topbar geometry mirrors. |
| `npm run test:e2e -- i18n-rtl-interactions.spec.ts` | Confirms dialog + dropdown keyboard nav under RTL. |
| `scripts/migrate-logical-tw.mjs` | One-shot mechanical migration (ADR 0107). Re-runs are no-ops. |
| `scripts/migrate-directional-icons.mjs` | One-shot mechanical migration of `<ChevronLeft />` etc. (ADR 0108). |

---

## When adding a new locale

Today: `en` (LTR), `ar` (RTL). Adding e.g. Hebrew:

1. Add `'he'` to `SUPPORTED_LOCALES` in `web/src/i18n/index.ts`.
2. Add `'he'` to the `RTL_LANGS` set in `web/src/hooks/useDirection.ts`.
3. Add `'he'` to the DB CHECK constraint in
   `services/document/migrations/000051_user_locale.up.sql` (or its
   successor — bump the migration number, never edit a shipped one).
4. Add the corresponding entry to `SupportedLocales` in
   `services/auth/internal/service/profile.go`.
5. Create the 8 namespace bundles at
   `web/public/locales/he/{common,auth,documents,admin,signatures,
   intelligence,workflows,errors}.json`.
6. Pick a Hebrew web font and add it to `index.html` + the
   `html[dir="rtl"] body` rule in `globals.css` (or change the
   selector to be locale-specific if Hebrew shouldn't use IBM Plex
   Sans Arabic).
7. Add the LanguageSelector option in
   `web/src/components/shared/LanguageSelector.tsx` — labels stay
   in the NATIVE script so the user always recognises their own
   language.

For an LTR-RTL-LTR script like Mongolian Cyrillic vs Mongolian
Traditional, the per-locale font + dir choice deserves its own ADR.

---

## What's NOT covered yet

- **Bidi isolation for mixed-direction inline content.** An English
  filename inside an Arabic sentence: today's rendering can let
  directional characters bleed into the surrounding text. Wrapping
  with `<bdi>` fixes it. We haven't done a sweep of every
  user-provided-string render site.
- **Per-component visual diff**. The Playwright specs catch the
  structural class of regressions but don't snapshot every dialog,
  empty state, or error banner. A QA visual sweep is its own
  follow-up.
- **Arabic copy itself.** ADR 0106 set up the i18n pipe; the actual
  string-by-string Arabic translation is Prompt 4's scope. Most UI
  still shows English copy with the layout mirrored.
