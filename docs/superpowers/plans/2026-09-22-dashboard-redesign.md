# Dashboard Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the SeDoc dashboard as a charted, animated, three-state overview sourced entirely from data the app already fetches.

**Architecture:** A new `web/src/components/dashboard/` module holds one data hook (a single extra read-only `POST /search` facet call), a shared widget shell that owns loading/empty/error, and small presentational widgets. The route file becomes assembly only. Chart colors are read from live CSS custom properties so light/dark both work.

**Tech Stack:** React 18, TanStack Query v5, TanStack Router, recharts 2.10 (already a dependency), Tailwind 3.4, vitest + @testing-library/react, Playwright + axe.

**Spec:** `docs/superpowers/specs/2026-09-22-dashboard-redesign.md`

## Global Constraints

- **Zero files outside `web/`.** No backend, proto, or service changes. No new endpoints or contract changes.
- **No new npm dependencies.** `recharts@^2.10.4` is already in `web/package.json`.
- **No new `globals.css` color tokens.** New `@keyframes` and the `motion-safe`/`motion-reduce` variants only.
- **No physical-direction Tailwind classes** (`ml-`, `pr-`, `left-`, `text-left`…). `npm run lint` runs `lint:rtl` (`check-no-physical-tw`) and fails on them. Use `ms-`, `pe-`, `start-`, `text-start`.
- **No `text-<color>` on a `bg-<color>/NN` tint** — the tinted-pair contrast rule from the UI phase; `src/styles/__tests__/neu-tokens.test.ts` asserts 17 such pairs.
- **Never render an empty state when a query failed.** Destructure `isError`/`refetch` and branch to `ErrorState` *before* the empty branch.
- **Animate only `transform`, `opacity`, `stroke-dashoffset`.** Easing `cubic-bezier(.4,0,.2,1)`.
- Every task ends green on: `npx tsc --noEmit`, `npm run lint`, `npm test`.
- Existing behaviour that must survive untouched: `PendingSuggestionsCard`, `DashboardUploadDialog` (both triggers, including drag-and-drop onto the Upload quick-action), the `data-testid="header-upload-button"` and `data-testid="quick-action-upload"` hooks.

**Verified facts the implementation depends on** (do not re-derive, do not "fix"):
- `POST /search` accepts `{ query, facets, page_size, filters, search_mode }`. An empty `query` builds `match_all`.
- `page_size: 0` is coerced to the server default — use `page_size: 1`.
- Facet names are exactly `created_at` (date_histogram, **month** buckets), `lifecycle_state`, `doc_type` (terms on `mime_type`), `author` (terms on `created_by_name`).
- `SearchResult.facets` is `Record<string, FacetBucket[]>`; `FacetBucket` is `{ value: string; count: number }`. For `created_at`, `value` is the bucket's ISO date string.
- `Workspace.document_count` exists; **no storage/bytes field exists**.
- Existing helpers to reuse, not rewrite: `formatFileSize`, `lifecycleStateLabel`, `getMimeTypeLabel`, `formatDate` in `src/lib/formatters.ts`; `cn` in `src/lib/cn.ts`; `ErrorState` in `src/components/ui/ErrorState.tsx`; `EmptyState` in `src/components/ui/EmptyState.tsx`; `Skeleton` in `src/components/ui/Skeleton.tsx`; `DirectionalIcon` in `src/components/shared/DirectionalIcon.tsx`.

---

## File Structure

**Create**
| File | Responsibility |
|---|---|
| `src/hooks/usePrefersReducedMotion.ts` | Live `prefers-reduced-motion` boolean |
| `src/hooks/useCountUp.ts` | Animate a numeral 0→value; instant under reduced motion |
| `src/components/dashboard/chartTheme.ts` | Read chart colors from CSS custom properties; re-read on theme flip |
| `src/components/dashboard/metrics.ts` | Pure derivations from facet buckets / task lists (no React) |
| `src/components/dashboard/useDashboardMetrics.ts` | The one extra `POST /search` facet query |
| `src/components/dashboard/WidgetCard.tsx` | Shared widget shell + loading/empty/error states |
| `src/components/dashboard/Sparkline.tsx` | Inline-SVG sparkline with draw animation |
| `src/components/dashboard/KpiTile.tsx` | One KPI tile |
| `src/components/dashboard/KpiStrip.tsx` | The four tiles + their queries |
| `src/components/dashboard/ActivityChart.tsx` | Monthly area chart |
| `src/components/dashboard/LifecycleDonut.tsx` | Donut ≥sm, proportion bar <sm |
| `src/components/dashboard/BreakdownBars.tsx` | Horizontal labelled bars (file types, contributors) |
| `src/components/dashboard/NeedsAttention.tsx` | Overdue/urgent tasks + unread mentions |
| `src/components/dashboard/ChartDataTable.tsx` | Visually-hidden table alternative for a chart |

**Modify**
- `src/routes/_authenticated/index.tsx` — becomes assembly; keeps greeting, upload dialog wiring, `PendingSuggestionsCard`, `QuickActions`.
- `src/styles/globals.css` — append `@keyframes` only (no token changes).

**Test**
- `src/components/dashboard/__tests__/metrics.test.ts`
- `src/components/dashboard/__tests__/widgets.test.tsx`
- `src/hooks/__tests__/useCountUp.test.tsx`

---

### Task 1: Motion primitives

**Files:**
- Create: `web/src/hooks/usePrefersReducedMotion.ts`
- Create: `web/src/hooks/useCountUp.ts`
- Test: `web/src/hooks/__tests__/useCountUp.test.tsx`

**Interfaces:**
- Consumes: nothing.
- Produces: `usePrefersReducedMotion(): boolean`; `useCountUp(value: number, durationMs?: number): number`.

- [ ] **Step 1: Write the failing test**

Create `web/src/hooks/__tests__/useCountUp.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useCountUp } from '../useCountUp'

function mockMatchMedia(reduced: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: reduced,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}

describe('useCountUp', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

  it('returns the final value immediately when reduced motion is requested', () => {
    mockMatchMedia(true)
    const { result } = renderHook(() => useCountUp(120))
    expect(result.current).toBe(120)
  })

  it('starts below the target and lands exactly on it', () => {
    mockMatchMedia(false)
    let now = 0
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      frames.push(cb); return frames.length
    })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())

    const { result } = renderHook(() => useCountUp(100, 600))
    expect(result.current).toBe(0)

    act(() => { now = 300; frames.shift()?.(now) })
    expect(result.current).toBeGreaterThan(0)
    expect(result.current).toBeLessThan(100)

    act(() => { now = 600; frames.shift()?.(now) })
    expect(result.current).toBe(100)
  })

  it('does not animate a zero target', () => {
    mockMatchMedia(false)
    const { result } = renderHook(() => useCountUp(0))
    expect(result.current).toBe(0)
  })
})
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd web && npm test -- src/hooks/__tests__/useCountUp.test.tsx`
Expected: FAIL — `Failed to resolve import "../useCountUp"`.

- [ ] **Step 3: Implement `usePrefersReducedMotion`**

Create `web/src/hooks/usePrefersReducedMotion.ts`:

```ts
import { useEffect, useState } from 'react'

const QUERY = '(prefers-reduced-motion: reduce)'

function read(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia(QUERY).matches
}

/**
 * Live `prefers-reduced-motion` state. The global CSS block in
 * globals.css is the backstop for transitions; JS-driven motion
 * (count-up, stroke-dashoffset draws) has to ask explicitly, which
 * is what this is for.
 */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(read)

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(QUERY)
    const onChange = () => setReduced(mq.matches)
    onChange()
    // Safari < 14 only has the deprecated addListener.
    if (typeof mq.addEventListener === 'function') {
      mq.addEventListener('change', onChange)
      return () => mq.removeEventListener('change', onChange)
    }
    mq.addListener(onChange)
    return () => mq.removeListener(onChange)
  }, [])

  return reduced
}
```

- [ ] **Step 4: Implement `useCountUp`**

Create `web/src/hooks/useCountUp.ts`:

```ts
import { useEffect, useRef, useState } from 'react'
import { usePrefersReducedMotion } from './usePrefersReducedMotion'

// Matches the CSS easing used across the dashboard, cubic-bezier(.4,0,.2,1),
// closely enough for a numeral: fast out, gentle settle.
function easeOut(t: number): number {
  return 1 - Math.pow(1 - t, 3)
}

/**
 * Counts a numeral up to `value` on mount and whenever `value` changes.
 * Returns `value` immediately when the viewer asked for reduced motion,
 * so the final state is always reachable without waiting.
 */
export function useCountUp(value: number, durationMs = 600): number {
  const reduced = usePrefersReducedMotion()
  const [display, setDisplay] = useState(() => (reduced ? value : 0))
  const frameRef = useRef<number | null>(null)

  useEffect(() => {
    if (reduced || value === 0 || !Number.isFinite(value)) {
      setDisplay(value)
      return
    }
    const from = 0
    const start = performance.now()
    const tick = (now: number) => {
      const elapsed = now - start
      if (elapsed >= durationMs) {
        setDisplay(value)
        frameRef.current = null
        return
      }
      setDisplay(Math.round(from + (value - from) * easeOut(elapsed / durationMs)))
      frameRef.current = requestAnimationFrame(tick)
    }
    setDisplay(from)
    frameRef.current = requestAnimationFrame(tick)
    return () => {
      if (frameRef.current !== null) cancelAnimationFrame(frameRef.current)
      frameRef.current = null
    }
  }, [value, durationMs, reduced])

  return display
}
```

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `cd web && npm test -- src/hooks/__tests__/useCountUp.test.tsx`
Expected: PASS, 3 tests.

- [ ] **Step 6: Typecheck and lint**

Run: `cd web && npx tsc --noEmit && npm run lint`
Expected: both clean.

- [ ] **Step 7: Commit**

```bash
git add web/src/hooks/usePrefersReducedMotion.ts web/src/hooks/useCountUp.ts web/src/hooks/__tests__/useCountUp.test.tsx
git commit -m "feat(web): motion primitives for the dashboard (reduced-motion aware count-up)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Metric derivations (pure functions)

**Files:**
- Create: `web/src/components/dashboard/metrics.ts`
- Test: `web/src/components/dashboard/__tests__/metrics.test.ts`

**Interfaces:**
- Consumes: `FacetBucket` from `@/types/api`, `Task` from `@/api/tasks`.
- Produces:
  - `type Point = { label: string; iso: string; value: number }`
  - `type Slice = { key: string; label: string; value: number; share: number }`
  - `monthSeries(buckets: FacetBucket[] | undefined, months?: number): Point[]`
  - `toSlices(buckets: FacetBucket[] | undefined, label: (v: string) => string, top?: number): Slice[]`
  - `taskStats(tasks: Task[] | undefined): { open: number; overdue: number; awaitingApproval: number; oldestWaitingDays: number | null }`
  - `trendSummary(points: Point[]): string`

- [ ] **Step 1: Write the failing test**

Create `web/src/components/dashboard/__tests__/metrics.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { monthSeries, toSlices, taskStats, trendSummary } from '../metrics'
import type { Task } from '@/api/tasks'

const task = (over: Partial<Task>): Task => ({
  id: 'x', title: 't', description: '', status: 'open', priority: 'normal',
  source: 'user', created_by: 'u', created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z', assignees: [], documents: [], ...over,
})

describe('monthSeries', () => {
  it('returns [] for a missing facet rather than throwing', () => {
    expect(monthSeries(undefined)).toEqual([])
  })

  it('sorts chronologically and keeps only the last N months', () => {
    const out = monthSeries([
      { value: '2026-03-01T00:00:00.000Z', count: 3 },
      { value: '2026-01-01T00:00:00.000Z', count: 1 },
      { value: '2026-02-01T00:00:00.000Z', count: 2 },
    ], 2)
    expect(out.map((p) => p.value)).toEqual([2, 3])
  })

  it('labels buckets by short month name', () => {
    const [p] = monthSeries([{ value: '2026-01-15T00:00:00.000Z', count: 7 }])
    expect(p.label).toMatch(/Jan/)
    expect(p.value).toBe(7)
  })

  it('skips buckets whose value is not a parseable date', () => {
    expect(monthSeries([{ value: 'not-a-date', count: 5 }])).toEqual([])
  })
})

describe('toSlices', () => {
  it('computes share against the total, not the top bucket', () => {
    const out = toSlices([
      { value: 'active', count: 30 },
      { value: 'draft', count: 10 },
    ], (v) => v)
    expect(out[0].share).toBeCloseTo(0.75)
    expect(out[1].share).toBeCloseTo(0.25)
  })

  it('returns [] when every bucket is zero (no divide-by-zero)', () => {
    expect(toSlices([{ value: 'a', count: 0 }], (v) => v)).toEqual([])
  })

  it('truncates to the top N by count', () => {
    const out = toSlices([
      { value: 'a', count: 1 }, { value: 'b', count: 9 }, { value: 'c', count: 5 },
    ], (v) => v, 2)
    expect(out.map((s) => s.key)).toEqual(['b', 'c'])
  })

  it('applies the label function', () => {
    const out = toSlices([{ value: 'in_review', count: 2 }], (v) => v.toUpperCase())
    expect(out[0].label).toBe('IN_REVIEW')
    expect(out[0].key).toBe('in_review')
  })
})

describe('taskStats', () => {
  it('is all zeroes for undefined input', () => {
    expect(taskStats(undefined)).toEqual({ open: 0, overdue: 0, awaitingApproval: 0, oldestWaitingDays: null })
  })

  it('counts open and in_progress as open, and excludes done from overdue', () => {
    const past = new Date(Date.now() - 86_400_000).toISOString()
    const s = taskStats([
      task({ status: 'open', due_at: past }),
      task({ status: 'in_progress' }),
      task({ status: 'done', due_at: past }),
      task({ status: 'cancelled', due_at: past }),
    ])
    expect(s.open).toBe(2)
    expect(s.overdue).toBe(1)
  })

  it('counts only open workflow tasks as awaiting approval', () => {
    const s = taskStats([
      task({ source: 'workflow', status: 'open' }),
      task({ source: 'workflow', status: 'done' }),
      task({ source: 'user', status: 'open' }),
    ])
    expect(s.awaitingApproval).toBe(1)
  })

  it('reports the age of the oldest open workflow task in whole days', () => {
    const tenDaysAgo = new Date(Date.now() - 10 * 86_400_000).toISOString()
    const s = taskStats([task({ source: 'workflow', status: 'open', created_at: tenDaysAgo })])
    expect(s.oldestWaitingDays).toBe(10)
  })
})

describe('trendSummary', () => {
  it('describes an empty series without inventing a direction', () => {
    expect(trendSummary([])).toMatch(/no documents/i)
  })

  it('names the direction, both endpoints and the total', () => {
    const s = trendSummary([
      { label: 'Jan', iso: '2026-01-01', value: 12 },
      { label: 'Feb', iso: '2026-02-01', value: 48 },
    ])
    expect(s).toContain('12')
    expect(s).toContain('48')
    expect(s).toMatch(/rising/i)
  })

  it('says falling when the series ends lower', () => {
    const s = trendSummary([
      { label: 'Jan', iso: '2026-01-01', value: 48 },
      { label: 'Feb', iso: '2026-02-01', value: 12 },
    ])
    expect(s).toMatch(/falling/i)
  })
})
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd web && npm test -- src/components/dashboard/__tests__/metrics.test.ts`
Expected: FAIL — cannot resolve `../metrics`.

- [ ] **Step 3: Implement**

Create `web/src/components/dashboard/metrics.ts`:

```ts
import type { FacetBucket } from '@/types/api'
import type { Task } from '@/api/tasks'

export interface Point { label: string; iso: string; value: number }
export interface Slice { key: string; label: string; value: number; share: number }

const MONTH_LABEL = new Intl.DateTimeFormat(undefined, { month: 'short' })

/**
 * Turns the `created_at` date_histogram facet into a chronological series.
 * The backend pins the interval to calendar month
 * (search/internal/opensearch/facets.go), so each bucket is one month and
 * `value` is an ISO timestamp for the start of it.
 */
export function monthSeries(buckets: FacetBucket[] | undefined, months = 12): Point[] {
  if (!buckets?.length) return []
  return buckets
    .map((b) => ({ time: new Date(b.value).getTime(), bucket: b }))
    .filter((r) => Number.isFinite(r.time))
    .sort((a, b) => a.time - b.time)
    .slice(-months)
    .map(({ time, bucket }) => ({
      label: MONTH_LABEL.format(new Date(time)),
      iso: new Date(time).toISOString(),
      value: bucket.count,
    }))
}

/**
 * Terms-facet buckets → labelled slices with their share of the total.
 * Share is computed against the summed total so the slices add to 1 —
 * dividing by the largest bucket (the obvious mistake) would make the
 * donut and the bars disagree with their own percentages.
 */
export function toSlices(
  buckets: FacetBucket[] | undefined,
  label: (value: string) => string,
  top = 6,
): Slice[] {
  if (!buckets?.length) return []
  const positive = buckets.filter((b) => b.count > 0)
  const total = positive.reduce((sum, b) => sum + b.count, 0)
  if (total === 0) return []
  return positive
    .slice()
    .sort((a, b) => b.count - a.count)
    .slice(0, top)
    .map((b) => ({ key: b.value, label: label(b.value), value: b.count, share: b.count / total }))
}

const DAY_MS = 86_400_000
const OPEN_STATUSES: ReadonlySet<Task['status']> = new Set(['open', 'in_progress'])

export function taskStats(tasks: Task[] | undefined): {
  open: number
  overdue: number
  awaitingApproval: number
  oldestWaitingDays: number | null
} {
  if (!tasks?.length) return { open: 0, overdue: 0, awaitingApproval: 0, oldestWaitingDays: null }
  const now = Date.now()
  const live = tasks.filter((t) => OPEN_STATUSES.has(t.status))
  const workflow = live.filter((t) => t.source === 'workflow')
  const oldest = workflow.reduce<number | null>((acc, t) => {
    const created = new Date(t.created_at).getTime()
    if (!Number.isFinite(created)) return acc
    return acc === null || created < acc ? created : acc
  }, null)
  return {
    open: live.length,
    overdue: live.filter((t) => t.due_at && new Date(t.due_at).getTime() < now).length,
    awaitingApproval: workflow.length,
    oldestWaitingDays: oldest === null ? null : Math.floor((now - oldest) / DAY_MS),
  }
}

/** The text alternative announced for the activity chart (WCAG 1.1.1). */
export function trendSummary(points: Point[]): string {
  if (!points.length) return 'No documents added in the last 12 months.'
  const first = points[0]
  const last = points[points.length - 1]
  const total = points.reduce((sum, p) => sum + p.value, 0)
  if (points.length === 1) {
    return `${first.value} documents added in ${first.label}.`
  }
  const direction = last.value > first.value ? 'rising' : last.value < first.value ? 'falling' : 'flat'
  return `Documents added per month, ${direction} from ${first.value} in ${first.label} to ${last.value} in ${last.label}. ${total} in total.`
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd web && npm test -- src/components/dashboard/__tests__/metrics.test.ts`
Expected: PASS, 15 tests.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/dashboard/metrics.ts web/src/components/dashboard/__tests__/metrics.test.ts
git commit -m "feat(web): pure metric derivations for the dashboard facets

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Chart theme + the facet query

**Files:**
- Create: `web/src/components/dashboard/chartTheme.ts`
- Create: `web/src/components/dashboard/useDashboardMetrics.ts`

**Interfaces:**
- Consumes: `search` from `@/api/search`; `Point`/`Slice` helpers from Task 2.
- Produces:
  - `useChartTheme(): { grid: string; axis: string; series: string[]; area: string }`
  - `useDashboardMetrics(): { activity: Point[]; lifecycle: Slice[]; fileTypes: Slice[]; contributors: Slice[]; isLoading: boolean; isError: boolean; refetch: () => void }`
  - `dashboardMetricsKey: readonly string[]`

- [ ] **Step 1: Implement the chart theme**

Recharts takes colors as props, so they cannot be Tailwind classes. Read them from the live custom properties and rebuild on theme flip. Create `web/src/components/dashboard/chartTheme.ts`:

```ts
import { useEffect, useState } from 'react'

export interface ChartTheme {
  grid: string
  axis: string
  /** Ordered categorical ramp, derived from the single accent. */
  series: string[]
  area: string
}

// `--primary` etc. are stored as bare HSL channels ("223 75% 47%") so
// Tailwind can compose them with an alpha. Recharts needs a real color
// string, so wrap them back up here.
function channel(styles: CSSStyleDeclaration, name: string, fallback: string): string {
  const raw = styles.getPropertyValue(name).trim()
  return raw ? `hsl(${raw})` : fallback
}

function read(): ChartTheme {
  if (typeof window === 'undefined') {
    return { grid: '#c4c9d4', axis: '#465063', series: ['#1e50d2'], area: '#1e50d2' }
  }
  const s = getComputedStyle(document.documentElement)
  const primary = s.getPropertyValue('--primary').trim() || '223 75% 47%'
  const [h, sat] = primary.split(/\s+/)
  // One accent, stepped in lightness — the single-accent rule from the
  // neumorphic phase. Lightness stays inside a band that keeps every
  // step distinguishable against both canvases.
  const ramp = [30, 42, 54, 66, 78].map((l) => `hsl(${h} ${sat} ${l}%)`)
  return {
    grid: channel(s, '--border', '#c4c9d4'),
    axis: channel(s, '--muted-foreground', '#465063'),
    series: [channel(s, '--primary', '#1e50d2'), ...ramp],
    area: channel(s, '--primary', '#1e50d2'),
  }
}

/**
 * Chart colors that follow the active theme. ComplianceDashboard.tsx
 * hardcodes hex values and therefore does not theme; this is the
 * replacement pattern for anything new.
 */
export function useChartTheme(): ChartTheme {
  const [theme, setTheme] = useState<ChartTheme>(read)

  useEffect(() => {
    setTheme(read())
    const target = document.documentElement
    const observer = new MutationObserver(() => setTheme(read()))
    observer.observe(target, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])

  return theme
}
```

- [ ] **Step 2: Implement the query hook**

Create `web/src/components/dashboard/useDashboardMetrics.ts`:

```ts
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'

import { search } from '@/api/search'
import { getMimeTypeLabel, lifecycleStateLabel } from '@/lib/formatters'
import { monthSeries, toSlices, type Point, type Slice } from './metrics'

export const dashboardMetricsKey = ['dashboard', 'metrics'] as const

// One read-only call to the existing POST /search feeds four widgets.
// An empty query builds match_all server-side
// (search/internal/opensearch/query.go). `page_size` cannot be 0 — the
// service coerces <=0 to its default page size — so ask for the
// smallest legal page and ignore `results`.
const FACETS = ['created_at', 'lifecycle_state', 'doc_type', 'author'] as const

export interface DashboardMetrics {
  activity: Point[]
  lifecycle: Slice[]
  fileTypes: Slice[]
  contributors: Slice[]
  isLoading: boolean
  isError: boolean
  refetch: () => void
}

export function useDashboardMetrics(): DashboardMetrics {
  const query = useQuery({
    queryKey: dashboardMetricsKey,
    queryFn: () => search({ query: '', facets: [...FACETS], page_size: 1, search_mode: 'lexical' }),
    staleTime: 120_000,
  })

  const facets = query.data?.facets
  const derived = useMemo(() => ({
    activity: monthSeries(facets?.created_at),
    lifecycle: toSlices(facets?.lifecycle_state, lifecycleStateLabel, 8),
    fileTypes: toSlices(facets?.doc_type, getMimeTypeLabel, 5),
    contributors: toSlices(facets?.author, (v) => v, 5),
  }), [facets])

  return {
    ...derived,
    isLoading: query.isLoading,
    isError: query.isError,
    refetch: () => { void query.refetch() },
  }
}
```

- [ ] **Step 3: Typecheck**

Run: `cd web && npx tsc --noEmit`
Expected: clean. If `lifecycleStateLabel`/`getMimeTypeLabel` have a different signature than `(s: string) => string`, adapt the call site with an arrow — do **not** change the shared helpers.

- [ ] **Step 4: Lint**

Run: `cd web && npm run lint`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/dashboard/chartTheme.ts web/src/components/dashboard/useDashboardMetrics.ts
git commit -m "feat(web): themed chart colors + one facet query behind four dashboard widgets

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Widget shell and its three states

**Files:**
- Create: `web/src/components/dashboard/WidgetCard.tsx`
- Create: `web/src/components/dashboard/ChartDataTable.tsx`
- Modify: `web/src/styles/globals.css` (append keyframes only)
- Test: `web/src/components/dashboard/__tests__/widgets.test.tsx`

**Interfaces:**
- Consumes: `ErrorState`, `Skeleton`, `cn`.
- Produces:
  - `WidgetCard(props: { title: string; subtitle?: string; action?: ReactNode; isLoading?: boolean; isError?: boolean; isEmpty?: boolean; emptyLabel?: string; emptyAction?: ReactNode; onRetry?: () => void; delayIndex?: number; className?: string; bodyClassName?: string; children: ReactNode })`
  - `ChartDataTable(props: { caption: string; columns: [string, string]; rows: { key: string; label: string; value: string | number }[] })`

- [ ] **Step 1: Write the failing test**

Create `web/src/components/dashboard/__tests__/widgets.test.tsx`:

```tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { WidgetCard } from '../WidgetCard'
import { ChartDataTable } from '../ChartDataTable'

describe('WidgetCard', () => {
  it('shows the failure state with Retry, and never the empty copy, when a query failed', async () => {
    const onRetry = vi.fn()
    render(
      <WidgetCard title="Lifecycle" isError isEmpty emptyLabel="Nothing here yet" onRetry={onRetry}>
        <p>rows</p>
      </WidgetCard>,
    )
    expect(screen.queryByText('Nothing here yet')).not.toBeInTheDocument()
    expect(screen.queryByText('rows')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('prefers the failure state over the loading state', () => {
    render(<WidgetCard title="Lifecycle" isLoading isError onRetry={vi.fn()}><p>rows</p></WidgetCard>)
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('renders children only when the query succeeded and is non-empty', () => {
    render(<WidgetCard title="Lifecycle"><p>rows</p></WidgetCard>)
    expect(screen.getByText('rows')).toBeInTheDocument()
  })

  it('labels its region with the title so the page has a navigable outline', () => {
    render(<WidgetCard title="File types"><p>rows</p></WidgetCard>)
    expect(screen.getByRole('region', { name: 'File types' })).toBeInTheDocument()
  })
})

describe('ChartDataTable', () => {
  it('exposes the chart numbers to assistive tech', () => {
    render(
      <ChartDataTable
        caption="Documents added per month"
        columns={['Month', 'Documents']}
        rows={[{ key: 'jan', label: 'Jan', value: 12 }]}
      />,
    )
    const table = screen.getByRole('table', { name: 'Documents added per month' })
    expect(table).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: 'Jan' })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '12' })).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: FAIL — cannot resolve `../WidgetCard`.

- [ ] **Step 3: Append the keyframes**

Append to the end of `web/src/styles/globals.css` (do not touch any token):

```css
/* Dashboard entrance motion (spec 2026-09-22). Only transform/opacity
 * and stroke-dashoffset animate, so every one of these stays on the
 * compositor. The global prefers-reduced-motion block above collapses
 * them to ~0ms; components that need the FINAL state (not a collapsed
 * animation) also ask usePrefersReducedMotion directly. */
@keyframes dash-rise {
  from { opacity: 0; transform: translateY(14px); }
  to   { opacity: 1; transform: none; }
}

@keyframes dash-draw {
  from { stroke-dashoffset: var(--dash-len, 1000); }
  to   { stroke-dashoffset: 0; }
}

@keyframes dash-grow {
  from { transform: scaleX(0); }
  to   { transform: scaleX(1); }
}

.dash-rise {
  animation: dash-rise 500ms cubic-bezier(0.4, 0, 0.2, 1) both;
  animation-delay: var(--dash-delay, 0ms);
}

.dash-draw {
  stroke-dasharray: var(--dash-len, 1000);
  animation: dash-draw 1200ms cubic-bezier(0.4, 0, 0.2, 1) both;
}

.dash-grow {
  transform-origin: left center;
  animation: dash-grow 700ms cubic-bezier(0.4, 0, 0.2, 1) both;
  animation-delay: var(--dash-delay, 0ms);
}

/* RTL: grow from the inline start in both directions. */
[dir='rtl'] .dash-grow { transform-origin: right center; }

@media (prefers-reduced-motion: reduce) {
  .dash-rise, .dash-draw, .dash-grow { animation: none; }
  .dash-draw { stroke-dasharray: none; stroke-dashoffset: 0; }
  .dash-grow { transform: none; }
}
```

- [ ] **Step 4: Implement `ChartDataTable`**

Create `web/src/components/dashboard/ChartDataTable.tsx`:

```tsx
/**
 * The text alternative for a chart. A chart announced only as one
 * `role="img"` label gives a screen-reader user the shape but not the
 * numbers; this puts the numbers in the accessibility tree without
 * showing a second copy on screen.
 */
export function ChartDataTable({
  caption,
  columns,
  rows,
}: {
  caption: string
  columns: [string, string]
  rows: { key: string; label: string; value: string | number }[]
}) {
  if (!rows.length) return null
  return (
    <table className="sr-only">
      <caption>{caption}</caption>
      <thead>
        <tr>
          <th scope="col">{columns[0]}</th>
          <th scope="col">{columns[1]}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key}>
            <td>{r.label}</td>
            <td>{r.value}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
```

- [ ] **Step 5: Implement `WidgetCard`**

Create `web/src/components/dashboard/WidgetCard.tsx`:

```tsx
import type { ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { cn } from '@/lib/cn'

interface WidgetCardProps {
  title: string
  subtitle?: string
  action?: ReactNode
  isLoading?: boolean
  isError?: boolean
  isEmpty?: boolean
  emptyLabel?: string
  emptyAction?: ReactNode
  onRetry?: () => void
  /** Stagger index for the entrance animation. */
  delayIndex?: number
  className?: string
  bodyClassName?: string
  children: ReactNode
}

/**
 * The shared dashboard widget shell. It owns the state precedence so no
 * individual widget can get it wrong: FAILED beats LOADING beats EMPTY
 * beats content. The UX audit found six screens rendering the
 * success-empty state on a query failure ("You're all caught up" when
 * the fetch actually died) — centralising the branch here is what stops
 * that recurring.
 */
export function WidgetCard({
  title, subtitle, action,
  isLoading, isError, isEmpty, emptyLabel, emptyAction, onRetry,
  delayIndex = 0, className, bodyClassName, children,
}: WidgetCardProps) {
  return (
    <section
      aria-label={title}
      className={cn(
        'dash-rise flex min-w-0 flex-col rounded-[--radius] bg-card p-5 shadow-neu',
        'transition-shadow duration-200 hover:shadow-neu-lg',
        className,
      )}
      style={{ '--dash-delay': `${delayIndex * 60}ms` } as React.CSSProperties}
    >
      <header className="mb-4 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold text-foreground">{title}</h2>
          {subtitle && <p className="mt-0.5 truncate text-xs text-muted-foreground">{subtitle}</p>}
        </div>
        {action && !isError && <div className="shrink-0">{action}</div>}
      </header>

      <div className={cn('min-w-0 flex-1', bodyClassName)}>
        {isError ? (
          <div className="flex flex-col items-center justify-center gap-3 rounded-2xl bg-muted px-4 py-10 text-center shadow-neu-inset">
            <AlertTriangle className="h-8 w-8 text-destructive" aria-hidden />
            <p className="text-sm text-muted-foreground">Couldn&apos;t load this.</p>
            {onRetry && (
              <Button variant="outline" size="sm" onClick={onRetry}>Retry</Button>
            )}
          </div>
        ) : isLoading ? (
          // Geometry-matched so the loaded card occupies the same box:
          // CLS stays at 0 on a slow connection.
          <div className="flex flex-col gap-3" aria-hidden>
            <Skeleton className="h-4 w-2/5 rounded-md" />
            <Skeleton className="h-28 w-full rounded-xl" />
            <Skeleton className="h-4 w-3/5 rounded-md" />
          </div>
        ) : isEmpty ? (
          <div className="flex flex-col items-center justify-center gap-2 rounded-2xl bg-muted px-4 py-10 text-center shadow-neu-inset">
            <p className="text-sm font-medium text-foreground">{emptyLabel ?? 'Nothing here yet'}</p>
            {emptyAction}
          </div>
        ) : (
          children
        )}
      </div>
    </section>
  )
}
```

- [ ] **Step 6: Run the tests and make sure they pass**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: PASS, 5 tests.

- [ ] **Step 7: Typecheck, lint**

Run: `cd web && npx tsc --noEmit && npm run lint`
Expected: clean. `lint:rtl` will reject `transform-origin: left` written as a Tailwind class — it is plain CSS in `globals.css` with an `[dir='rtl']` override, which is allowed.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/dashboard/WidgetCard.tsx web/src/components/dashboard/ChartDataTable.tsx web/src/components/dashboard/__tests__/widgets.test.tsx web/src/styles/globals.css
git commit -m "feat(web): dashboard widget shell with honest three-state precedence

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: KPI strip

**Files:**
- Create: `web/src/components/dashboard/Sparkline.tsx`
- Create: `web/src/components/dashboard/KpiTile.tsx`
- Create: `web/src/components/dashboard/KpiStrip.tsx`
- Test: add to `web/src/components/dashboard/__tests__/widgets.test.tsx`

**Interfaces:**
- Consumes: `useCountUp` (Task 1), `taskStats`/`Point` (Task 2), `useDashboardMetrics` (Task 3).
- Produces:
  - `Sparkline(props: { points: Point[]; className?: string; ariaHidden?: boolean })`
  - `KpiTile(props: { icon: LucideIcon; label: string; value: number | undefined; hint?: string; hintTone?: 'muted' | 'alert'; href: string; sparkline?: Point[]; delayIndex?: number })`
  - `KpiStrip()`

- [ ] **Step 1: Write the failing tests**

Append to `web/src/components/dashboard/__tests__/widgets.test.tsx`:

```tsx
import { Sparkline } from '../Sparkline'
import { FolderOpen } from 'lucide-react'
import { KpiTile } from '../KpiTile'

describe('Sparkline', () => {
  it('renders nothing for fewer than two points — a one-point line is not a trend', () => {
    const { container } = render(<Sparkline points={[{ label: 'Jan', iso: '2026-01-01', value: 3 }]} />)
    expect(container.querySelector('svg')).toBeNull()
  })

  it('draws a polyline for a real series', () => {
    const { container } = render(
      <Sparkline points={[
        { label: 'Jan', iso: '2026-01-01', value: 3 },
        { label: 'Feb', iso: '2026-02-01', value: 9 },
      ]} />,
    )
    expect(container.querySelector('polyline')).not.toBeNull()
  })

  it('survives a flat series without producing NaN coordinates', () => {
    const { container } = render(
      <Sparkline points={[
        { label: 'Jan', iso: '2026-01-01', value: 5 },
        { label: 'Feb', iso: '2026-02-01', value: 5 },
      ]} />,
    )
    expect(container.querySelector('polyline')?.getAttribute('points')).not.toMatch(/NaN/)
  })
})

describe('KpiTile', () => {
  it('shows a skeleton instead of a zero while the value is still undefined', () => {
    const { container } = render(
      <KpiTile icon={FolderOpen} label="Documents" value={undefined} href="/workspaces" />,
    )
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(container.querySelector('[data-testid="kpi-skeleton"]')).not.toBeNull()
  })

  it('names the tile for assistive tech as label plus value', () => {
    render(<KpiTile icon={FolderOpen} label="Documents" value={42} href="/workspaces" />)
    expect(screen.getByRole('link', { name: /Documents: 42/ })).toBeInTheDocument()
  })
})
```

Add this mock at the top of the file, beside the existing imports, so `Link` renders as an anchor in tests:

```tsx
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
  useNavigate: () => vi.fn(),
}))
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: FAIL — cannot resolve `../Sparkline`.

- [ ] **Step 3: Implement `Sparkline`**

Create `web/src/components/dashboard/Sparkline.tsx`:

```tsx
import { useId } from 'react'

import { cn } from '@/lib/cn'
import type { Point } from './metrics'

const W = 120
const H = 34

/**
 * A 120x34 inline-SVG trend line. Inline rather than recharts because a
 * sparkline has no axes, no tooltip and no legend — mounting a chart
 * library four times in the KPI strip would cost far more than it earns.
 *
 * Decorative by default: the tile's aria-label already carries the
 * figure, and the full series is available in the activity chart's data
 * table, so announcing it again here would be noise.
 */
export function Sparkline({ points, className }: { points: Point[]; className?: string }) {
  const gradientId = useId()
  if (points.length < 2) return null

  const values = points.map((p) => p.value)
  const min = Math.min(...values)
  const max = Math.max(...values)
  // A flat series has zero range; dividing by it yields NaN coordinates
  // and an invisible line, so pin it to the vertical middle instead.
  const range = max - min || 1
  const step = W / (points.length - 1)

  const coords = points.map((p, i) => {
    const x = i * step
    const y = max === min ? H / 2 : H - ((p.value - min) / range) * (H - 4) - 2
    return `${x.toFixed(2)},${y.toFixed(2)}`
  })

  const line = coords.join(' ')
  const area = `0,${H} ${line} ${W},${H}`

  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      className={cn('h-8 w-full', className)}
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="currentColor" stopOpacity="0.25" />
          <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <polygon points={area} fill={`url(#${gradientId})`} />
      <polyline
        points={line}
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="dash-draw"
        style={{ '--dash-len': 400 } as React.CSSProperties}
      />
    </svg>
  )
}
```

- [ ] **Step 4: Implement `KpiTile`**

Create `web/src/components/dashboard/KpiTile.tsx`:

```tsx
import { Link } from '@tanstack/react-router'
import type { LucideIcon } from 'lucide-react'

import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { Skeleton } from '@/components/ui/Skeleton'
import { useCountUp } from '@/hooks/useCountUp'
import { cn } from '@/lib/cn'
import { Sparkline } from './Sparkline'
import type { Point } from './metrics'

interface KpiTileProps {
  icon: LucideIcon
  label: string
  /** `undefined` means "still loading" — it must not render as 0. */
  value: number | undefined
  hint?: string
  hintTone?: 'muted' | 'alert'
  href: string
  /** Only supplied where a real series exists; no decorative fakes. */
  sparkline?: Point[]
  delayIndex?: number
}

export function KpiTile({
  icon: Icon, label, value, hint, hintTone = 'muted', href, sparkline, delayIndex = 0,
}: KpiTileProps) {
  const shown = useCountUp(value ?? 0)

  return (
    <Link
      to={href}
      aria-label={value === undefined ? label : `${label}: ${value}`}
      className={cn(
        'dash-rise group flex min-w-0 flex-col gap-3 rounded-[--radius] bg-card p-5 text-start shadow-neu',
        'transition-[box-shadow,transform] duration-200',
        'hover:-translate-y-0.5 hover:shadow-neu-lg active:translate-y-0 active:shadow-neu-pressed',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
      )}
      style={{ '--dash-delay': `${delayIndex * 60}ms` } as React.CSSProperties}
    >
      <div className="flex items-center justify-between">
        <span
          className="flex h-9 w-9 items-center justify-center rounded-xl bg-muted text-foreground shadow-neu-inset"
          aria-hidden
        >
          <Icon className="h-[1.05rem] w-[1.05rem]" />
        </span>
        <DirectionalIcon
          name="ChevronRight"
          className="h-4 w-4 text-muted-foreground transition-transform group-hover:translate-x-0.5"
        />
      </div>

      {value === undefined ? (
        <div className="flex min-h-[64px] flex-col gap-2" data-testid="kpi-skeleton">
          <Skeleton className="h-9 w-20 rounded-md" />
          <Skeleton className="h-3 w-24 rounded-md" />
        </div>
      ) : (
        <div className="min-h-[64px]">
          <span className="block text-[34px] font-semibold leading-none tracking-[-0.025em] tabular-nums text-foreground">
            {shown}
          </span>
          <span className="mt-1.5 block text-[13px] font-medium text-muted-foreground">{label}</span>
        </div>
      )}

      {/* The hint is real data or nothing — never a placeholder trend. */}
      {hint && (
        <p className={cn('text-xs', hintTone === 'alert' ? 'text-destructive' : 'text-muted-foreground')}>
          {hint}
        </p>
      )}

      {sparkline && sparkline.length > 1 && (
        <Sparkline points={sparkline} className="text-primary" />
      )}
    </Link>
  )
}
```

- [ ] **Step 5: Implement `KpiStrip`**

Create `web/src/components/dashboard/KpiStrip.tsx`:

```tsx
import { useQuery } from '@tanstack/react-query'
import { Bell, CheckSquare, FileText, Stamp } from 'lucide-react'

import { getUnreadCount } from '@/api/notifications'
import { listMyTasks, taskKeys } from '@/api/tasks'
import { getWorkspaces } from '@/api/workspaces'
import { KpiTile } from './KpiTile'
import { taskStats } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

/**
 * The four headline figures. Each is sourced from a query the dashboard
 * already ran before this redesign, except the document total, which
 * sums Workspace.document_count — Postgres-authoritative, unlike the
 * search index, which lags ingestion.
 *
 * There is deliberately no "storage used" tile: no tenant-wide byte
 * total is readable by a non-admin (storage_by_region lives behind
 * /admin/compliance/overview), and a fabricated one is worse than none.
 */
export function KpiStrip() {
  const workspaces = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces, staleTime: 60_000 })
  const tasks = useQuery({ queryKey: taskKeys.mine(), queryFn: () => listMyTasks(false), staleTime: 30_000 })
  const unread = useQuery({
    queryKey: ['notifications', 'unread-count'], queryFn: getUnreadCount, staleTime: 30_000,
  })
  const metrics = useDashboardMetrics()

  const docTotal = workspaces.data?.reduce((sum, w) => sum + (w.document_count ?? 0), 0)
  const stats = taskStats(tasks.data)

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <KpiTile
        icon={FileText}
        label="Documents"
        value={workspaces.isLoading ? undefined : docTotal ?? 0}
        hint={workspaces.isError ? 'unable to load' : `across ${workspaces.data?.length ?? 0} workspaces`}
        href="/workspaces"
        // The only KPI with a genuine historical series behind it.
        sparkline={metrics.activity}
        delayIndex={0}
      />
      <KpiTile
        icon={CheckSquare}
        label="Open tasks"
        value={tasks.isLoading ? undefined : stats.open}
        hint={
          tasks.isError ? 'unable to load'
            : stats.overdue > 0 ? `${stats.overdue} overdue`
            : 'assigned to you'
        }
        hintTone={stats.overdue > 0 ? 'alert' : 'muted'}
        href="/tasks"
        delayIndex={1}
      />
      <KpiTile
        icon={Stamp}
        label="Awaiting approval"
        value={tasks.isLoading ? undefined : stats.awaitingApproval}
        hint={
          tasks.isError ? 'unable to load'
            : stats.oldestWaitingDays !== null
              ? `oldest waiting ${stats.oldestWaitingDays}d`
              : 'nothing pending'
        }
        href="/tasks"
        delayIndex={2}
      />
      <KpiTile
        icon={Bell}
        label="Unread"
        value={unread.isLoading ? undefined : unread.data ?? 0}
        hint={unread.isError ? 'unable to load' : 'notifications'}
        href="/notifications"
        delayIndex={3}
      />
    </div>
  )
}
```

- [ ] **Step 6: Run the tests and make sure they pass**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: PASS, 10 tests.

- [ ] **Step 7: Typecheck, lint**

Run: `cd web && npx tsc --noEmit && npm run lint`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/dashboard/Sparkline.tsx web/src/components/dashboard/KpiTile.tsx web/src/components/dashboard/KpiStrip.tsx web/src/components/dashboard/__tests__/widgets.test.tsx
git commit -m "feat(web): dashboard KPI strip with a real trend sparkline

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Activity chart

**Files:**
- Create: `web/src/components/dashboard/ActivityChart.tsx`
- Test: add to `web/src/components/dashboard/__tests__/widgets.test.tsx`

**Interfaces:**
- Consumes: `WidgetCard`, `ChartDataTable`, `useChartTheme`, `useDashboardMetrics`, `trendSummary`.
- Produces: `ActivityChart(props: { delayIndex?: number })`

- [ ] **Step 1: Write the failing test**

Append to `web/src/components/dashboard/__tests__/widgets.test.tsx`:

```tsx
import { ActivityChart } from '../ActivityChart'
import * as metricsHook from '../useDashboardMetrics'

const emptyMetrics = {
  activity: [], lifecycle: [], fileTypes: [], contributors: [],
  isLoading: false, isError: false, refetch: vi.fn(),
}

describe('ActivityChart', () => {
  it('renders the failure state with Retry, not the empty state', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isError: true })
    render(<ActivityChart />)
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText(/no documents added yet/i)).not.toBeInTheDocument()
    vi.restoreAllMocks()
  })

  it('exposes the series as an accessible table so the numbers are not trapped in an image', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      activity: [
        { label: 'Jan', iso: '2026-01-01T00:00:00.000Z', value: 12 },
        { label: 'Feb', iso: '2026-02-01T00:00:00.000Z', value: 48 },
      ],
    })
    render(<ActivityChart />)
    expect(screen.getByRole('table', { name: /documents added per month/i })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '48' })).toBeInTheDocument()
    vi.restoreAllMocks()
  })
})
```

Recharts measures its container, which is 0×0 in jsdom, so the SVG itself will not render in tests. That is expected and is exactly why the data table is the assertion target.

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: FAIL — cannot resolve `../ActivityChart`.

- [ ] **Step 3: Implement**

Create `web/src/components/dashboard/ActivityChart.tsx`:

```tsx
import { useId } from 'react'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { ChartDataTable } from './ChartDataTable'
import { WidgetCard } from './WidgetCard'
import { useChartTheme } from './chartTheme'
import { trendSummary } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

/**
 * Documents added per month. The interval is months, not days, because
 * the `created_at` facet is a date_histogram pinned to calendar month
 * server-side (search/internal/opensearch/facets.go). A daily series
 * would need a backend change, which this phase does not make — so the
 * axis says what the data actually is.
 */
export function ActivityChart({ delayIndex = 0 }: { delayIndex?: number }) {
  const { activity, isLoading, isError, refetch } = useDashboardMetrics()
  const theme = useChartTheme()
  const reduced = usePrefersReducedMotion()
  const gradientId = useId()

  return (
    <WidgetCard
      title="Documents added"
      subtitle="Last 12 months, of indexed documents"
      isLoading={isLoading}
      isError={isError}
      isEmpty={activity.length === 0}
      emptyLabel="No documents added yet"
      onRetry={refetch}
      delayIndex={delayIndex}
    >
      <div className="h-[220px] w-full" role="img" aria-label={trendSummary(activity)}>
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={activity} margin={{ top: 4, right: 4, bottom: 0, left: -20 }}>
            <defs>
              <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={theme.area} stopOpacity={0.35} />
                <stop offset="100%" stopColor={theme.area} stopOpacity={0.02} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke={theme.grid} strokeDasharray="3 3" vertical={false} />
            <XAxis
              dataKey="label"
              tick={{ fontSize: 11, fill: theme.axis }}
              tickLine={false}
              axisLine={false}
􀀀            />
            <YAxis
              tick={{ fontSize: 11, fill: theme.axis }}
              tickLine={false}
              axisLine={false}
              allowDecimals={false}
              width={44}
            />
            <Tooltip
              cursor={{ stroke: theme.grid }}
              contentStyle={{
                background: 'hsl(var(--popover))',
                border: '1px solid hsl(var(--border))',
                borderRadius: '12px',
                color: 'hsl(var(--popover-foreground))',
                fontSize: '12px',
              }}
              labelStyle={{ color: 'hsl(var(--muted-foreground))' }}
              formatter={(value: number) => [value, 'Documents']}
            />
            <Area
              type="monotone"
              dataKey="value"
              stroke={theme.area}
              strokeWidth={2}
              fill={`url(#${gradientId})`}
              isAnimationActive={!reduced}
              animationDuration={1200}
              animationEasing="ease-out"
              dot={false}
              activeDot={{ r: 4, strokeWidth: 0 }}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>

      <ChartDataTable
        caption="Documents added per month"
        columns={['Month', 'Documents']}
        rows={activity.map((p) => ({ key: p.iso, label: p.label, value: p.value }))}
      />
    </WidgetCard>
  )
}
```

**Note for the implementer:** the `􀀀` character on the `XAxis` closing line above is a transcription artifact — write a plain `/>` there. Verify the file contains no non-ASCII characters before committing; `npm run lint` runs `lint:utf8` and will fail on stray ones.

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: PASS, 12 tests.

- [ ] **Step 5: Typecheck, lint**

Run: `cd web && npx tsc --noEmit && npm run lint`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/dashboard/ActivityChart.tsx web/src/components/dashboard/__tests__/widgets.test.tsx
git commit -m "feat(web): monthly document-activity chart with a table alternative

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Lifecycle donut, breakdown bars, needs-attention

**Files:**
- Create: `web/src/components/dashboard/LifecycleDonut.tsx`
- Create: `web/src/components/dashboard/BreakdownBars.tsx`
- Create: `web/src/components/dashboard/NeedsAttention.tsx`
- Test: add to `web/src/components/dashboard/__tests__/widgets.test.tsx`

**Interfaces:**
- Consumes: `WidgetCard`, `ChartDataTable`, `useChartTheme`, `useDashboardMetrics`, `Slice`, `taskStats`.
- Produces:
  - `LifecycleDonut(props: { delayIndex?: number })`
  - `BreakdownBars(props: { title: string; subtitle?: string; slices: Slice[]; isLoading: boolean; isError: boolean; onRetry: () => void; emptyLabel: string; unit: string; delayIndex?: number })`
  - `NeedsAttention(props: { delayIndex?: number })`

- [ ] **Step 1: Write the failing tests**

Append to `web/src/components/dashboard/__tests__/widgets.test.tsx`:

```tsx
import { BreakdownBars } from '../BreakdownBars'
import { LifecycleDonut } from '../LifecycleDonut'

describe('BreakdownBars', () => {
  const slices = [
    { key: 'pdf', label: 'PDF', value: 30, share: 0.75 },
    { key: 'docx', label: 'Word', value: 10, share: 0.25 },
  ]

  it('prints the figure beside every bar so colour is never the only encoding', () => {
    render(
      <BreakdownBars title="File types" slices={slices} isLoading={false} isError={false}
        onRetry={vi.fn()} emptyLabel="No files yet" unit="documents" />,
    )
    expect(screen.getByText('30')).toBeInTheDocument()
    expect(screen.getByText('10')).toBeInTheDocument()
    expect(screen.getByText('PDF')).toBeInTheDocument()
  })

  it('shows Retry and hides the rows when the query failed', () => {
    render(
      <BreakdownBars title="File types" slices={[]} isLoading={false} isError
        onRetry={vi.fn()} emptyLabel="No files yet" unit="documents" />,
    )
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText('No files yet')).not.toBeInTheDocument()
  })
})

describe('LifecycleDonut', () => {
  it('lists every state with its count in the accessible table', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      lifecycle: [
        { key: 'active', label: 'Active', value: 30, share: 0.75 },
        { key: 'draft', label: 'Draft', value: 10, share: 0.25 },
      ],
    })
    render(<LifecycleDonut />)
    expect(screen.getByRole('table', { name: /documents by lifecycle state/i })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: 'Active' })).toBeInTheDocument()
    vi.restoreAllMocks()
  })
})
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: FAIL — cannot resolve `../BreakdownBars`.

- [ ] **Step 3: Implement `BreakdownBars`**

Create `web/src/components/dashboard/BreakdownBars.tsx`:

```tsx
import { WidgetCard } from './WidgetCard'
import type { Slice } from './metrics'

/**
 * Horizontal proportion bars. Plain divs rather than a chart library:
 * the bars carry their own label and figure, so recharts would add a
 * measurement pass and a render tree for something a flex row already
 * does — and the scaleX transform keeps the growth animation on the
 * compositor.
 */
export function BreakdownBars({
  title, subtitle, slices, isLoading, isError, onRetry, emptyLabel, unit, delayIndex = 0,
}: {
  title: string
  subtitle?: string
  slices: Slice[]
  isLoading: boolean
  isError: boolean
  onRetry: () => void
  emptyLabel: string
  unit: string
  delayIndex?: number
}) {
  return (
    <WidgetCard
      title={title}
      subtitle={subtitle}
      isLoading={isLoading}
      isError={isError}
      isEmpty={slices.length === 0}
      emptyLabel={emptyLabel}
      onRetry={onRetry}
      delayIndex={delayIndex}
    >
      <ul className="flex flex-col gap-3.5">
        {slices.map((s, i) => (
          <li key={s.key} className="min-w-0">
            <div className="mb-1.5 flex items-baseline justify-between gap-2">
              <span className="truncate text-[13px] font-medium text-foreground">{s.label}</span>
              <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
                <span className="font-semibold text-foreground">{s.value}</span>
                <span className="sr-only"> {unit}, </span>
                {' '}
                {Math.round(s.share * 100)}%
              </span>
            </div>
            <div className="h-2 w-full overflow-hidden rounded-full bg-muted shadow-neu-inset">
              <div
                className="dash-grow h-full rounded-full bg-primary"
                style={{
                  width: `${Math.max(s.share * 100, 2)}%`,
                  '--dash-delay': `${i * 60}ms`,
                } as React.CSSProperties}
              />
            </div>
          </li>
        ))}
      </ul>
    </WidgetCard>
  )
}
```

- [ ] **Step 4: Implement `LifecycleDonut`**

Create `web/src/components/dashboard/LifecycleDonut.tsx`:

```tsx
import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip } from 'recharts'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { ChartDataTable } from './ChartDataTable'
import { WidgetCard } from './WidgetCard'
import { useChartTheme } from './chartTheme'
import { useDashboardMetrics } from './useDashboardMetrics'

/**
 * Where every indexed document sits in the lifecycle. Below `sm` the
 * donut is replaced by a stacked proportion bar — at 330px a donut's
 * labels collide and the legend is the only thing anyone reads anyway.
 */
export function LifecycleDonut({ delayIndex = 0 }: { delayIndex?: number }) {
  const { lifecycle, isLoading, isError, refetch } = useDashboardMetrics()
  const theme = useChartTheme()
  const reduced = usePrefersReducedMotion()

  const total = lifecycle.reduce((sum, s) => sum + s.value, 0)
  const color = (i: number) => theme.series[i % theme.series.length]

  return (
    <WidgetCard
      title="Lifecycle"
      subtitle={`${total} indexed documents`}
      isLoading={isLoading}
      isError={isError}
      isEmpty={lifecycle.length === 0}
      emptyLabel="Nothing indexed yet"
      onRetry={refetch}
      delayIndex={delayIndex}
    >
      <div className="hidden h-[190px] w-full sm:block" role="img" aria-label={`Documents by lifecycle state, ${total} in total`}>
        <ResponsiveContainer width="100%" height="100%">
          <PieChart>
            <Pie
              data={lifecycle}
              dataKey="value"
              nameKey="label"
              cx="50%"
              cy="50%"
              innerRadius={52}
              outerRadius={78}
              paddingAngle={2}
              stroke="none"
              isAnimationActive={!reduced}
              animationDuration={800}
            >
              {lifecycle.map((s, i) => <Cell key={s.key} fill={color(i)} />)}
            </Pie>
            <Tooltip
              contentStyle={{
                background: 'hsl(var(--popover))',
                border: '1px solid hsl(var(--border))',
                borderRadius: '12px',
                color: 'hsl(var(--popover-foreground))',
                fontSize: '12px',
              }}
            />
          </PieChart>
        </ResponsiveContainer>
      </div>

      {/* Below sm: one stacked bar carrying the same proportions. */}
      <div className="sm:hidden" aria-hidden>
        <div className="flex h-3 w-full overflow-hidden rounded-full bg-muted shadow-neu-inset">
          {lifecycle.map((s, i) => (
            <div key={s.key} style={{ width: `${s.share * 100}%`, background: color(i) }} />
          ))}
        </div>
      </div>

      <ul className="mt-4 flex flex-col gap-2">
        {lifecycle.map((s, i) => (
          <li key={s.key} className="flex items-center gap-2 text-xs">
            <span
              className="h-2.5 w-2.5 shrink-0 rounded-full"
              style={{ background: color(i) }}
              aria-hidden
            />
            <span className="min-w-0 flex-1 truncate text-foreground">{s.label}</span>
            <span className="shrink-0 font-semibold tabular-nums text-foreground">{s.value}</span>
          </li>
        ))}
      </ul>

      <ChartDataTable
        caption="Documents by lifecycle state"
        columns={['State', 'Documents']}
        rows={lifecycle.map((s) => ({ key: s.key, label: s.label, value: s.value }))}
      />
    </WidgetCard>
  )
}
```

- [ ] **Step 5: Implement `NeedsAttention`**

Create `web/src/components/dashboard/NeedsAttention.tsx`:

```tsx
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Clock, Flame, Stamp } from 'lucide-react'

import { listMyTasks, taskKeys, type Task } from '@/api/tasks'
import { Button } from '@/components/ui/shadcn/button'
import { cn } from '@/lib/cn'
import { formatRelativeTime } from '@/lib/formatters'
import { WidgetCard } from './WidgetCard'

const OPEN: ReadonlySet<Task['status']> = new Set(['open', 'in_progress'])

// Ranking: overdue first, then urgent/high, then everything else by due
// date. This is presentation only — no task is created, changed or
// reordered server-side.
function rank(t: Task, now: number): number {
  const overdue = t.due_at && new Date(t.due_at).getTime() < now
  if (overdue) return 0
  if (t.priority === 'urgent') return 1
  if (t.priority === 'high') return 2
  if (t.source === 'workflow') return 3
  return 4
}

export function NeedsAttention({ delayIndex = 0 }: { delayIndex?: number }) {
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: taskKeys.mine(),
    queryFn: () => listMyTasks(false),
    staleTime: 30_000,
  })

  const now = Date.now()
  const rows = (data ?? [])
    .filter((t) => OPEN.has(t.status))
    .sort((a, b) => rank(a, now) - rank(b, now))
    .slice(0, 5)

  return (
    <WidgetCard
      title="Needs your attention"
      subtitle="Overdue and high-priority work"
      action={
        <Button variant="ghost" size="sm" asChild>
          <Link to="/tasks">View all</Link>
        </Button>
      }
      isLoading={isLoading}
      isError={isError}
      isEmpty={rows.length === 0}
      emptyLabel="Nothing needs you right now"
      onRetry={() => { void refetch() }}
      delayIndex={delayIndex}
    >
      <ul className="flex flex-col gap-1">
        {rows.map((t) => {
          const overdue = Boolean(t.due_at && new Date(t.due_at).getTime() < now)
          const Icon = overdue ? Clock : t.source === 'workflow' ? Stamp : Flame
          return (
            <li key={t.id}>
              <Link
                to="/tasks"
                className={cn(
                  'flex min-h-[48px] items-center gap-3 rounded-xl px-2 py-2',
                  'transition-shadow hover:shadow-neu-sm',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                )}
              >
                <span
                  className={cn(
                    'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                    // Tinted background with a token-strength foreground:
                    // never text-<color> on bg-<color>/NN, which is the
                    // sub-AA pair the UI phase eliminated.
                    overdue ? 'bg-destructive/10 text-destructive' : 'bg-muted text-foreground',
                  )}
                  aria-hidden
                >
                  <Icon className="h-4 w-4" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium text-foreground">{t.title}</span>
                  <span className="mt-0.5 block text-xs text-muted-foreground">
                    {overdue && t.due_at
                      ? `Overdue ${formatRelativeTime(t.due_at)}`
                      : t.due_at
                        ? `Due ${formatRelativeTime(t.due_at)}`
                        : t.source === 'workflow' ? 'Approval step' : 'No due date'}
                  </span>
                </span>
              </Link>
            </li>
          )
        })}
      </ul>
    </WidgetCard>
  )
}
```

If `Button` does not support `asChild`, replace the action with `<Button variant="ghost" size="sm" onClick={() => navigate({ to: '/tasks' })}>View all</Button>` using `useNavigate`, matching the pattern already in `index.tsx`. Check `src/components/ui/shadcn/button.tsx` before choosing.

- [ ] **Step 6: Run the tests and make sure they pass**

Run: `cd web && npm test -- src/components/dashboard/__tests__/widgets.test.tsx`
Expected: PASS, 15 tests.

- [ ] **Step 7: Typecheck, lint**

Run: `cd web && npx tsc --noEmit && npm run lint`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/dashboard/LifecycleDonut.tsx web/src/components/dashboard/BreakdownBars.tsx web/src/components/dashboard/NeedsAttention.tsx web/src/components/dashboard/__tests__/widgets.test.tsx
git commit -m "feat(web): lifecycle donut, breakdown bars and needs-attention widgets

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Assemble the route and run every gate

**Files:**
- Modify: `web/src/routes/_authenticated/index.tsx`

**Interfaces:**
- Consumes: everything from Tasks 5–7.
- Produces: the rendered dashboard.

- [ ] **Step 1: Rewrite the route as assembly**

Replace the body of `web/src/routes/_authenticated/index.tsx`, keeping `greet`, `QuickActions`, `QUICK_ACTIONS` and the upload-dialog wiring **exactly as they are today**. Delete `KpiRow`, `KpiCard`, `OpenTasksCard`, `TaskRow`, `RecentActivityCard`, `notificationIcon`, `NotificationLite` and `relTime` — every one is replaced by a component from Tasks 5–7. Remove the now-unused imports (`useQuery`, `Card`, `WarmCard`, `Skeleton`, `listMyTasks`, `getWorkspaces`, `getUnreadCount`, `getNotifications`, `formatDate`, and the lucide icons no longer referenced); `npx tsc --noEmit` and eslint's `no-unused-vars` will name any that are missed.

The new component body:

```tsx
function DashboardPage() {
  const user = useAuthStore((s) => s.user)
  const greeting = greet(user?.display_name?.split(' ')[0])
  // Upload dialog state lives here so BOTH triggers share one dialog:
  // the header button (always visible, unmistakable) and the Quick-
  // actions card (click or drop files onto it).
  const [uploadOpen, setUploadOpen] = useState(false)
  const [droppedFiles, setDroppedFiles] = useState<File[]>([])

  return (
    <div className="space-y-6">
      <PageHeader
        title={greeting}
        description="Workspace overview"
        actions={
          <Button onClick={() => setUploadOpen(true)} data-testid="header-upload-button">
            <Upload className="me-1.5 h-4 w-4" aria-hidden /> Upload
          </Button>
        }
      />

      <KpiStrip />

      <PendingSuggestionsCard />

      {/* Trend beside composition: "how it's moving" next to "what I have". */}
      <div className="grid gap-6 lg:grid-cols-3">
        <ActivityChart delayIndex={4} />
        <LifecycleDonut delayIndex={5} />
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <NeedsAttention delayIndex={6} />
        <FileTypesAndContributors />
      </div>

      <QuickActions
        onUploadClick={() => setUploadOpen(true)}
        onUploadDrop={(files) => {
          setDroppedFiles(files)
          setUploadOpen(true)
        }}
      />

      <DashboardUploadDialog
        open={uploadOpen}
        onOpenChange={(v) => {
          setUploadOpen(v)
          if (!v) setDroppedFiles([])
        }}
        initialFiles={droppedFiles}
      />
    </div>
  )
}

// Both panels read the same facet query, so they share one hook call
// rather than each mounting their own.
function FileTypesAndContributors() {
  const { fileTypes, contributors, isLoading, isError, refetch } = useDashboardMetrics()
  return (
    <div className="grid gap-6 sm:grid-cols-2">
      <BreakdownBars
        title="File types"
        slices={fileTypes}
        isLoading={isLoading}
        isError={isError}
        onRetry={refetch}
        emptyLabel="No files indexed"
        unit="documents"
        delayIndex={7}
      />
      <BreakdownBars
        title="Top contributors"
        slices={contributors}
        isLoading={isLoading}
        isError={isError}
        onRetry={refetch}
        emptyLabel="No contributors yet"
        unit="documents"
        delayIndex={8}
      />
    </div>
  )
}
```

Add these imports at the top:

```tsx
import { ActivityChart } from '@/components/dashboard/ActivityChart'
import { BreakdownBars } from '@/components/dashboard/BreakdownBars'
import { KpiStrip } from '@/components/dashboard/KpiStrip'
import { LifecycleDonut } from '@/components/dashboard/LifecycleDonut'
import { NeedsAttention } from '@/components/dashboard/NeedsAttention'
import { useDashboardMetrics } from '@/components/dashboard/useDashboardMetrics'
```

`ActivityChart` spans two of the three columns; give it `className="lg:col-span-2"` by passing the prop through `WidgetCard` — `ActivityChart` already forwards `delayIndex`, so add an optional `className?: string` to `ActivityChart`'s props and pass it to `WidgetCard`, then use `<ActivityChart delayIndex={4} className="lg:col-span-2" />`.

- [ ] **Step 2: Typecheck**

Run: `cd web && npx tsc --noEmit`
Expected: clean. Fix any unused-import errors by deleting the import, never by adding a `void` reference.

- [ ] **Step 3: Lint**

Run: `cd web && npm run lint`
Expected: clean across eslint, `lint:rtl`, `lint:utf8`, `lint:tenant`.

- [ ] **Step 4: Full unit suite**

Run: `cd web && npm test`
Expected: PASS, no regressions. `src/routes/__tests__/ux-error-states.test.tsx` and `src/test/a11y.test.tsx` must still pass — if either mocks a dashboard helper that Task 8 deleted, update the mock, not the component.

- [ ] **Step 5: Production build**

Run: `cd web && npm run build`
Expected: succeeds. Note the dashboard chunk size; recharts is already bundled for `/reports`, so the dashboard should reuse the shared chunk rather than duplicating it.

- [ ] **Step 6: The a11y gate, light and dark**

Run: `cd web && npm run test:e2e -- e2e/70-a11y.spec.ts`
Expected: PASS. If Playwright's browsers are missing system libs, set `LD_LIBRARY_PATH` to the staged lib directory and serve the preview on port 4173, per the "Web a11y gate" notes.

Any axe violation on the dashboard must be fixed in the component. Most likely candidates: a chart `role="img"` without an accessible name, and colour-only encoding in the donut legend — both are already addressed, so a failure here means an implementation drifted from the plan.

- [ ] **Step 7: Manual pass**

Check at 390px, 768px and 1440px, in light **and** dark:
1. No horizontal scroll at 390px; KPI tiles sit 2×2; the lifecycle donut is a stacked bar.
2. Chart axis text, grid lines and series are visible in dark mode — this is what `chartTheme` exists for; hardcoded hex would fail here.
3. Tab order runs KPI tiles → widgets → quick actions, with a visible focus ring on every stop.
4. With `prefers-reduced-motion: reduce` set in the OS/devtools, numerals show their final value immediately and no card slides in.
5. Stop OpenSearch (`docker compose stop opensearch`) and reload: the four facet widgets each show Retry; KPIs, Needs-attention, quick actions and upload still work. Restart it afterwards.

- [ ] **Step 8: Commit**

```bash
git add web/src/routes/_authenticated/index.tsx
git commit -m "feat(web): assemble the redesigned dashboard

Charted overview sourced entirely from data the app already fetches:
one extra read-only POST /search facet call feeds the activity chart,
lifecycle donut, file types and contributors; KPIs reuse the existing
workspace, task and notification queries. No backend, endpoint or
business-logic change.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Self-review notes

- **Spec coverage:** §2 data sourcing → Tasks 2, 3, 5, 6, 7. §2.1 corrections → Task 5 (no storage tile, sparkline only on Documents), Task 6 (monthly, no range toggle). §2.2 degradation → Task 4 per-widget states + Task 8 step 7.5. §3 layout → Task 8. §4 motion → Tasks 1, 4. §5 theming → Task 3. §6 a11y → Task 4 (`ChartDataTable`), Tasks 6–7 (`role="img"`, figures beside bars), Task 8 step 6. §7 testing → every task's test step.
- **Known deviation to watch:** `Button asChild` in `NeedsAttention` (Task 7) is unverified against this repo's button; the step says to check and gives the fallback.
- **Deliberately not built:** daily activity buckets, storage-used, and trend arrows on the three KPIs without a series — all recorded in spec §8.
