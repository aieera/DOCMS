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
