import { cloneElement, isValidElement, type ReactElement, type ReactNode } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'

import * as metricsHook from '../useDashboardMetrics'
import { LifecycleDonut } from '../LifecycleDonut'

// L123: belt and braces for the LifecycleDonut axe fix (c76599b1). The e2e
// axe scan of `/` is the primary guard; this one runs in every unit pass.
//
// jsdom has no layout, so recharts' ResponsiveContainer measures 0x0 and
// renders no SVG at all — which is why the widget tests could not see the
// violation. Hand the chart fixed dimensions instead, in this file only.
vi.mock('recharts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('recharts')>()
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children: ReactNode }) => (
      <div style={{ width: 400, height: 190 }}>
        {isValidElement(children)
          ? cloneElement(children as ReactElement<{ width: number; height: number }>, { width: 400, height: 190 })
          : children}
      </div>
    ),
  }
})

// Reduced motion, so the Pie draws its final sectors on the first render
// rather than animating in from nothing.
function preferReducedMotion() {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: true, media: query, onchange: null,
    addEventListener: vi.fn(), removeEventListener: vi.fn(),
    addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn(),
  }))
}

describe('LifecycleDonut — recharts internals stay out of the a11y tree (L123)', () => {
  beforeEach(() => {
    preferReducedMotion()
    vi.spyOn(metricsHook, 'useDashboardMetrics').mockReturnValue({
      activity: [], fileTypes: [], contributors: [],
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
    const { container } = render(<LifecycleDonut />)
    expect(container.querySelectorAll('.recharts-pie-sector path').length).toBe(3)
  })

  it('exposes no unnamed role="img" outside an aria-hidden subtree (axe svg-img-alt)', () => {
    const { container } = render(<LifecycleDonut />)
    const unnamed = [...container.querySelectorAll('[role="img"]')].filter(
      (el) => !el.getAttribute('aria-label') && !el.getAttribute('aria-labelledby')
        && !el.closest('[aria-hidden="true"]'),
    )
    expect(unnamed).toEqual([])
  })

  it('leaves no tab stop inside the pie (axe aria-hidden-focus)', () => {
    const { container } = render(<LifecycleDonut />)
    const pie = container.querySelector('.recharts-pie')
    expect(pie).not.toBeNull()
    expect(pie!.getAttribute('tabindex')).not.toBe('0')
    expect(pie!.querySelectorAll('[tabindex="0"]')).toHaveLength(0)
  })

  it('keeps the named composite role="img" outside the hidden subtree', () => {
    render(<LifecycleDonut />)
    const chart = screen.getByRole('img', { name: 'Documents by lifecycle state, 50 in total' })
    expect(chart.closest('[aria-hidden="true"]')).toBeNull()
    expect(chart.querySelector('[aria-hidden="true"] .recharts-pie')).not.toBeNull()
  })
})
