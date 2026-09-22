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

  it('labels a month-boundary UTC bucket by its UTC month, not the local one', () => {
    // Backend buckets are midnight UTC on the 1st. West of UTC, formatting
    // that instant in the local zone rolls it back to the previous month
    // (e.g. America/New_York reads 2026-01-01T00:00:00.000Z as Dec 31
    // 19:00 local) which mislabels the bar. A mid-month timestamp can't
    // catch this: it takes a month-boundary instant to cross the line.
    const iso = '2026-01-01T00:00:00.000Z'
    const [p] = monthSeries([{ value: iso, count: 1 }])
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
