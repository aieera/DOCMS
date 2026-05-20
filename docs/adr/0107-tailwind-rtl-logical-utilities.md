# ADR 0107 — Tailwind RTL + logical-utility migration

Status: Accepted (mechanical migration shipped + regression guard
in place; manual review of `rounded-{tl,tr,bl,br}` and `space-x-*`
deferred — see "What's not migrated").
Date: 2026-05-19
Depends on: ADR 0106 (i18n foundation).

## Context

ADR 0106 gave the app an Arabic locale and stamped `<html dir="rtl">`
when that locale is active. By itself that buys nothing visually —
Tailwind's physical utilities (`ml-4`, `pl-2`, `text-left`,
`border-l`, `rounded-l-md`, `left-0`) hard-code physical direction
and don't flip under `dir="rtl"`. The audit identified ~310
occurrences across ~150 files.

The mechanical fix is well-known: replace physical utilities with
their logical counterparts (`ms-*`/`me-*`/`ps-*`/`pe-*`,
`text-start`/`text-end`, `border-s/border-e`, `rounded-s-*`/
`rounded-e-*`, `start-*`/`end-*`). The CSS-native logical properties
those compile to (`margin-inline-start`, `padding-inline-end`, etc.)
auto-flip under `dir="rtl"` with no JS involvement.

## What ships now

1. **`tailwindcss-rtl` plugin** installed and wired into
   `tailwind.config.js`. It supplements — not duplicates — Tailwind
   3.4's native logical utilities (Tailwind 3.3+ already provides
   `ms-*`/`me-*`/`ps-*`/`pe-*`/`start-*`/`end-*`/`border-s`/
   `border-e`/`rounded-s-*`/`rounded-e-*`/`text-start`/`text-end` and
   `rtl:`/`ltr:` variants). The plugin's real value: `space-s-*`
   horizontal-stack helpers + a couple of variants the native set
   doesn't cover.

2. **Mechanical migration** via `web/scripts/migrate-logical-tw.mjs`.
   The script rewrites `ml-/mr-/pl-/pr-/left-/right-/text-left/
   text-right/border-l/border-r/rounded-l/rounded-r` (plus their
   responsive/state-prefixed and arbitrary-value forms) to logical
   equivalents. It handles negative-margin prefixes (`-ml-4` →
   `-ms-4`), arbitrary values (`ml-[14px]` → `ms-[14px]`), and stops
   at JS identifier boundaries so `leftIcon`, `rightArrow`,
   `props.left` are untouched.

   Outcome of this run: **77 files rewritten, 214 occurrences
   migrated**.

3. **Arabic font**. `IBM Plex Sans Arabic` is preloaded in
   `web/index.html` alongside Inter, and `globals.css` applies it
   via `html[dir="rtl"] body { font-family: 'IBM Plex Sans Arabic',
   var(--font-sans); }`. Inter has no Arabic glyphs — leaving it
   active in RTL mode produces .notdef boxes for every letter.

4. **Regression guard**. `web/scripts/check-no-physical-tw.mjs` runs
   as part of `npm run lint` and fails CI if any of the migrated
   patterns reappear. Genuine physical-direction needs (SVG arrows,
   chart axes, video timelines) opt out by annotating the line
   above with `// physical-direction: intentional`.

5. **Playwright RTL shell spec** at
   `web/e2e/i18n-rtl-shell.spec.ts` verifies that
   - `?lng=ar` (or `dms_locale=ar` cookie) flips `<html dir lang>`,
   - the topbar's user-menu chip ends up in the LEFT viewport half
     (which is the right edge of an RTL layout — catches lingering
     `left-*`/`right-*` we missed),
   - a baseline screenshot is captured so visual diffs catch bidi
     breakage that text-content assertions can't.

## What's not migrated (deferred — manual review)

- **`rounded-{tl,tr,bl,br}-*`** (top-left, top-right, bottom-left,
  bottom-right corners). Tailwind 3.4 ships logical equivalents
  (`rounded-ss-*`/`rounded-se-*`/`rounded-es-*`/`rounded-ee-*`) but
  they're uncommon and the call sites tend to be intentional (a
  card's top-left corner is often the corner that should always be
  rounded regardless of direction — think the leading edge of a
  chat bubble). The migration script left them alone; the
  regression guard does NOT flag them. A future Phase-2 sweep can
  review each occurrence and decide per call.
- **`space-x-{N}`**. The prompt itself recommends per-call review:
  in many cases `flex gap-{N}` is the better swap because gap is
  inherently direction-aware. The migration script left these
  alone, and the regression guard doesn't ban them (banning would
  flood CI with non-actionable warnings).
- **Chart libraries (recharts, cytoscape) + SVG path data**. These
  use SVG transforms or props like `textAnchor="start"|"end"`, not
  Tailwind classes, so the migration script's regex never matches
  them. Confirmed by spot-check after the run.
- **Code editors / `cm-*` / Monaco styles**. Not present in this
  codebase, so nothing to skip. If a code-editor lands later, its
  internal CSS will need the marker comment.

## What we explicitly chose NOT to do

- **Auto-add `rtl:rotate-180` to "left arrow" icons**. The icon
  arrow on a "back" button SHOULD mirror in RTL; the icon arrow on
  a "next-month" calendar pager SHOULD also mirror; but the icon
  arrow on a music-player rewind button should NOT mirror (rewind
  is physically backwards regardless of language). Distinguishing
  these requires per-call judgement. Phase 2 owns this; for now
  every `ChevronLeft`/`ChevronRight` ships with its original
  orientation in both modes.
- **A full visual-diff suite across every page**. The Playwright
  spec snapshots one screen (dashboard). Adding screenshots per
  route is straightforward but explodes CI artifact size; the
  bidi-correctness check above catches the structural class of
  regressions, and the per-page visual sweep is the QA team's
  responsibility, not CI's.

## How to handle a genuinely physical-direction need

When you need to keep a physical class — e.g. an icon that
represents "left" semantically — annotate the line above:

```tsx
// physical-direction: intentional — rewind icon is physically backwards
<RewindIcon className="ml-2 h-4 w-4" />
```

The regression guard checks both the offending line and the line
above for the marker. Use it sparingly; the point of the marker is
to make the reviewer pause and ask "is this really physical?", not
to silence the rule.

## Verification

```bash
cd web

# Type-check + run vitest after the migration:
npx tsc --noEmit             # clean
npm test                     # 48/49 pass; the 1 failure is an
                             # unrelated pre-existing assertion
                             # (Button.test.tsx expects bg-red-500
                             # but the component uses bg-destructive)

# Regression guard:
npm run lint:rtl             # ✓ no regressions

# RTL shell spec:
npm run test:e2e -- i18n-rtl-shell.spec.ts
```

## Open questions deferred

- **Bidi-isolation for mixed-direction inline content.** An English
  filename inside an Arabic sentence renders with the directional
  characters bleeding into the surrounding text. `unicode-bidi: isolate`
  on `<bdi>` would fix it; needs a sweep of every component that
  renders user-provided strings.
- **`text-align: start` vs `text-align: justify`**. CSS-logical
  doesn't have a `justify` analogue; we currently preserve
  `text-justify` as-is, which is fine in both directions.
- **Per-component visual QA**. ADR 0106 promised a smoke. This ADR
  is the foundation under that smoke. The actual page-by-page
  visual sweep — every dialog, every empty state, every error
  banner — is its own follow-up.
