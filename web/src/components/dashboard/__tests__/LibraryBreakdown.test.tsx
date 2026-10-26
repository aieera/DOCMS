import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'

import { BreakdownBars } from '../BreakdownBars'
import { LibraryBreakdown } from '../LibraryBreakdown'
import { LifecycleRing } from '../LifecycleDonut'
import { LIFECYCLE_COLORS, lifecycleColor } from '../chartTheme'
import * as metricsHook from '../useDashboardMetrics'
import { withOther, type Slice } from '../metrics'
import enCommon from '../../../../public/locales/en/common.json'
import arCommon from '../../../../public/locales/ar/common.json'

// Reduced motion, so the ring draws its final sectors on the first render
// rather than animating in from nothing.
function preferReducedMotion() {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: true, media: query, onchange: null,
    addEventListener: vi.fn(), removeEventListener: vi.fn(),
    addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn(),
  }))
}

const emptyMetrics = {
  activity: [], lifecycle: [], fileTypes: [], fileTypesTotal: 0, contributors: [],
  isLoading: false, isError: false, isUnavailable: false, refetch: vi.fn(),
}

const LIFECYCLE: Slice[] = [
  { key: 'active', label: 'Active', value: 30, share: 0.6 },
  { key: 'draft', label: 'Draft', value: 15, share: 0.3 },
  { key: 'archived', label: 'Archived', value: 5, share: 0.1 },
]
const FILE_TYPES: Slice[] = [
  { key: 'application/pdf', label: 'PDF', value: 20, share: 0.4 },
  { key: 'application/msword', label: 'Word', value: 10, share: 0.2 },
]
const CONTRIBUTORS: Slice[] = [
  { key: 'Alice Mason', label: 'Alice Mason', value: 25, share: 0.5 },
  { key: 'bob@example.com', label: 'bob@example.com', value: 15, share: 0.3 },
]
// The file-type facet covers all 50 documents, 30 of them in the two types shown.
const loaded = {
  ...emptyMetrics, lifecycle: LIFECYCLE, fileTypes: FILE_TYPES, fileTypesTotal: 50, contributors: CONTRIBUTORS,
}

const column = (name: string) => screen.getByRole('group', { name })

afterEach(async () => {
  // Unmount first: switching the language re-renders anything still mounted,
  // and by then the metrics spy is gone and the real query hook would run.
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  await act(async () => { await i18n.changeLanguage('en') })
})

describe('LibraryBreakdown', () => {
  it('renders the three columns from one metrics call', () => {
    const spy = vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
    render(<LibraryBreakdown />)

    expect(spy).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('region', { name: 'Library breakdown' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'Library breakdown' })).toBeInTheDocument()
    for (const name of ['Lifecycle', 'File types', 'Top contributors']) {
      expect(screen.getByRole('heading', { level: 3, name })).toBeInTheDocument()
    }
  })

  it('is one card: no nested region per column', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
    render(<LibraryBreakdown />)
    expect(screen.getAllByRole('region')).toHaveLength(1)
  })

  it('shows the failure state once with Retry, and none of the columns', async () => {
    const refetch = vi.fn()
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isError: true, refetch })
    render(<LibraryBreakdown />)

    expect(screen.getAllByRole('button', { name: /retry/i })).toHaveLength(1)
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(refetch).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('heading', { level: 3 })).not.toBeInTheDocument()
    expect(screen.queryByText(/nothing indexed|no files indexed|no contributors/i)).not.toBeInTheDocument()
  })

  it('says the facets are unavailable once, never a "No ... yet" claim', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isUnavailable: true })
    render(<LibraryBreakdown />)
    expect(screen.getAllByText('Too many documents to chart')).toHaveLength(1)
    expect(screen.queryByText(/nothing indexed|no files indexed|no contributors/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 3 })).not.toBeInTheDocument()
  })

  it('shows the empty copy once when no column has data', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(emptyMetrics)
    render(<LibraryBreakdown />)
    expect(screen.getAllByText('Nothing indexed yet')).toHaveLength(1)
    expect(screen.queryByRole('heading', { level: 3 })).not.toBeInTheDocument()
  })

  it('keeps the other columns when one has nothing to show', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...loaded, contributors: [] })
    render(<LibraryBreakdown />)
    expect(within(column('Top contributors')).getByText('No contributors yet')).toBeInTheDocument()
    expect(within(column('File types')).getByText('PDF')).toBeInTheDocument()
    expect(within(column('Lifecycle')).getByText('Active', { selector: 'li span' })).toBeInTheDocument()
  })

  it('renders a skeleton, no column and no empty copy while loading', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isLoading: true })
    render(<LibraryBreakdown />)
    expect(screen.getByRole('region', { name: 'Library breakdown' })).toHaveAttribute('aria-busy', 'true')
    expect(screen.queryByRole('heading', { level: 3 })).not.toBeInTheDocument()
    expect(screen.queryByText(/nothing indexed/i)).not.toBeInTheDocument()
  })

  it('reserves its loaded height while loading (the min-height includes the 20px body padding)', () => {
    const bodyClass = () => screen.getByRole('region', { name: 'Library breakdown' }).children[1].getAttribute('class') ?? ''
    const spy = vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isLoading: true })
    const { rerender } = render(<LibraryBreakdown />)
    const loading = bodyClass()
    spy.mockReturnValue(loaded)
    rerender(<LibraryBreakdown />)
    expect(loading).toMatch(/\bmin-h-\[\d+px\]/)
    expect(bodyClass()).toBe(loading)
  })

  it('lays the columns out three-up from the shell breakpoint with logical dividers', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
    render(<LibraryBreakdown />)
    const grid = column('Lifecycle').parentElement as HTMLElement
    expect(grid.className).toMatch(/\bgrid-cols-1\b/)
    expect(grid.className).toMatch(/\bshell:grid-cols-3\b/)
    expect(grid.className).toMatch(/\bdivide-y\b/)
    // divide-x is physical in Tailwind 3 and would put the rule on the wrong
    // side under dir="rtl"; the columns carry a logical border-s instead.
    expect(grid.className).not.toMatch(/\bdivide-x\b/)
    expect(column('File types').className).toMatch(/\bshell:border-s\b/)
    expect(column('Top contributors').className).toMatch(/\bshell:border-s\b/)
    expect(column('Lifecycle').className).not.toMatch(/border-s/)
  })

  describe('lifecycle column', () => {
    it('lists every state with its count and exposes the numbers as a table', () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
      render(<LibraryBreakdown />)
      const col = column('Lifecycle')
      for (const [label, count] of [['Active', '30'], ['Draft', '15'], ['Archived', '5']]) {
        // Each state is also a cell in the accessible table, so scope to the legend.
        const row = within(col).getByText(label, { selector: 'li span' }).closest('li') as HTMLElement
        expect(row).toHaveTextContent(count)
      }
      expect(screen.getByRole('table', { name: 'Documents by lifecycle state' })).toBeInTheDocument()
    })

    it('names the ring with the total', () => {
      preferReducedMotion()
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
      render(<LibraryBreakdown />)
      expect(screen.getByRole('img', { name: 'Documents by lifecycle state, 50 in total' })).toBeInTheDocument()
    })

    it('says what the shares in the other two columns are of', () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
      render(<LibraryBreakdown />)
      expect(column('File types')).toHaveTextContent('Of indexed documents')
      expect(column('Top contributors')).toHaveTextContent('Of indexed documents')
    })
  })

  describe('file types column', () => {
    it('shows each type with its count and share', () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
      render(<LibraryBreakdown />)
      const col = column('File types')
      const pdf = within(col).getByText('PDF').closest('li') as HTMLElement
      expect(pdf).toHaveTextContent('20')
      expect(pdf).toHaveTextContent('40%')
      const word = within(col).getByText('Word').closest('li') as HTMLElement
      expect(word).toHaveTextContent('10')
      expect(word).toHaveTextContent('20%')
    })
  })

  describe('top contributors column', () => {
    it('shows contributors with share percentages', () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
      render(<LibraryBreakdown />)
      const col = column('Top contributors')
      const alice = within(col).getByText('Alice Mason').closest('li') as HTMLElement
      expect(alice).toHaveTextContent('25')
      expect(alice).toHaveTextContent('50%')
      const bob = within(col).getByText('bob@example.com').closest('li') as HTMLElement
      expect(bob).toHaveTextContent('15')
      expect(bob).toHaveTextContent('30%')
    })

    it('gives each contributor an initial and a progress bar at their share', () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
      render(<LibraryBreakdown />)
      const col = column('Top contributors')
      expect(within(col).getByText('A')).toBeInTheDocument()
      expect(within(col).getByText('B')).toBeInTheDocument()
      const bars = within(col).getAllByRole('progressbar', { hidden: true })
      expect(bars.map((b) => b.getAttribute('aria-valuenow'))).toEqual(['50', '30'])
    })

    it('reads the count aloud with its unit, so the figure is never a bare number', () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
      render(<LibraryBreakdown />)
      const alice = within(column('Top contributors')).getByText('Alice Mason').closest('li') as HTMLElement
      expect(alice).toHaveTextContent('25 documents, 50%')
    })
  })
})

describe('lifecycle colours are keyed by state, not by position', () => {
  it('maps each lifecycle state to its own token, and anything unknown to the border colour', () => {
    expect(LIFECYCLE_COLORS).toEqual({
      draft: 'var(--chart-4)',
      in_review: 'var(--chart-5)',
      active: 'var(--chart-1)',
      superseded: 'var(--chart-3)',
      retained: 'var(--chart-2)',
      archived: 'var(--muted-foreground)',
      disposed: 'var(--ring)',
    })
    expect(lifecycleColor('active')).toBe('var(--chart-1)')
    expect(lifecycleColor('quarantined')).toBe('var(--border)')
    expect(lifecycleColor('constructor')).toBe('var(--border)')
  })

  it('gives a state the same ring colour wherever it sits in the list', () => {
    preferReducedMotion()
    const slices: Slice[] = [
      { key: 'draft', label: 'Draft', value: 5, share: 0.1 },
      { key: 'in_review', label: 'In review', value: 5, share: 0.1 },
      { key: 'archived', label: 'Archived', value: 5, share: 0.1 },
      { key: 'active', label: 'Active', value: 5, share: 0.1 },
      { key: 'disposed', label: 'Disposed', value: 5, share: 0.1 },
      { key: 'superseded', label: 'Superseded', value: 5, share: 0.1 },
      { key: 'mystery', label: 'Mystery', value: 5, share: 0.1 },
    ]
    const { container } = render(<LifecycleRing slices={slices} total={35} />)
    const fills = [...container.querySelectorAll('.recharts-pie-sector path')].map((p) => p.getAttribute('fill'))
    expect(fills).toEqual([
      'var(--chart-4)', 'var(--chart-5)', 'var(--muted-foreground)', 'var(--chart-1)',
      'var(--ring)', 'var(--chart-3)', 'var(--border)',
    ])
  })

  it('colours the legend dots by state as well, so seven states never repeat a hue', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...loaded,
      lifecycle: [
        { key: 'archived', label: 'Archived', value: 9, share: 0.5 },
        { key: 'active', label: 'Active', value: 5, share: 0.3 },
        { key: 'draft', label: 'Draft', value: 4, share: 0.2 },
      ],
    })
    render(<LibraryBreakdown />)
    const dot = (label: string) =>
      (within(column('Lifecycle')).getByText(label, { selector: 'li span' }).closest('li') as HTMLElement)
        .querySelector('[data-testid="legend-dot"]') as HTMLElement
    expect(dot('Active').style.backgroundColor).toBe('var(--chart-1)')
    expect(dot('Draft').style.backgroundColor).toBe('var(--chart-4)')
    expect(dot('Archived').style.backgroundColor).toBe('var(--muted-foreground)')
  })
})

describe('LifecycleRing', () => {
  it('is an 88px ring named with the total, its sectors hidden from assistive tech', () => {
    preferReducedMotion()
    const { container } = render(<LifecycleRing slices={LIFECYCLE} total={50} />)
    const ring = screen.getByRole('img', { name: 'Documents by lifecycle state, 50 in total' })
    expect(ring.className).toMatch(/\bh-\[88px\]/)
    expect(ring.className).toMatch(/\bw-\[88px\]/)
    expect(container.querySelectorAll('.recharts-pie-sector path')).toHaveLength(3)
    expect(ring.querySelector('[aria-hidden="true"] .recharts-pie')).not.toBeNull()
  })

  it('prints the total in the middle, compact so a large library still fits', () => {
    preferReducedMotion()
    render(<LifecycleRing slices={LIFECYCLE} total={1_200_000} />)
    expect(screen.getByText('1.2M')).toBeInTheDocument()
  })
})

describe('BreakdownBars variant="stacked"', () => {
  const NAMES = ['PDF', 'Word', 'Excel', 'PowerPoint', 'Text', 'Image', 'Archive']
  const seven: Slice[] = NAMES.map((label, i) => ({
    key: label.toLowerCase(), label, value: 70 - i * 10, share: (70 - i * 10) / 280,
  }))
  const stacked = (slices: Slice[], total?: number) => (
    <BreakdownBars variant="stacked" slices={slices} total={total} otherLabel="Other" />
  )
  const segments = (container: HTMLElement) =>
    [...(container.querySelector('[data-testid="stacked-bar"]')?.children ?? [])] as HTMLElement[]

  it('draws an 8px bar and a list with count and share', () => {
    const { container } = render(stacked(FILE_TYPES))
    const bar = container.querySelector('[data-testid="stacked-bar"]') as HTMLElement
    expect(bar.className).toMatch(/\bh-2\b/)
    expect(bar).toHaveAttribute('aria-hidden', 'true')
    const rows = screen.getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('PDF')
    expect(rows[0]).toHaveTextContent('20 documents, 40%')
  })

  it('has no card frame of its own: the caller owns the title and the states', () => {
    render(stacked(FILE_TYPES))
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
  })

  it('colours the first five slices chart-1 to chart-5 by rank', () => {
    const { container } = render(stacked(seven.slice(0, 5)))
    expect(segments(container).map((s) => s.style.backgroundColor)).toEqual([
      'var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)',
    ])
    expect(screen.queryByText('Other')).not.toBeInTheDocument()
  })

  // I9: "Other" is neutral because of what it is, not where it falls. With
  // three named types it sits at index 3, where the palette has chart-4.
  it('colours Other neutral by its key when fewer than five types are named', () => {
    const { container } = render(stacked(seven.slice(0, 3), 280))
    expect(segments(container).map((s) => s.style.backgroundColor)).toEqual([
      'var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--muted-foreground)',
    ])
    const rows = screen.getAllByRole('listitem')
    expect(rows).toHaveLength(4)
    expect(rows[3]).toHaveTextContent('Other')
    const dot = rows[3].querySelector('[aria-hidden]') as HTMLElement
    expect(dot.style.backgroundColor).toBe('var(--muted-foreground)')
  })

  it('merges everything past the fifth into one neutral Other segment', () => {
    const { container } = render(stacked(seven))
    const segs = segments(container)
    expect(segs).toHaveLength(6)
    expect(segs[5].style.backgroundColor).toBe('var(--muted-foreground)')

    const rows = screen.getAllByRole('listitem')
    expect(rows).toHaveLength(6)
    expect(rows[4]).toHaveTextContent('Text')
    // 20 + 10 documents; 30 / 280 rounds to 11%.
    expect(rows[5]).toHaveTextContent('Other')
    expect(rows[5]).toHaveTextContent('30 documents, 11%')
    expect(screen.queryByText('Image')).not.toBeInTheDocument()
    expect(screen.queryByText('Archive')).not.toBeInTheDocument()
  })

  it('sizes each segment by its share', () => {
    const { container } = render(stacked(FILE_TYPES))
    expect(segments(container).map((s) => s.style.width)).toEqual(['40%', '20%'])
  })

  it('keeps the plain bars variant working unchanged', () => {
    render(
      <BreakdownBars title="File types" slices={FILE_TYPES} isLoading={false} isError={false}
        onRetry={vi.fn()} emptyLabel="No files yet" />,
    )
    expect(screen.getByRole('region', { name: 'File types' })).toBeInTheDocument()
    expect(screen.queryByTestId('stacked-bar')).not.toBeInTheDocument()
  })
})

describe('withOther', () => {
  const mk = (n: number): Slice[] =>
    Array.from({ length: n }, (_, i) => ({ key: `k${i}`, label: `L${i}`, value: 10 - i, share: (10 - i) / 100 }))

  it('returns the slices untouched when they fit', () => {
    expect(withOther(mk(5), 5, 'Other')).toEqual(mk(5))
    expect(withOther([], 5, 'Other')).toEqual([])
  })

  it('folds the tail into one slice that sums its values and shares', () => {
    const out = withOther(mk(8), 5, 'Other')
    expect(out).toHaveLength(6)
    expect(out.slice(0, 5)).toEqual(mk(5))
    expect(out[5].label).toBe('Other')
    expect(out[5].value).toBe(5 + 4 + 3)
    expect(out[5].share).toBeCloseTo(0.12)
  })

  describe('with the facet total', () => {
    it('adds the unshown remainder as Other, so the shares close at 1', () => {
      // mk(5) = 10 + 9 + 8 + 7 + 6 = 40 documents of a facet of 100.
      const out = withOther(mk(5), 5, 'Other', 100)
      expect(out).toHaveLength(6)
      expect(out[5]).toEqual({ key: '__other', label: 'Other', value: 60, share: 0.6 })
    })

    it('adds nothing when the slices account for the whole facet', () => {
      expect(withOther(mk(5), 5, 'Other', 40)).toEqual(mk(5))
    })

    it('counts a folded tail and the unshown remainder together', () => {
      // mk(8) = 10 + 9 + ... + 3 = 52; keep 5 (40), facet of 100 -> 60 other.
      const out = withOther(mk(8), 5, 'Other', 100)
      expect(out).toHaveLength(6)
      expect(out[5].value).toBe(60)
      expect(out[5].share).toBeCloseTo(0.6)
    })

    it('uses the larger of the total and the slices\' own sum', () => {
      // A total of 10 against 52 documents of slices is inconsistent input;
      // the larger number wins rather than a negative remainder.
      const out = withOther(mk(8), 5, 'Other', 10)
      expect(out[5].value).toBe(12)
    })
  })
})

// The hook caps file types at the top five, but the facet can hold more. The
// card derives "Other" from the facet total so the bar always closes at 100%.
describe('the Other remainder of a truncated file-type facet', () => {
  // Five types shown of a facet of 100 documents: 30 + 20 + 15 + 10 + 5 = 80.
  const FIVE: Slice[] = [30, 20, 15, 10, 5].map((value, i) => ({
    key: `t${i}`, label: `Type ${i + 1}`, value, share: value / 100,
  }))
  const shownPercents = () =>
    screen.getAllByRole('listitem').map((li) => Number(/(\d+)%/.exec(li.textContent ?? '')?.[1]))

  it('shows one neutral Other segment and row when the five shown fall short of the facet', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...loaded, fileTypes: FIVE, fileTypesTotal: 100 })
    render(<LibraryBreakdown />)
    const col = column('File types')

    const segments = [...(col.querySelector('[data-testid="stacked-bar"]')?.children ?? [])] as HTMLElement[]
    expect(segments).toHaveLength(6)
    expect(segments[5].style.backgroundColor).toBe('var(--muted-foreground)')
    expect(segments[5].style.width).toBe('20%')

    const rows = within(col).getAllByRole('listitem')
    expect(rows).toHaveLength(6)
    expect(rows[5]).toHaveTextContent('Other')
    expect(rows[5]).toHaveTextContent('20 documents, 20%')
  })

  it('closes the list at 100% (within rounding)', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...loaded, fileTypes: FIVE, fileTypesTotal: 100 })
    render(<LibraryBreakdown />)
    const col = column('File types')
    const percents = within(col).getAllByRole('listitem').map((li) => Number(/(\d+)%/.exec(li.textContent ?? '')?.[1]))
    expect(percents.reduce((a, b) => a + b, 0)).toBe(100)
  })

  it('shows no Other when the types shown account for the whole facet', () => {
    const whole: Slice[] = [60, 40].map((value, i) => ({ key: `w${i}`, label: `W${i}`, value, share: value / 100 }))
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...loaded, fileTypes: whole, fileTypesTotal: 100 })
    render(<LibraryBreakdown />)
    expect(within(column('File types')).queryByText('Other')).not.toBeInTheDocument()
    expect(within(column('File types')).getAllByRole('listitem')).toHaveLength(2)
  })

  it('folds a tail and the unshown remainder into the same single Other', () => {
    const seven: Slice[] = [30, 20, 15, 10, 5, 4, 3].map((value, i) => ({
      key: `s${i}`, label: `S${i + 1}`, value, share: value / 100,
    }))
    render(<BreakdownBars variant="stacked" slices={seven} total={100} otherLabel="Other" />)
    const rows = screen.getAllByRole('listitem')
    expect(rows).toHaveLength(6)
    // 100 - (30 + 20 + 15 + 10 + 5): the two tail types plus the 13 unshown.
    expect(rows[5]).toHaveTextContent('20 documents, 20%')
    expect(shownPercents().reduce((a, b) => a + b, 0)).toBe(100)
  })

  it('is not asked to invent a remainder when the caller gives no total', () => {
    render(<BreakdownBars variant="stacked" slices={FIVE} otherLabel="Other" />)
    expect(screen.queryByText('Other')).not.toBeInTheDocument()
  })
})

describe('count copy is pluralised, not concatenated', () => {
  const COUNTS = [1, 2, 5, 25]
  const slices: Slice[] = COUNTS.map((value) => ({ key: `c${value}`, label: `L${value}`, value, share: value / 100 }))
  const spoken = (container: HTMLElement) =>
    [...container.querySelectorAll('li .sr-only')].map((n) => n.textContent)

  it('reads English counts with the right noun in every place a count is shown', () => {
    const want = ['1 document, 1%', '2 documents, 2%', '5 documents, 5%', '25 documents, 25%']

    const stacked = render(<BreakdownBars variant="stacked" slices={slices} total={100} otherLabel="Other" />)
    expect(spoken(stacked.container).slice(0, 4)).toEqual(want)
    stacked.unmount()

    const bars = render(
      <BreakdownBars title="Bars" slices={slices} isLoading={false} isError={false} onRetry={vi.fn()} emptyLabel="-" />,
    )
    expect(spoken(bars.container)).toEqual(want)
    bars.unmount()

    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...loaded, contributors: slices.map((s) => ({ ...s, label: `Person ${s.value}` })),
    })
    const card = render(<LibraryBreakdown />)
    expect(spoken(within(column('Top contributors')).getAllByRole('list')[0].parentElement as HTMLElement)).toEqual(want)
    card.unmount()
  })

  it('keeps the visible figure exactly as before and hides it from assistive tech', () => {
    const { container } = render(<BreakdownBars variant="stacked" slices={slices} otherLabel="Other" />)
    const first = container.querySelector('li') as HTMLElement
    const visible = first.querySelector('.tabular-nums > [aria-hidden="true"]') as HTMLElement
    expect(visible).toHaveTextContent('1 1%')
    expect(within(first).getByText('1', { selector: '.font-semibold' })).toBeInTheDocument()
  })

  describe('in Arabic', () => {
    const withArabic = async () => {
      i18n.addResourceBundle('ar', 'common', arCommon, true, true)
      await act(async () => { await i18n.changeLanguage('ar') })
    }
    const WANT = ['مستند واحد، 1٪', 'مستندان، 2٪', '5 مستندات، 5٪', '25 مستندًا، 25٪']

    it('gives one, two, few and many their own forms in the stacked list', async () => {
      await withArabic()
      const { container } = render(<BreakdownBars variant="stacked" slices={slices} total={100} otherLabel="أخرى" />)
      expect(spoken(container).slice(0, 4)).toEqual(WANT)
    })

    it('does the same in the plain bars variant', async () => {
      await withArabic()
      const { container } = render(
        <BreakdownBars title="x" slices={slices} isLoading={false} isError={false} onRetry={vi.fn()} emptyLabel="-" />,
      )
      expect(spoken(container)).toEqual(WANT)
    })

    it('does the same in the contributors rows', async () => {
      await withArabic()
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
        ...loaded, contributors: slices.map((s) => ({ ...s, label: `Person ${s.value}` })),
      })
      render(<LibraryBreakdown />)
      expect(spoken(column('أبرز المساهمين'))).toEqual(WANT)
    })

    it('uses no Latin comma between the count and the share', async () => {
      await withArabic()
      const { container } = render(<BreakdownBars variant="stacked" slices={slices} otherLabel="أخرى" />)
      for (const text of spoken(container)) expect(text).not.toContain(',')
    })

    it('resolves every CLDR count class, including zero', () => {
      i18n.addResourceBundle('ar', 'common', arCommon, true, true)
      const ar = i18n.getFixedT('ar', 'common')
      const out = [0, 1, 2, 3, 11, 100].map((count) => ar('dashboard.widgets.doc_count', { count }))
      expect(out).toEqual(['لا مستندات', 'مستند واحد', 'مستندان', '3 مستندات', '11 مستندًا', '100 مستند'])
    })
  })
})

describe('in Arabic (RTL)', () => {
  const withArabic = async () => {
    i18n.addResourceBundle('ar', 'common', arCommon, true, true)
    await act(async () => { await i18n.changeLanguage('ar') })
  }

  it('translates the card title, the column headings and the captions', async () => {
    await withArabic()
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
    render(<LibraryBreakdown />)
    expect(screen.getByRole('heading', { level: 2, name: 'تفصيل المكتبة' })).toBeInTheDocument()
    for (const name of ['دورة الحياة', 'أنواع الملفات', 'أبرز المساهمين']) {
      expect(screen.getByRole('heading', { level: 3, name })).toBeInTheDocument()
    }
    expect(screen.getByRole('table', { name: 'المستندات بحسب حالة دورة الحياة' })).toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(/Library breakdown|Top contributors|Of indexed/)
  })

  it('names the merged tail "أخرى"', async () => {
    await withArabic()
    const many: Slice[] = ['a', 'b', 'c', 'd', 'e', 'f'].map((k, i) => ({
      key: k, label: k.toUpperCase(), value: 6 - i, share: (6 - i) / 21,
    }))
    render(<BreakdownBars variant="stacked" slices={many} otherLabel={i18n.t('dashboard.widgets.other')} />)
    expect(screen.getByText('أخرى')).toBeInTheDocument()
  })

  it('fills the contributor bars from the inline start, which is the right edge here', async () => {
    await withArabic()
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue(loaded)
    render(<LibraryBreakdown />)
    const indicators = within(column('أبرز المساهمين')).getAllByRole('progressbar', { hidden: true })
      .map((bar) => (bar.firstElementChild as HTMLElement).style.transform)
    // 50% and 30% filled: the indicator slides in from the right, so the
    // offset is positive (an LTR bar uses the negative of the same number).
    expect(indicators).toEqual(['translateX(50%)', 'translateX(70%)'])
  })
})

describe('catalogue', () => {
  const widgets = (c: typeof enCommon) => c.dashboard.widgets as Record<string, string>

  it('ships library_title and other in en and ar', () => {
    expect(widgets(enCommon).library_title).toBe('Library breakdown')
    expect(widgets(arCommon as unknown as typeof enCommon).library_title).toBe('تفصيل المكتبة')
    expect(widgets(enCommon).other).toBe('Other')
    expect(widgets(arCommon as unknown as typeof enCommon).other).toBe('أخرى')
  })
})
