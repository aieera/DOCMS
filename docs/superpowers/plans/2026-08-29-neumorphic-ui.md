# Neumorphic UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Re-skin the entire SeDoc web app in a neumorphic visual language (soft dual-shadow surfaces, high-contrast content, cool-blue accent, light + dark) without changing any logic, keeping every CI gate green.

**Architecture:** A shadow-token layer + recolored palette in `globals.css` (light + dark) plus four Tailwind `boxShadow` utilities drive the look. Re-skinning the ~40 shared UI primitives propagates neumorphism to all 124 routes; a bespoke-styling sweep catches the rest. Only `className`/token/style changes — no props, handlers, routes, queries, or state shapes change. The disabled theme toggle is re-enabled (UI infra).

**Tech Stack:** React 18, TypeScript, Vite 5, Tailwind 3.4 (+ tailwindcss-rtl, class-variance-authority, tailwind-merge via `@/lib/cn`), Radix/shadcn primitives, lucide-react, react-i18next, vitest + Testing Library + axe-core, Playwright.

**Spec:** `docs/superpowers/specs/2026-08-29-neumorphic-ui-design.md`

## Global Constraints

- Branch `neu-ui` (cut from Dms_ui @ ec15714). Run `git status --short` before each commit; commit only the files the task names. Commit-message trailer on every commit body: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- **UI only.** Do not change component props, event handlers, API calls, routes/paths, TanStack query keys, Zustand store shapes, or any business logic. Changes are limited to `className`, `style`, CSS tokens, `tailwind.config.js`, and re-enabling the theme provider/toggle.
- **No new dependencies.**
- Token values are HSL triplets (`"223 75% 47%"`), never `hsl()` wrappers, in `:root`/`.dark`. Every text pair listed in Task 1's test must be ≥4.5:1 in BOTH themes.
- Neumorphic surfaces use the shared shadow utilities `shadow-neu / shadow-neu-sm / shadow-neu-inset / shadow-neu-pressed`; do not hand-roll `shadow-[...]` for surfaces.
- A11y guardrails: visible `:focus-visible` accent ring on every interactive control (never shadow-only); inputs/controls keep a 1px `--input` border under inset shadow; active/selected state carries an accent cue besides shadow; touch targets ≥44px.
- Logical CSS only for horizontal position/spacing (`ms-/me-/ps-/pe-/start-/end-`, `border-s/e`) — enforced by `npm run lint` (which chains `eslint && lint:rtl && lint:utf8 && lint:tenant`).
- The dev server (`sedoc-web` systemd unit) serves the tree with HMR on :3000 — do NOT start another `npm run dev`; verify visually at `http://192.168.70.22:3000`.
- Local gates: `npx tsc --noEmit`, `npm run lint`, `npm test -- --run`, `npm run build`. Playwright per Task 10.
- Known pre-existing unrelated unit failure: `DocumentActionsMenu.test` "Copy aria-disabled" — do not fix, do not let it mask new failures.

---

## File structure

| Path | Responsibility |
|---|---|
| `web/src/styles/globals.css` | Modify: `:root` + `.dark` blocks (palette + shadow tokens), small-screen shadow media query. |
| `web/tailwind.config.js` | Modify: add `boxShadow` neu utilities. |
| `web/src/styles/__tests__/neu-tokens.test.ts` | Create: contrast (light+dark), radius, shadow-var presence. |
| `web/src/components/layout/theme-provider.tsx` | Modify: honor stored mode + system, apply `.dark`. |
| `web/index.html` | Modify: restore the theme boot script; neumorphic `theme-color`. |
| `web/src/components/layout/theme-toggle.tsx` | Modify/verify: the toggle control (light/dark/system). |
| `web/src/components/ui/shadcn/*.tsx` | Modify: neumorphic re-skin of the 27 shadcn primitives (Tasks 2,3,4,5). |
| `web/src/components/ui/*.tsx`, `web/src/components/ui/crextio/*.tsx` | Modify: loose + crextio primitives (Task 7). |
| `web/src/components/layout/{app-layout,app-sidebar,app-topbar,breadcrumbs}.tsx` | Modify: neumorphic shell (Task 6). |
| ~59 files using `shadow-`/bespoke cards | Modify: sweep to primitives/utilities (Task 8). |
| `web/e2e/70-a11y.spec.ts` | Modify: re-add a dark-theme scan (Task 10). |

---

### Task 1: Neumorphic foundation — tokens, shadow layer, Tailwind, theme re-enable, contrast test

**Files:**
- Create: `web/src/styles/__tests__/neu-tokens.test.ts`
- Modify: `web/src/styles/globals.css` (`:root` and `.dark` blocks; add a small-screen media query)
- Modify: `web/tailwind.config.js` (`theme.extend.boxShadow`)
- Modify: `web/src/components/layout/theme-provider.tsx`
- Modify: `web/index.html`

**Interfaces:**
- Consumes: nothing.
- Produces: the CSS custom properties `--nm-dist --nm-blur --nm-light --nm-dark --nm-shadow --nm-shadow-sm --nm-shadow-inset --nm-shadow-pressed` in both themes; Tailwind utilities `shadow-neu shadow-neu-sm shadow-neu-inset shadow-neu-pressed`; the recolored color tokens (same names as today); a working `useTheme()` whose `resolved` follows the stored mode/system and toggles the `.dark` class.

- [ ] **Step 1: Write the failing contrast/token test**

Create `web/src/styles/__tests__/neu-tokens.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const css = readFileSync(resolve(__dirname, '../globals.css'), 'utf8')
function block(sel: string): string {
  const start = css.indexOf(sel)
  if (start < 0) throw new Error(`missing block ${sel}`)
  const open = css.indexOf('{', start)
  let depth = 0, i = open
  for (; i < css.length; i++) { if (css[i] === '{') depth++; else if (css[i] === '}') { depth--; if (depth === 0) break } }
  return css.slice(open, i)
}
const light = block(':root')
const dark = block('.dark')

function token(scope: string, name: string): [number, number, number] {
  const m = scope.match(new RegExp(`--${name}:\\s*([\\d.]+)\\s+([\\d.]+)%\\s+([\\d.]+)%`))
  if (!m) throw new Error(`token --${name} not found`)
  return [Number(m[1]), Number(m[2]), Number(m[3])]
}
function hslToRgb([h, s, l]: [number, number, number]): [number, number, number] {
  const S = s / 100, L = l / 100
  const c = (1 - Math.abs(2 * L - 1)) * S
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1))
  const m = L - c / 2
  const [r, g, b] = h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x] : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x]
  return [(r + m) * 255, (g + m) * 255, (b + m) * 255]
}
function lum(rgb: [number, number, number]): number {
  const [r, g, b] = rgb.map((v) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4) })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
function contrast(scope: string, fg: string, bg: string): number {
  const l1 = lum(hslToRgb(token(scope, fg))), l2 = lum(hslToRgb(token(scope, bg)))
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}
const PAIRS: [string, string][] = [
  ['foreground', 'background'], ['foreground', 'card'], ['foreground', 'muted'],
  ['muted-foreground', 'background'], ['muted-foreground', 'muted'], ['muted-foreground', 'card'],
  ['primary-foreground', 'primary'], ['primary', 'background'],
  ['destructive-foreground', 'destructive'], ['success-foreground', 'success'],
  ['info-foreground', 'info'], ['sidebar-foreground', 'sidebar'],
]

for (const [label, scope] of [['light', light], ['dark', dark]] as const) {
  describe(`neumorphic tokens — ${label} AA text contrast`, () => {
    it.each(PAIRS)(`--%s on --%s ≥ 4.5:1 (${label})`, (fg, bg) => {
      expect(contrast(scope, fg, bg)).toBeGreaterThanOrEqual(4.5)
    })
  })
}
describe('neumorphic shape + shadow layer', () => {
  it('radius is 1.25rem', () => { expect(light).toMatch(/--radius:\s*1\.25rem/) })
  it('defines the neumorphic shadow vars in light and dark', () => {
    for (const s of [light, dark]) for (const v of ['--nm-shadow', '--nm-shadow-inset', '--nm-light', '--nm-dark']) {
      expect(s).toContain(v)
    }
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run src/styles/__tests__/neu-tokens.test.ts`
Expected: FAIL — `.dark` token values are the old warm palette (some pairs may pass, but `--nm-*` vars are missing and `--radius` is `1rem`, so the shape/shadow tests fail; several dark pairs differ).

- [ ] **Step 3: Replace the `:root` block in `web/src/styles/globals.css`**

Replace the whole `:root { … }` block (the light palette) with:

```css
  :root {
    /* Neumorphic light palette (spec 2026-08-29). Surfaces share the
     * canvas color; depth comes from dual shadows (--nm-*), not fill.
     * Text/icons stay high-contrast (verified by neu-tokens.test.ts). */
    --background: 220 22% 91%;   /* #e3e7ee soft cool-grey canvas */
    --foreground: 217 25% 16%;   /* #1f2733 near-black slate (~11:1) */

    --muted: 220 20% 87%;        /* recessed well, slightly darker */
    --muted-foreground: 218 17% 34%;  /* #465063 (>=4.5 on canvas+muted) */

    --popover: 220 22% 91%;
    --popover-foreground: 217 25% 16%;
    --card: 220 22% 91%;          /* SAME as canvas — neumorphic */
    --card-foreground: 217 25% 16%;

    --border: 220 16% 80%;        /* faint real line kept under insets */
    --input: 220 16% 78%;

    --primary: 223 75% 47%;       /* #1e50d2 cool blue; AA both ways */
    --primary-foreground: 0 0% 100%;

    --secondary: 220 22% 91%;
    --secondary-foreground: 217 25% 16%;
    --accent: 220 20% 87%;
    --accent-foreground: 217 25% 16%;

    --destructive: 3 71% 42%;     /* #b3271f */
    --destructive-foreground: 0 0% 100%;
    --success: 157 55% 27%;       /* #1f6b4c */
    --success-foreground: 0 0% 100%;
    --warning: 38 92% 50%;
    --warning-foreground: 30 40% 14%;
    --warning-strong: 35 90% 27%;
    --info: 223 75% 47%;
    --info-foreground: 0 0% 100%;

    --ring: 223 75% 47%;
    --radius: 1.25rem;

    /* Sidebar shares the canvas (neumorphic rail). */
    --sidebar: 220 22% 91%;
    --sidebar-foreground: 217 25% 16%;
    --sidebar-border: 220 16% 80%;
    --sidebar-accent: 223 75% 47%;
    --sidebar-accent-foreground: 0 0% 100%;

    /* Folder/feature tints reduced to one accent-blue family. */
    --folder-sage: 220 20% 87%;
    --folder-sage-foreground: 223 75% 40%;
    --folder-indigo: 220 20% 87%;
    --folder-indigo-foreground: 223 75% 40%;
    --folder-coral: 220 20% 87%;
    --folder-coral-foreground: 223 75% 40%;

    /* Legacy aliases (kept so var(--color-*) consumers don't break). */
    --color-bg: hsl(var(--background));
    --color-bg-secondary: hsl(var(--card));
    --color-text: hsl(var(--foreground));
    --color-text-secondary: hsl(var(--muted-foreground));
    --color-primary: hsl(var(--primary));
    --color-primary-hover: hsl(223 75% 40%);
    --color-border: hsl(var(--border));
    --color-accent: hsl(var(--accent));
    --color-danger: hsl(var(--destructive));
    --color-success: hsl(var(--success));
    --color-warning: hsl(var(--warning));

    --font-sans: 'Inter', system-ui, sans-serif;
    --font-mono: 'JetBrains Mono', monospace;

    /* Neumorphic shadow layer. */
    --nm-dist: 6px;
    --nm-blur: 14px;
    --nm-light: 220 30% 98%;   /* highlight ≈ #f7f9fc */
    --nm-dark: 220 22% 74%;    /* shadow ≈ #b3bcce */
    --nm-shadow: var(--nm-dist) var(--nm-dist) var(--nm-blur) hsl(var(--nm-dark)), calc(-1 * var(--nm-dist)) calc(-1 * var(--nm-dist)) var(--nm-blur) hsl(var(--nm-light));
    --nm-shadow-sm: 3px 3px 7px hsl(var(--nm-dark)), -3px -3px 7px hsl(var(--nm-light));
    --nm-shadow-inset: inset var(--nm-dist) var(--nm-dist) var(--nm-blur) hsl(var(--nm-dark)), inset calc(-1 * var(--nm-dist)) calc(-1 * var(--nm-dist)) var(--nm-blur) hsl(var(--nm-light));
    --nm-shadow-pressed: inset 3px 3px 7px hsl(var(--nm-dark)), inset -3px -3px 7px hsl(var(--nm-light));
  }
```

- [ ] **Step 4: Replace the `.dark` block**

Replace the whole `.dark { … }` block with:

```css
  .dark {
    --background: 218 13% 19%;   /* #2a2e35 */
    --foreground: 220 25% 92%;   /* #e6e9ef */
    --muted: 218 13% 15%;
    --muted-foreground: 219 14% 70%;  /* #a7afbd (>=4.5 on dark canvas) */
    --popover: 218 13% 19%;
    --popover-foreground: 220 25% 92%;
    --card: 218 13% 19%;
    --card-foreground: 220 25% 92%;
    --border: 220 10% 30%;
    --input: 220 10% 32%;
    --primary: 221 100% 68%;     /* #5b8cff */
    --primary-foreground: 220 40% 12%;
    --secondary: 218 13% 19%;
    --secondary-foreground: 220 25% 92%;
    --accent: 218 13% 15%;
    --accent-foreground: 220 25% 92%;
    --destructive: 3 74% 60%;
    --destructive-foreground: 220 40% 12%;
    --success: 157 45% 55%;
    --success-foreground: 220 40% 12%;
    --warning: 38 90% 60%;
    --warning-foreground: 30 40% 12%;
    --warning-strong: 38 90% 68%;
    --info: 221 100% 68%;
    --info-foreground: 220 40% 12%;
    --ring: 221 100% 68%;
    --sidebar: 218 13% 19%;
    --sidebar-foreground: 220 25% 92%;
    --sidebar-border: 220 10% 30%;
    --sidebar-accent: 221 100% 68%;
    --sidebar-accent-foreground: 220 40% 12%;
    --folder-sage: 218 13% 15%;
    --folder-sage-foreground: 221 100% 72%;
    --folder-indigo: 218 13% 15%;
    --folder-indigo-foreground: 221 100% 72%;
    --folder-coral: 218 13% 15%;
    --folder-coral-foreground: 221 100% 72%;
    --color-bg-secondary: hsl(var(--card));
    --color-accent: hsl(var(--accent));
    --nm-dist: 6px;
    --nm-blur: 14px;
    --nm-light: 218 13% 25%;   /* highlight lighter than canvas */
    --nm-dark: 220 14% 9%;     /* shadow darker than canvas */
    --nm-shadow: var(--nm-dist) var(--nm-dist) var(--nm-blur) hsl(var(--nm-dark)), calc(-1 * var(--nm-dist)) calc(-1 * var(--nm-dist)) var(--nm-blur) hsl(var(--nm-light));
    --nm-shadow-sm: 3px 3px 7px hsl(var(--nm-dark)), -3px -3px 7px hsl(var(--nm-light));
    --nm-shadow-inset: inset var(--nm-dist) var(--nm-dist) var(--nm-blur) hsl(var(--nm-dark)), inset calc(-1 * var(--nm-dist)) calc(-1 * var(--nm-dist)) var(--nm-blur) hsl(var(--nm-light));
    --nm-shadow-pressed: inset 3px 3px 7px hsl(var(--nm-dark)), inset -3px -3px 7px hsl(var(--nm-light));
  }
```

Then add, after the `.dark` block's closing brace (inside `@layer base` if the file uses one, else at top level), the small-screen shadow scale-down:

```css
  @media (max-width: 640px) {
    :root, .dark { --nm-dist: 4px; --nm-blur: 9px; }
  }
```

- [ ] **Step 5: Add the Tailwind boxShadow utilities**

In `web/tailwind.config.js`, add to `theme.extend`:

```js
      boxShadow: {
        neu: 'var(--nm-shadow)',
        'neu-sm': 'var(--nm-shadow-sm)',
        'neu-inset': 'var(--nm-shadow-inset)',
        'neu-pressed': 'var(--nm-shadow-pressed)',
      },
```

- [ ] **Step 6: Re-enable the theme provider**

In `web/src/components/layout/theme-provider.tsx`, replace the pinned-light body of `ThemeProvider` so it resolves and applies the real theme:

```tsx
export function ThemeProvider({ children, defaultMode = 'system' }: { children: ReactNode; defaultMode?: ThemeMode }) {
  const [mode, setModeState] = useState<ThemeMode>(() => {
    if (typeof window === 'undefined') return defaultMode
    const stored = window.localStorage.getItem(STORAGE_KEY) as ThemeMode | null
    return stored ?? defaultMode
  })
  const [resolved, setResolved] = useState<'light' | 'dark'>('light')

  useEffect(() => {
    const mql = window.matchMedia('(prefers-color-scheme: dark)')
    const compute = (): 'light' | 'dark' => (mode === 'system' ? (mql.matches ? 'dark' : 'light') : mode)
    const apply = () => { const r = compute(); setResolved(r); applyClass(r) }
    apply()
    window.localStorage.setItem(STORAGE_KEY, mode)
    if (mode === 'system') {
      mql.addEventListener('change', apply)
      return () => mql.removeEventListener('change', apply)
    }
  }, [mode])

  const setMode = (m: ThemeMode) => setModeState(m)
  return (
    <ThemeContext.Provider value={{ mode, resolved, setMode }}>
      {children}
    </ThemeContext.Provider>
  )
}
```

In `web/index.html`, restore the pre-mount boot script inside `<head>` (prevents a flash) — it reads the same `vaultdms-theme` key:

```html
    <script>
      (function () {
        try {
          var stored = localStorage.getItem('vaultdms-theme');
          var mode = stored || 'system';
          var resolved = mode === 'system'
            ? (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
            : mode;
          if (resolved === 'dark') document.documentElement.classList.add('dark');
          document.documentElement.style.colorScheme = resolved;
        } catch (e) {}
      })();
    </script>
```

and set `<meta name="theme-color" content="#e3e7ee" />` (add or update).

- [ ] **Step 7: Run the token test, type-check, view the app**

Run: `cd web && npm test -- --run src/styles/__tests__/neu-tokens.test.ts && npx tsc --noEmit`
Expected: all contrast pairs (light+dark) PASS, radius + shadow-var tests PASS, tsc clean. If any contrast pair fails, adjust that token's L% minimally (keep hue) and re-run.

Open `http://192.168.70.22:3000/` — the canvas is soft grey; existing `shadow-*` cards still look old (Tasks 2–8 convert them). Toggle dark via devtools adding `.dark` to `<html>` to sanity-check the dark palette.

- [ ] **Step 8: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/styles/globals.css web/src/styles/__tests__/neu-tokens.test.ts web/tailwind.config.js web/src/components/layout/theme-provider.tsx web/index.html
git commit -m "feat(web): neumorphic token + shadow foundation, dark theme re-enabled"
```

---

### Task 2: Core surface primitives — Button, Card, Badge

**Files:**
- Modify: `web/src/components/ui/shadcn/button.tsx`
- Modify: `web/src/components/ui/card.tsx`
- Modify: `web/src/components/ui/shadcn/badge.tsx`
- Test: `web/src/components/ui/__tests__/neu-button.test.tsx` (create)

**Interfaces:**
- Consumes: Task 1 shadow utilities + tokens.
- Produces: same exports/props (`Button`+`buttonVariants`, `Card`+subcomponents, `Badge`+`badgeVariants`) — only classes change.

- [ ] **Step 1: Write a failing class-contract test**

Create `web/src/components/ui/__tests__/neu-button.test.tsx`:

```tsx
import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'

describe('neumorphic Button/Card', () => {
  it('default button is a raised neumorphic accent surface that presses in', () => {
    const { getByRole } = render(<Button>Go</Button>)
    const c = getByRole('button').className
    expect(c).toContain('shadow-neu')
    expect(c).toContain('active:shadow-neu-pressed')
    expect(c).toContain('bg-primary')
  })
  it('card is a raised neumorphic surface with no hard border', () => {
    const { container } = render(<Card>x</Card>)
    const c = (container.firstChild as HTMLElement).className
    expect(c).toContain('shadow-neu')
    expect(c).not.toMatch(/\bborder\b/)
  })
})
```

Run: `cd web && npm test -- --run src/components/ui/__tests__/neu-button.test.tsx` → FAIL.

- [ ] **Step 2: Re-skin Button**

In `web/src/components/ui/shadcn/button.tsx`, change ONLY the base string and `variant` map inside `buttonVariants` (keep everything else — props, `loading`, `asChild`, `size`, exports):

Base string (replace the `cn(...)` first arg):
```
cn(
  'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg text-sm font-medium',
  'ring-offset-background transition-[box-shadow,background-color,color] duration-150',
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
  'disabled:pointer-events-none disabled:opacity-50 disabled:shadow-none',
  '[&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0',
)
```
`variant` map:
```
default: 'bg-primary text-primary-foreground shadow-neu-sm hover:brightness-110 active:shadow-neu-pressed',
destructive: 'bg-destructive text-destructive-foreground shadow-neu-sm hover:brightness-110 active:shadow-neu-pressed',
outline: 'bg-background text-foreground border border-input shadow-neu-sm hover:text-primary active:shadow-neu-pressed',
secondary: 'bg-secondary text-secondary-foreground shadow-neu-sm hover:text-primary active:shadow-neu-pressed',
ghost: 'text-foreground hover:shadow-neu-sm hover:text-primary active:shadow-neu-pressed',
link: 'text-primary underline-offset-4 hover:underline',
```
Keep `size` but bump the icon radius: leave sizes as-is (radius comes from base `rounded-lg`).

- [ ] **Step 3: Re-skin Card**

In `web/src/components/ui/card.tsx`, change the `Card` root `cn(...)` to:
```
cn('rounded-2xl bg-card text-card-foreground shadow-neu', className)
```
(remove the `border border-border/70` and the bespoke `shadow-[...]`). Leave header/title/description/content/footer unchanged.

- [ ] **Step 4: Re-skin Badge**

In `web/src/components/ui/shadcn/badge.tsx`, for each variant keep the color mapping but drop hard borders and add `shadow-neu-sm` to solid variants; `outline` keeps `border border-input`. (Read the file; apply the same recipe: solid = `bg-* text-* shadow-neu-sm`, no `border`.)

- [ ] **Step 5: Run tests + tsc**

Run: `cd web && npm test -- --run src/components/ui/__tests__/neu-button.test.tsx && npm test -- --run && npx tsc --noEmit`
Expected: new test PASS; full suite green (known failure aside); tsc clean. View a few pages on :3000 — buttons and cards now read neumorphic.

- [ ] **Step 6: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components/ui/shadcn/button.tsx web/src/components/ui/card.tsx web/src/components/ui/shadcn/badge.tsx web/src/components/ui/__tests__/neu-button.test.tsx
git commit -m "feat(web): neumorphic Button, Card, Badge"
```

---

### Task 3: Inputs & fields — Input, Textarea, Select, SearchInput, PasswordInput

**Files:** Modify `web/src/components/ui/shadcn/input.tsx`, `.../textarea.tsx`, `.../select.tsx`, `web/src/components/ui/SearchInput.tsx`, `web/src/components/ui/PasswordInput.tsx`. Test: `web/src/components/ui/__tests__/neu-input.test.tsx` (create).

**Interfaces:** Consumes Task 1. Produces same exports/props; fields become inset wells with a faint border + focus ring.

- [ ] **Step 1: Failing test**

Create `web/src/components/ui/__tests__/neu-input.test.tsx`:
```tsx
import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { Input } from '@/components/ui/shadcn/input'
describe('neumorphic Input', () => {
  it('is an inset well with a real border and a focus ring', () => {
    const { getByRole } = render(<Input aria-label="x" />)
    const c = getByRole('textbox').className
    expect(c).toContain('shadow-neu-inset')
    expect(c).toContain('border-input')
    expect(c).toContain('focus-visible:ring-2')
  })
})
```
Run → FAIL.

- [ ] **Step 2: Re-skin Input**

In `web/src/components/ui/shadcn/input.tsx`, change the `<input>` `cn(...)` base (keep sugar props, ids, error logic) to:
```
'flex h-10 w-full rounded-lg border border-input bg-muted px-3 py-1 text-sm text-foreground shadow-neu-inset transition-shadow',
'file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground',
'placeholder:text-muted-foreground',
'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
'disabled:cursor-not-allowed disabled:opacity-50',
'aria-[invalid=true]:border-destructive aria-[invalid=true]:focus-visible:ring-destructive',
icon && 'ps-9',
className,
```
(h-9→h-10 for touch; `shadow-sm`→`shadow-neu-inset`; `bg-background`→`bg-muted` so the well reads; `ring-1`→`ring-2` + offset.)

- [ ] **Step 3: Apply the same inset recipe to Textarea and the Select trigger**

Read `textarea.tsx` and `select.tsx`; on the field/trigger element replace `border … bg-background shadow-sm` with `border border-input bg-muted shadow-neu-inset` and upgrade the focus ring to `ring-2 … ring-offset-2`. The Select **content/dropdown** panel gets `shadow-neu` + `rounded-xl` and drops any hard border (that panel is a raised surface — same as overlays in Task 4).

- [ ] **Step 4: SearchInput & PasswordInput**

These wrap `Input`; verify they inherit the inset look. If either adds its own `border`/`shadow-sm` wrapper, swap to the inset recipe. Keep the show/hide button in PasswordInput as a neumorphic ghost icon-button (`shadow-neu-sm` on hover, `active:shadow-neu-pressed`).

- [ ] **Step 5: Tests + tsc**

Run: `cd web && npm test -- --run src/components/ui/__tests__/neu-input.test.tsx && npm test -- --run && npx tsc --noEmit` → PASS + green. View a form on :3000.

- [ ] **Step 6: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components/ui/shadcn/input.tsx web/src/components/ui/shadcn/textarea.tsx web/src/components/ui/shadcn/select.tsx web/src/components/ui/SearchInput.tsx web/src/components/ui/PasswordInput.tsx web/src/components/ui/__tests__/neu-input.test.tsx
git commit -m "feat(web): neumorphic inset form fields"
```

---

### Task 4: Overlays — dialog, alert-dialog, sheet, drawer, popover, dropdown-menu, context-menu, tooltip, command

**Files:** Modify each of `web/src/components/ui/shadcn/{dialog,alert-dialog,sheet,drawer,popover,dropdown-menu,context-menu,tooltip,command}.tsx`. No new test file (behavior unchanged; covered by existing suites + Task 10 a11y).

**Interfaces:** Consumes Task 1. Produces same exports/props.

- [ ] **Step 1: Apply the raised-surface recipe to every overlay content panel**

For each file, on the *content/panel* element (the `*Content` primitive) replace hard borders + bespoke shadows with the neumorphic raised surface, preserving all Radix props, `forwardRef`, animation data-attributes, and `cn(className)` passthrough:
- Replace `border bg-popover … shadow-md`/`shadow-lg` with `bg-popover text-popover-foreground shadow-neu rounded-2xl` (drop `border`).
- Menu **items** (dropdown/context/command/select items): selected/active state uses `focus:bg-accent focus:text-primary` — keep, no shadow on rows.
- Tooltip: smaller radius `rounded-lg` + `shadow-neu-sm` (tooltips are tiny).
- Sheet/Drawer panels: `shadow-neu` on the canvas color, keep the slide animations; the edge that meets the viewport keeps no border.
- The overlay/backdrop (`*Overlay`) stays a translucent scrim (`bg-black/40` or existing) — unchanged.

- [ ] **Step 2: Verify + tsc + focused a11y sanity**

Run: `cd web && npx tsc --noEmit && npm test -- --run`
Expected: green. Open a dialog and a dropdown on :3000 (e.g. a document actions menu) — panels read as raised neumorphic cards; focus rings visible on items.

- [ ] **Step 3: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components/ui/shadcn/dialog.tsx web/src/components/ui/shadcn/alert-dialog.tsx web/src/components/ui/shadcn/sheet.tsx web/src/components/ui/shadcn/drawer.tsx web/src/components/ui/shadcn/popover.tsx web/src/components/ui/shadcn/dropdown-menu.tsx web/src/components/ui/shadcn/context-menu.tsx web/src/components/ui/shadcn/tooltip.tsx web/src/components/ui/shadcn/command.tsx
git commit -m "feat(web): neumorphic overlay surfaces"
```

---

### Task 5: Structural primitives — tabs, accordion, avatar, progress, separator, scroll-area, navigation-menu, calendar, combobox, date-picker

**Files:** Modify each of `web/src/components/ui/shadcn/{tabs,accordion,avatar,progress,separator,scroll-area,navigation-menu,calendar,combobox,date-picker}.tsx`.

**Interfaces:** Consumes Task 1. Same exports/props.

- [ ] **Step 1: Apply per-primitive neumorphic recipe**

- **tabs:** `TabsList` = inset track (`bg-muted shadow-neu-inset rounded-xl p-1`); `TabsTrigger` active = raised thumb (`data-[state=active]:bg-background data-[state=active]:shadow-neu-sm data-[state=active]:text-primary`), inactive = `text-muted-foreground`.
- **accordion:** items on the canvas; the trigger row raises on hover (`hover:shadow-neu-sm rounded-lg`); drop hard dividers or keep a faint `border-border`.
- **avatar:** circular with `shadow-neu-sm`; fallback keeps `bg-muted text-muted-foreground`.
- **progress:** track = inset well (`bg-muted shadow-neu-inset`); indicator = `bg-primary`.
- **separator:** replace the 1px line with a subtle engraved look: keep `bg-border` but height 1px (neumorphism often uses a faint groove — a single `bg-border` line is acceptable and AA-neutral).
- **scroll-area:** thumb `bg-border rounded-full`; no change to behavior.
- **navigation-menu:** trigger/content use the overlay recipe from Task 4 for the popover content; triggers are ghost neumorphic buttons.
- **calendar / date-picker / combobox:** day cells and options: selected = `bg-primary text-primary-foreground shadow-neu-sm rounded-lg`; today = accent ring; the popover panel uses the Task 4 raised recipe.

- [ ] **Step 2: Verify**

Run: `cd web && npx tsc --noEmit && npm test -- --run` → green. View tabs (e.g. document detail) and a date picker on :3000.

- [ ] **Step 3: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components/ui/shadcn/tabs.tsx web/src/components/ui/shadcn/accordion.tsx web/src/components/ui/shadcn/avatar.tsx web/src/components/ui/shadcn/progress.tsx web/src/components/ui/shadcn/separator.tsx web/src/components/ui/shadcn/scroll-area.tsx web/src/components/ui/shadcn/navigation-menu.tsx web/src/components/ui/shadcn/calendar.tsx web/src/components/ui/shadcn/combobox.tsx web/src/components/ui/shadcn/date-picker.tsx
git commit -m "feat(web): neumorphic structural primitives"
```

---

### Task 6: The shell — layout, sidebar, top bar, breadcrumbs, theme toggle

**Files:** Modify `web/src/components/layout/{app-layout,app-sidebar,app-topbar,breadcrumbs,theme-toggle}.tsx`. Update `web/src/components/layout/__tests__/*` if any assert on the old navy classes.

**Interfaces:** Consumes Tasks 1–5. Exports/props unchanged (`AppLayout`, `AppSidebar`, `MobileSidebar`, `SidebarContent`, `AppTopbar`, `Breadcrumbs`).

- [ ] **Step 1: Canvas (app-layout)**

Remove the navy backdrop + curved-corner panel treatment: the wrapper `div` and `main` both sit on `bg-background` (the soft canvas). Delete `bg-sidebar` on the flex wrapper and `rounded-tl-[28px] bg-background` special-casing; `main` is plain `bg-background`. Keep the skip link, the `lg:ps-[260px]`/collapsed offset, and the single-scroll structure.

- [ ] **Step 2: Rail (app-sidebar)**

- Rail container: `bg-sidebar` (= canvas) with a soft right edge (`shadow-neu-sm` on the aside, or a faint `border-e border-sidebar-border`).
- Active nav item: `bg-background shadow-neu-inset text-primary` (pressed-in) + keep the accent leading bar; inactive: `text-sidebar-foreground hover:shadow-neu-sm rounded-lg`.
- Replace every `bg-white/…`, `text-white`, `hover:bg-white/…`, `ring-white/…` with token classes (`text-sidebar-foreground`, `hover:text-primary`, `focus-visible:ring-ring`). Brand mark: raised `shadow-neu-sm` tile in `bg-primary text-primary-foreground`.
- Collapse button + mobile drawer: neumorphic ghost icon-button; drawer panel `bg-sidebar shadow-neu`.
- Acceptance grep (must be empty): `grep -nE "white/|text-white|bg-sidebar-accent[^-]|navy" web/src/components/layout/app-sidebar.tsx` — actually just ensure no `white/` or `text-white` remain: `grep -nE "white/|text-white" web/src/components/layout/app-sidebar.tsx`.

- [ ] **Step 3: Top bar (app-topbar)**

- Header: `bg-background` with a soft bottom separation (`shadow-neu-sm` or a hairline `border-b border-border`); remove `bg-sidebar`/`text-sidebar-foreground`/`white/*`.
- Search field: inset pill (`bg-muted shadow-neu-inset rounded-full`) with focus ring.
- Icon buttons (tasks, notifications, language, avatar): neumorphic ghost (`hover:shadow-neu-sm active:shadow-neu-pressed rounded-lg`); count badges `bg-primary text-primary-foreground`.
- Acceptance grep empty: `grep -nE "white/|text-white|bg-sidebar|text-sidebar-foreground" web/src/components/layout/app-topbar.tsx`.

- [ ] **Step 4: Breadcrumbs**

Replace `text-sidebar-foreground/NN` (any opacity) → `text-muted-foreground`; `text-white`/`hover:text-white` → `text-foreground`/`hover:text-foreground`; current crumb → `text-foreground font-semibold`. Grep empty: `grep -nE "white|sidebar-foreground" web/src/components/layout/breadcrumbs.tsx`.

- [ ] **Step 5: Theme toggle**

`theme-toggle.tsx` exists but was unmounted. Ensure it renders a light/dark/system control (a neumorphic icon-button or a small segmented control) driven by `useTheme()`; mount it in the top bar's right cluster (in `app-topbar.tsx`) OR the user menu. It must be keyboard-operable and labelled (`aria-label`). Add i18n keys `common.theme.{light,dark,system,label}` to `public/locales/{en,ar}/common.json` if the control shows text.

- [ ] **Step 6: Verify + tests**

Run: `cd web && npm test -- --run src/components/layout && npx tsc --noEmit && npm run lint`
Expected: layout tests green (update any that asserted old navy classes — do NOT delete assertions, retarget them to the new token classes), tsc + lint clean. View the whole shell on :3000, toggle light/dark.

- [ ] **Step 7: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components/layout/app-layout.tsx web/src/components/layout/app-sidebar.tsx web/src/components/layout/app-topbar.tsx web/src/components/layout/breadcrumbs.tsx web/src/components/layout/theme-toggle.tsx web/src/components/layout/__tests__ web/public/locales/en/common.json web/public/locales/ar/common.json
git commit -m "feat(web): neumorphic app shell + restored theme toggle"
```

---

### Task 7: Loose & crextio primitives

**Files:** Modify `web/src/components/ui/{DataTable,EmptyState,ErrorState,Skeleton,Spinner,ViewModeToggle,FileIcon,CommandPalette,Dialog,form,label}.tsx` and `web/src/components/ui/crextio/*.tsx`.

**Interfaces:** Consumes Tasks 1–5. Same exports/props.

- [ ] **Step 1: Apply recipes**

- **DataTable:** header row on canvas; the table sits in a raised card (`shadow-neu rounded-2xl` wrapper) if it currently uses a bordered container; row hover `hover:bg-muted`; keep sticky-header behavior.
- **EmptyState / ErrorState:** the illustrative container becomes an inset well (`bg-muted shadow-neu-inset rounded-2xl`); the action button inherits neumorphic Button.
- **Skeleton:** shimmer on an inset well (`bg-muted shadow-neu-inset`); keep the pulse animation (respect reduced-motion).
- **Spinner:** unchanged (accent color).
- **ViewModeToggle:** segmented like Tabs — inset track, raised active thumb + accent.
- **FileIcon / label / form / Dialog(legacy) / CommandPalette:** re-skin surfaces to `shadow-neu`/inset per role; CommandPalette panel uses the overlay recipe.
- **crextio/*:** warm-card + the metric widgets become neumorphic raised cards on the canvas; data bars/rings use the accent. Drop the warm cream `bg-*` in favor of `bg-card`.

- [ ] **Step 2: Verify**

Run: `cd web && npx tsc --noEmit && npm test -- --run` → green. View the dashboard (crextio widgets) and a table on :3000.

- [ ] **Step 3: Commit**

```bash
cd /home/sedoc/DOCMS
git add web/src/components/ui/DataTable.tsx web/src/components/ui/EmptyState.tsx web/src/components/ui/ErrorState.tsx web/src/components/ui/Skeleton.tsx web/src/components/ui/Spinner.tsx web/src/components/ui/ViewModeToggle.tsx web/src/components/ui/FileIcon.tsx web/src/components/ui/CommandPalette.tsx web/src/components/ui/Dialog.tsx web/src/components/ui/form.tsx web/src/components/ui/label.tsx web/src/components/ui/crextio
git commit -m "feat(web): neumorphic loose + crextio primitives"
```

---

### Task 8: Bespoke-styling sweep across screens (~59 files)

**Files:** Every `web/src/**/*.tsx` (excluding `ui/` primitives already done) that matches the greps below. Batch by area; commit per area.

**Interfaces:** Consumes Tasks 1–7. UI-only class edits.

- [ ] **Step 1: Enumerate the work**

Run and save the list:
```
cd web
grep -rlE "shadow-(sm|md|lg|xl|2xl|inner)\b|shadow-\[|border border-|bg-card|rounded-2xl|rounded-3xl" src/routes src/components --include='*.tsx' | grep -v '/ui/' | sort > /tmp/neu-sweep.txt
wc -l /tmp/neu-sweep.txt
```

- [ ] **Step 2: Apply the sweep recipe, one area at a time**

For each file, apply consistently (UI-only; never touch handlers/queries):
- Raised container/card → remove `border border-*` + `shadow-{sm,md,lg,xl}`/`shadow-[...]`, add `shadow-neu rounded-2xl bg-card`. If it was a small chip/tile use `shadow-neu-sm`.
- Recessed area (wells, code blocks, empty panels, search bars) → `bg-muted shadow-neu-inset rounded-xl`.
- Any `bg-white`/`bg-slate-*`/`bg-gray-*`/`text-white` hardcoded surface → token equivalents (`bg-card`/`bg-muted`/`text-foreground`).
- Hardcoded accent colors (`-emerald-`, `-indigo-`, `-blue-*` used as brand) → `primary`/token equivalents so light+dark both work.
Areas, in order (commit after each): dashboard/home; workspaces + folder tiles (`components/folders/*`, `BrowserTiles`, `BrowserDetailsPanel`); document viewer chrome (`viewer/*`); search; tasks + workflows; admin hub + admin pages; settings; signatures; ask/clauses/reports; notifications/trash/misc.

- [ ] **Step 3: After each area — verify**

Run: `cd web && npx tsc --noEmit && npm run lint` and view the area on :3000 in light + dark. Then commit that area:
```
git add web/src/<area> && git commit -m "feat(web): neumorphic sweep — <area>"
```

- [ ] **Step 4: Confirm the sweep is exhaustive**

Run the Step-1 grep again; remaining hits must be intentional (e.g. a scrim). Note any deliberate leaves in the commit body.

---

### Task 9: Responsive, RTL & dark pass

**Files:** Any component needing breakpoint/touch fixes found during the audit (UI-only).

- [ ] **Step 1: Audit at breakpoints**

With the dev server, check `/`, `/workspaces`, a document, `/search`, `/admin`, `/tasks` at 360, 768, 1024, 1440 in BOTH themes (devtools device toolbar). Record issues: overflow, sub-44px touch targets, muddy shadows, unreadable pairs, RTL mis-mirroring.

- [ ] **Step 2: Fix**

- Wrap wide tables/tab bars in `overflow-x-auto`; ensure the body never scrolls horizontally.
- Grids: `grid-cols-1 sm:grid-cols-2 lg:grid-cols-3/4` where tiles are fixed-width today.
- Bump touch targets to `min-h-11 min-w-11` (44px) on icon-only controls.
- Confirm `@media (max-width:640px)` shadow scale-down (Task 1) reads well; adjust `--nm-dist/blur` if needed.
- RTL: run `npm run lint` (lint:rtl catches physical utilities); spot-check the rail/drawer side and chevrons with `<html dir="rtl">`.

- [ ] **Step 3: Verify + commit**

Run: `cd web && npx tsc --noEmit && npm run lint && npm test -- --run`
```
git add -A web/src && git commit -m "feat(web): neumorphic responsive + RTL + dark polish"
```

---

### Task 10: Gates, visual capture, spec finalize

**Files:** Modify `web/e2e/70-a11y.spec.ts`; update spec §8.

- [ ] **Step 1: Extend the a11y gate to scan dark**

In `web/e2e/70-a11y.spec.ts`, add a dark-theme variant of the core scans: before `page.goto`, set the stored theme so the app boots dark — `await page.addInitScript(() => localStorage.setItem('vaultdms-theme','dark'))` — and re-run the `workspaces home` + `home`/document scans, asserting the same axe rules. Keep the existing light scans.

- [ ] **Step 2: Run all gates against the built bundle (as CI does)**

Stage Playwright's system libs into the scratchpad per `docs/runbooks/dev-server-quirks.md` / the `web-a11y-gate` memory (apt-get download + dpkg -x + LD_LIBRARY_PATH), then:
```
cd web && npm run build
(npx vite preview --port 4173 --host 127.0.0.1 >/tmp/preview.log 2>&1 &)
export LD_LIBRARY_PATH=<scratchpad>/pwlibs/extracted/usr/lib/x86_64-linux-gnu
CI= npx playwright test e2e/70-a11y.spec.ts e2e/71-keyboard.spec.ts e2e/i18n-rtl-interactions.spec.ts
pkill -f "vite preview --port 4173"
```
Expected: all PASS. Fix any axe `color-contrast`/`non-text-contrast` by adjusting the offending class to a token pair from Task 1; fix focus/keyboard regressions in the owning primitive.

- [ ] **Step 3: Visual capture**

Using the session `shoot.mjs` (logs in via `SEED_*`), capture `/`, `/workspaces`, a document, `/admin`, `/search` at 360/768/1440 in light + dark into `docs/superpowers/screens/neu/`. Eyeball each for muddy shadows / low-contrast text and fix in the owning component.

- [ ] **Step 4: Full local gate + spec finalize**

Run: `cd web && npx tsc --noEmit && npm run lint && npm test -- --run && npm run build` (all clean, known unit failure aside).
Update the spec §8 open questions with the final accent hue and any per-widget data-color decisions. Commit:
```
git add web/e2e/70-a11y.spec.ts docs/superpowers/specs/2026-08-29-neumorphic-ui-design.md docs/superpowers/screens/neu
git commit -m "test(web): a11y gate scans dark; neumorphic redesign finalized"
```
