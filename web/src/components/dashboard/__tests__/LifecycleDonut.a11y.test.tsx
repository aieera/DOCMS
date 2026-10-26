import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import axe from 'axe-core'

import * as metricsHook from '../useDashboardMetrics'
import { LibraryBreakdown } from '../LibraryBreakdown'

// L123: belt and braces for the lifecycle donut's axe fix (c76599b1). The e2e
// axe scan of `/` is the primary guard; this one runs in every unit pass.
//
// It now runs against LibraryBreakdown, which owns the lifecycle ring. The
// ring has a fixed 88px canvas (no ResponsiveContainer), so jsdom's missing
// layout no longer hides its SVG and no recharts mock is needed.

// Reduced motion, so the Pie draws its final sectors on the first render
// rather than animating in from nothing.
function preferReducedMotion() {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: true, media: query, onchange: null,
    addEventListener: vi.fn(), removeEventListener: vi.fn(),
    addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn(),
  }))
}

describe('LibraryBreakdown — recharts internals stay out of the a11y tree (L123)', () => {
  beforeEach(() => {
    preferReducedMotion()
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      activity: [],
      fileTypes: [
        { key: 'application/pdf', label: 'PDF', value: 20, share: 0.4 },
        { key: 'application/msword', label: 'Word', value: 10, share: 0.2 },
      ],
      contributors: [
        { key: 'Alice', label: 'Alice', value: 25, share: 0.5 },
        { key: 'Bob', label: 'Bob', value: 15, share: 0.3 },
      ],
      lifecycle: [
        { key: 'active', label: 'Active', value: 30, share: 0.6 },
        { key: 'draft', label: 'Draft', value: 15, share: 0.3 },
        { key: 'archived', label: 'Archived', value: 5, share: 0.1 },
      ],
      isLoading: false, isError: false, isUnavailable: false, refetch: vi.fn(),
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('actually renders the pie sectors (otherwise the checks below prove nothing)', () => {
    const { container } = render(<LibraryBreakdown />)
    expect(container.querySelectorAll('.recharts-pie-sector path').length).toBe(3)
  })

  it('exposes no unnamed role="img" outside an aria-hidden subtree (axe svg-img-alt)', () => {
    const { container } = render(<LibraryBreakdown />)
    const unnamed = [...container.querySelectorAll('[role="img"]')].filter(
      (el) => !el.getAttribute('aria-label') && !el.getAttribute('aria-labelledby')
        && !el.closest('[aria-hidden="true"]'),
    )
    expect(unnamed).toEqual([])
  })

  it('leaves no tab stop inside the pie (axe aria-hidden-focus)', () => {
    const { container } = render(<LibraryBreakdown />)
    const pie = container.querySelector('.recharts-pie')
    expect(pie).not.toBeNull()
    expect(pie!.getAttribute('tabindex')).not.toBe('0')
    expect(pie!.querySelectorAll('[tabindex="0"]')).toHaveLength(0)
  })

  it('keeps the named composite role="img" outside the hidden subtree', () => {
    render(<LibraryBreakdown />)
    const chart = screen.getByRole('img', { name: 'Documents by lifecycle state, 50 in total' })
    expect(chart.closest('[aria-hidden="true"]')).toBeNull()
    expect(chart.querySelector('[aria-hidden="true"] .recharts-pie')).not.toBeNull()
  })

  // The structural checks above name the two rules this widget tripped before;
  // this one runs the whole engine over the card, so a new violation in any of
  // the three columns (an unnamed progressbar, a heading level skip, a list
  // with a non-li child) fails here rather than in the e2e scan.
  it('has no axe violations (WCAG 2.1 AA)', async () => {
    const { container } = render(<LibraryBreakdown />)
    const results = await axe.run(container, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
    })
    expect(results.violations.map((v) => `[${v.id}] ${v.help}: ${v.nodes.map((n) => n.html).join(' | ')}`)).toEqual([])
  })
})
