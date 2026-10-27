import { cloneElement, isValidElement, type ReactElement, type ReactNode } from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import i18n from 'i18next'

import { DocumentsAddedChart } from '../DocumentsAddedChart'
import * as metricsHook from '../useDashboardMetrics'
import type { Point } from '../metrics'
import arCommon from '../../../../public/locales/ar/common.json'

// jsdom has no layout, so recharts' ResponsiveContainer measures 0x0 and
// draws nothing. Hand the chart fixed dimensions, in this file only, so the
// bars and their value labels can be asserted on.
vi.mock('recharts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('recharts')>()
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children: ReactNode }) => (
      <div style={{ width: 600, height: 180 }}>
        {isValidElement(children)
          ? cloneElement(children as ReactElement<{ width: number; height: number }>, { width: 600, height: 180 })
          : children}
      </div>
    ),
  }
})

// Reduced motion, so the bars (and their labels) are drawn in their final
// state on the first render instead of animating in from nothing.
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

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const series = (values: number[]): Point[] =>
  values.map((value, i) => ({
    label: MONTHS[i],
    iso: new Date(Date.UTC(2026, i, 1)).toISOString(),
    value,
  }))

describe('DocumentsAddedChart', () => {
  // L101/L113: restore in afterEach, so a failing assertion can't skip
  // the restore and leak its spy into every test after it.
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('renders the failure state with Retry, not the empty state', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isError: true })
    render(<DocumentsAddedChart />)
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText(/no documents added/i)).not.toBeInTheDocument()
  })

  // I3: an empty 12-month window is not an empty tenant — an archive
  // imported more than a year ago sits right beside a Documents KPI of 500.
  it('says the window is empty, never that no documents exist "yet"', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics })
    render(<DocumentsAddedChart />)
    expect(screen.getByText('No documents added in the last 12 months')).toBeInTheDocument()
    expect(screen.queryByText(/yet/i)).not.toBeInTheDocument()
  })

  it('exposes the series as an accessible table so the numbers are not trapped in an image', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      activity: [
        { label: 'Jan', iso: '2026-01-01T00:00:00.000Z', value: 12 },
        { label: 'Feb', iso: '2026-02-01T00:00:00.000Z', value: 48 },
      ],
    })
    render(<DocumentsAddedChart />)
    expect(screen.getByRole('table', { name: /documents added per month/i })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '48' })).toBeInTheDocument()
  })

  it('shows the 12-month total and the busiest month', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      // Total 1,234; March (index 2) is the single maximum.
      activity: series([100, 80, 500, 60, 40, 30, 20, 100, 90, 70, 60, 84]),
    })
    render(<DocumentsAddedChart />)
    expect(screen.getByText('Last 12 months').parentElement).toHaveTextContent('1,234')
    expect(screen.getByText('Busiest month').parentElement).toHaveTextContent('Mar')
    // The subtitle names the window by its first and last month label.
    expect(screen.getByText('Indexed documents per month, Jan – Dec')).toBeInTheDocument()
  })

  it('names the first month when two months tie for the busiest', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      activity: series([0, 0, 0, 0, 50, 0, 0, 0, 50, 0, 0, 0]),
    })
    render(<DocumentsAddedChart />)
    expect(screen.getByText('Busiest month').parentElement).toHaveTextContent('May')
    expect(screen.getByText('Busiest month').parentElement).not.toHaveTextContent('Sep')
  })

  it('renders the empty state, with no busiest month, when every month is zero', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics,
      activity: series(Array<number>(12).fill(0)),
    })
    render(<DocumentsAddedChart />)
    expect(screen.getByText('No documents added in the last 12 months')).toBeInTheDocument()
    expect(screen.queryByText('Busiest month')).not.toBeInTheDocument()
    expect(screen.queryByText('Last 12 months')).not.toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('says "Too many documents to chart" when the facets are unavailable, with no stats', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isUnavailable: true })
    render(<DocumentsAddedChart />)
    expect(screen.getByText('Too many documents to chart')).toBeInTheDocument()
    expect(screen.queryByText('Busiest month')).not.toBeInTheDocument()
  })

  it('shows no subtitle or stats while loading', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isLoading: true })
    render(<DocumentsAddedChart />)
    expect(screen.queryByText(/Indexed documents per month/)).not.toBeInTheDocument()
    expect(screen.queryByText('Busiest month')).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Documents added' })).toHaveAttribute('aria-busy', 'true')
  })

  it('summarises the trend on the chart as a single named image', () => {
    const activity = series([100, 80, 500, 60, 40, 30, 20, 100, 90, 70, 60, 84])
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, activity })
    render(<DocumentsAddedChart />)
    expect(screen.getByRole('img', { name: '1,234 documents added from Jan to Dec; busiest month Mar' }))
      .toBeInTheDocument()
  })

  // I10: the summary is pluralised on the numeric total; the figure itself
  // is a separate, locale-formatted interpolation.
  it('says "1 document added" in the singular', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      ...emptyMetrics, activity: series([0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0]),
    })
    render(<DocumentsAddedChart />)
    expect(screen.getByRole('img', { name: '1 document added from Jan to Dec; busiest month Mar' }))
      .toBeInTheDocument()
  })

  it('applies the caller class to the card frame', () => {
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics })
    render(<DocumentsAddedChart className="lg:col-span-2" />)
    expect(screen.getByRole('region', { name: 'Documents added' })).toHaveClass('lg:col-span-2')
  })

  describe('bars', () => {
    it('draws a chart-1 bar and a value label for each month that has documents, and nothing for empty months', () => {
      preferReducedMotion()
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
        ...emptyMetrics,
        activity: series([100, 0, 500, 0, 40, 30, 20, 100, 90, 70, 60, 84]),
      })
      const { container } = render(<DocumentsAddedChart />)

      const bars = container.querySelectorAll('.recharts-bar-rectangle path')
      // Recharts draws no rectangle for a zero-height bar: 2 of the 12 months are empty.
      expect(bars).toHaveLength(10)
      // A CSS variable, not a resolved hex: the bars follow light/dark
      // without re-reading computed styles.
      for (const bar of bars) expect(bar).toHaveAttribute('fill', 'var(--chart-1)')

      const labels = [...container.querySelectorAll('.recharts-label-list text')].map((n) => n.textContent)
      expect(labels).toEqual(['100', '500', '40', '30', '20', '100', '90', '70', '60', '84'])
    })
  })

  // ADR 0108: under dir="rtl" the months run right to left and the value axis
  // sits on the right edge. useDirection reads i18next's resolved language, so
  // load the real Arabic bundle and switch to it; the ResponsiveContainer mock
  // above gives recharts a fixed 600px canvas, so tick positions are real.
  describe('in Arabic (RTL)', () => {
    const x = (el: Element) => Number(el.getAttribute('x'))
    const tick = (container: HTMLElement, axis: string, text: string) => {
      const found = [...container.querySelectorAll(`.recharts-${axis} .recharts-cartesian-axis-tick-value`)]
        .find((n) => n.textContent === text)
      expect(found, `${axis} tick "${text}"`).toBeDefined()
      return found as Element
    }
    const withLanguage = async (lng: string) => {
      i18n.addResourceBundle('ar', 'common', arCommon, true, true)
      await act(async () => { await i18n.changeLanguage(lng) })
    }
    afterEach(async () => {
      await act(async () => { await i18n.changeLanguage('en') })
    })

    const activity = series([100, 80, 500, 60, 40, 30, 20, 100, 90, 70, 60, 84])

    it('lays the first month to the right of the last and puts the value axis on the right', async () => {
      preferReducedMotion()
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, activity })
      await withLanguage('ar')
      const { container } = render(<DocumentsAddedChart />)

      expect(x(tick(container, 'xAxis', 'Jan'))).toBeGreaterThan(x(tick(container, 'xAxis', 'Dec')))
      // 600px canvas: an axis on the physical right has its ticks past the midpoint.
      expect(x(tick(container, 'yAxis', '0'))).toBeGreaterThan(300)
    })

    it('keeps months left to right with the value axis on the left in English', async () => {
      preferReducedMotion()
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, activity })
      const { container } = render(<DocumentsAddedChart />)

      expect(x(tick(container, 'xAxis', 'Jan'))).toBeLessThan(x(tick(container, 'xAxis', 'Dec')))
      expect(x(tick(container, 'yAxis', '0'))).toBeLessThan(300)
    })

    it('still draws the value labels above the bars', async () => {
      preferReducedMotion()
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, activity })
      await withLanguage('ar')
      const { container } = render(<DocumentsAddedChart />)

      const labels = [...container.querySelectorAll('.recharts-label-list text')].map((n) => n.textContent)
      expect(labels).toHaveLength(12)
      expect(labels).toContain('500')
    })

    // CLDR Arabic: 2 is dual, 3-10 few (plural noun), 11-99 many (singular
    // accusative noun); the total is formatted separately from the count.
    it.each([
      [2, 'أُضيف مستندان من Jan إلى Dec؛ الشهر الأكثر نشاطًا Mar'],
      [5, 'أُضيفت 5 مستندات من Jan إلى Dec؛ الشهر الأكثر نشاطًا Mar'],
      [25, 'أُضيف 25 مستندًا من Jan إلى Dec؛ الشهر الأكثر نشاطًا Mar'],
      [1, 'أُضيف مستند واحد من Jan إلى Dec؛ الشهر الأكثر نشاطًا Mar'],
      [100, 'أُضيف 100 مستند من Jan إلى Dec؛ الشهر الأكثر نشاطًا Mar'],
    ])('pluralises the Arabic summary for %i documents', async (total, name) => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
        ...emptyMetrics, activity: series([0, 0, total, 0, 0, 0, 0, 0, 0, 0, 0, 0]),
      })
      await withLanguage('ar')
      render(<DocumentsAddedChart />)
      expect(screen.getByRole('img', { name })).toBeInTheDocument()
    })

    it('says the facets are unavailable in Arabic', async () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, isUnavailable: true })
      await withLanguage('ar')
      render(<DocumentsAddedChart />)
      expect(screen.getByText(arCommon.dashboard.widgets.facets_unavailable)).toBeInTheDocument()
      expect(screen.queryByText('Too many documents to chart')).not.toBeInTheDocument()
    })

    it('localises the chart summary, the table and the stats', async () => {
      vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({ ...emptyMetrics, activity })
      await withLanguage('ar')
      render(<DocumentsAddedChart />)

      expect(screen.getByRole('img', { name: 'أُضيف 1,234 مستندًا من Jan إلى Dec؛ الشهر الأكثر نشاطًا Mar' }))
        .toBeInTheDocument()
      const table = screen.getByRole('table', { name: 'المستندات المضافة شهريًا' })
      expect(table.querySelector('thead')).toHaveTextContent('الشهر')
      expect(table.querySelector('thead')).toHaveTextContent('المستندات')
      expect(screen.getByText('آخر 12 شهرًا')).toBeInTheDocument()
      expect(screen.getByText('الشهر الأكثر نشاطًا')).toBeInTheDocument()
      expect(screen.getByText('المستندات المفهرسة شهريًا، Jan – Dec')).toBeInTheDocument()
    })
  })
})
