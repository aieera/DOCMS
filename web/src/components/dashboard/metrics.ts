import type { FacetBucket } from '@/types/api'
import type { Task } from '@/api/tasks'

export interface Point { label: string; iso: string; value: number }
export interface Slice { key: string; label: string; value: number; share: number }

// timeZone: 'UTC' is required — backend buckets are midnight UTC on the
// 1st, and without it any viewer west of UTC (all of the Americas) sees
// the instant roll back to the previous local day, mislabelling every
// bar one month early.
const MONTH_LABEL = new Intl.DateTimeFormat(undefined, { month: 'short', timeZone: 'UTC' })

const monthKey = (d: Date) => `${d.getUTCFullYear()}-${d.getUTCMonth()}`

/**
 * Turns the `created_at` date_histogram facet into the last `months`
 * calendar months, ending with the month containing `now`.
 *
 * The backend pins the interval to calendar month and sets
 * `min_doc_count: 1` (search/internal/opensearch/facets.go), so a month
 * with no documents is OMITTED from the response rather than returned
 * as 0. Plotting the raw buckets therefore drew a lone dot when only one
 * month had documents, and joined distant months with a straight line
 * that hid the empty months between them. The histogram covers every
 * document the viewer can read, so an absent month genuinely means zero:
 * fill it. Buckets outside the window are dropped, which also discards
 * the Go zero-value `0001-01-01` bucket that documents indexed without a
 * `created_at` land in.
 *
 * Returns [] when no month in the window has documents, so callers can
 * show their empty state instead of a flat line at zero.
 */
export function monthSeries(
  buckets: FacetBucket[] | undefined,
  months = 12,
  now: Date = new Date(),
): Point[] {
  const counts = new Map<string, number>()
  for (const b of buckets ?? []) {
    const start = new Date(b.value)
    if (!Number.isFinite(start.getTime())) continue
    const key = monthKey(start)
    counts.set(key, (counts.get(key) ?? 0) + b.count)
  }

  const points: Point[] = []
  for (let back = months - 1; back >= 0; back--) {
    // Date.UTC rolls a negative month back into the previous year.
    const start = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - back, 1))
    points.push({
      label: MONTH_LABEL.format(start),
      iso: start.toISOString(),
      value: counts.get(monthKey(start)) ?? 0,
    })
  }
  return points.some((p) => p.value > 0) ? points : []
}

/**
 * Terms-facet buckets → labelled slices with their share of the total.
 * Share is each slice's share of ALL positive buckets, not of only the
 * `top` slices returned — dividing by the largest bucket, or by the sum
 * of just the shown slices, would inflate a small contributor's share
 * once the list is truncated (e.g. 5 documents out of 1000 rendering as
 * "20%" because it happened to be one of the top 3). Consequence: once
 * truncated, the returned shares sum to less than 1; the remainder is
 * the share held by the untruncated tail.
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
