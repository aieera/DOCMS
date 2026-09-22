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
    ], 2, new Date('2026-03-15T00:00:00.000Z'))
    expect(out.map((p) => p.value)).toEqual([2, 3])
  })

  it('labels buckets by short month name', () => {
    const [p] = monthSeries(
      [{ value: '2026-01-15T00:00:00.000Z', count: 7 }],
      1,
      new Date('2026-01-20T00:00:00.000Z'),
    )
    expect(p.label).toMatch(/Jan/)
    expect(p.value).toBe(7)
  })

  // The backend histogram uses min_doc_count: 1, so a month with no
  // documents is OMITTED rather than returned as 0. Plotting the raw
  // buckets drew a single dot when only one month had documents, and
  // would join two distant months with a straight line that hides the
  // empty months between them.
  it('fills months absent from the histogram with zero across the whole window', () => {
    const out = monthSeries(
      [{ value: '2026-09-01T00:00:00.000Z', count: 4 }],
      12,
      new Date('2026-09-22T00:00:00.000Z'),
    )
    expect(out).toHaveLength(12)
    expect(out.map((p) => p.value)).toEqual([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4])
    expect(out[0].label).toBe('Oct')
    expect(out[11].label).toBe('Sep')
  })

  it('keeps a gap as zeros rather than joining distant months directly', () => {
    const out = monthSeries([
      { value: '2026-01-01T00:00:00.000Z', count: 2 },
      { value: '2026-09-01T00:00:00.000Z', count: 5 },
    ], 12, new Date('2026-09-22T00:00:00.000Z'))
    // Oct 2025 .. Sep 2026: Jan is index 3, Sep is index 11.
    expect(out[3].value).toBe(2)
    expect(out.slice(4, 11).every((p) => p.value === 0)).toBe(true)
    expect(out[11].value).toBe(5)
  })

  it('drops buckets outside the window, including the Go zero-value year-1 date', () => {
    const out = monthSeries([
      { value: '0001-01-01T00:00:00.000Z', count: 2 },
      { value: '2026-09-01T00:00:00.000Z', count: 4 },
    ], 12, new Date('2026-09-22T00:00:00.000Z'))
    expect(out.reduce((sum, p) => sum + p.value, 0)).toBe(4)
  })

  // [] means "none in THIS window", not "none ever": a tenant whose
  // archive predates the window still has documents. The ActivityChart
  // empty copy must therefore say "in the last 12 months" (I3), never "yet".
  it('returns [] when no month in the window has documents, even if older months do', () => {
    expect(monthSeries(
      [{ value: '2024-01-01T00:00:00.000Z', count: 9 }],
      12,
      new Date('2026-09-22T00:00:00.000Z'),
    )).toEqual([])
  })

  it('skips buckets whose value is not a parseable date', () => {
    expect(monthSeries([{ value: 'not-a-date', count: 5 }])).toEqual([])
  })

  it('labels a month-boundary UTC bucket by its UTC month, not the local one', () => {
    // Backend buckets are midnight UTC on the 1st. West of UTC, formatting
    // that instant in the local zone rolls it back to the previous month
    // (e.g. America/New_York reads 2026-01-01T00:00:00.000Z as Dec 31
    // 19:00 local) which mislabels the bar. A mid-month timestamp can't
    // catch this: it takes a month-boundary instant to cross the line.
    const iso = '2026-01-01T00:00:00.000Z'
    const [p] = monthSeries([{ value: iso, count: 1 }], 1, new Date('2026-01-20T00:00:00.000Z'))
    const expected = new Intl.DateTimeFormat(undefined, { month: 'short', timeZone: 'UTC' }).format(
      new Date(iso),
    )
    expect(p.label).toBe(expected)
    expect(p.label).toBe('Jan')
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

  // I1: doc_type and author are size-20 terms aggregations, so the sum of
  // the RETURNED buckets undercounts once a tenant has more than 20 values.
  // The caller passes the true document total as the denominator.
  it('divides by an explicit denominator when one is given', () => {
    const out = toSlices([
      { value: 'a', count: 30 },
      { value: 'b', count: 10 },
    ], (v) => v, 5, 200)
    expect(out[0].share).toBeCloseTo(30 / 200, 6)
    expect(out[1].share).toBeCloseTo(10 / 200, 6)
  })

  it('keeps share against the grand total when truncated, so shown shares sum to less than 1', () => {
    const buckets = [
      { value: 'a', count: 50 },
      { value: 'b', count: 40 },
      { value: 'c', count: 30 },
      { value: 'd', count: 20 },
      { value: 'e', count: 10 },
      { value: 'f', count: 5 },
      { value: 'g', count: 1 },
    ]
    const grandTotal = buckets.reduce((sum, b) => sum + b.count, 0)
    const out = toSlices(buckets, (v) => v, 3)
    expect(out.map((s) => s.key)).toEqual(['a', 'b', 'c'])
    for (const s of out) {
      expect(s.share).toBeCloseTo(s.value / grandTotal)
    }
    const shownTotal = out.reduce((sum, s) => sum + s.share, 0)
    expect(shownTotal).toBeLessThan(1)
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
    expect(trendSummary([])).toBe('No documents added in the last 12 months.')
  })

  // M4: after zero-fill both endpoints are usually 0, so a first-vs-last
  // summary read "flat from 0 in Oct to 0 in Sep" for a series with 50
  // documents in March. Describe the total, the peak and the latest month.
  it('names the total, the peak month and the latest month of a zero-filled series', () => {
    const months = ['Oct', 'Nov', 'Dec', 'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep']
    const points = months.map((label, i) => ({ label, iso: `m${i}`, value: label === 'Mar' ? 50 : 0 }))
    const s = trendSummary(points)
    expect(s).not.toMatch(/flat|rising|falling/i)
    expect(s).toContain('50 documents added in the last 12 months')
    expect(s).toContain('most in Mar (50)')
    expect(s).toContain('0 in Sep, the latest month')
  })

  it('reports the first peak when two months tie', () => {
    const s = trendSummary([
      { label: 'Jan', iso: '2026-01-01', value: 12 },
      { label: 'Feb', iso: '2026-02-01', value: 48 },
      { label: 'Mar', iso: '2026-03-01', value: 48 },
    ])
    expect(s).toContain('108 documents added in the last 3 months')
    expect(s).toContain('most in Feb (48)')
    expect(s).toContain('48 in Mar, the latest month')
  })
})
