import { formatDateTime, formatRelativeTime } from '@/lib/formatters'

// One date presentation for the whole app: relative text ("2 weeks
// ago") with the absolute value on hover, in a <time> element so it
// is machine-readable.
//
// Before this, the same screen could show three formats — the document
// header used "Jul 15, 2026 12:21 AM", Compliance used the raw
// "7/15/2026, 12:22:27 AM" from toLocaleString(), and History used a
// fourth. Anything user-facing should render through here (or
// formatDateTime when an absolute value is genuinely wanted).
export function TimeAgo({
  date,
  className,
  absolute = false,
}: {
  date: string | Date | null | undefined
  className?: string
  /** Show the absolute timestamp as the visible text (relative moves
   *  to the tooltip). Use for audit/legal surfaces where the exact
   *  moment is the point. */
  absolute?: boolean
}) {
  if (!date) return <span className={className}>—</span>
  const abs = formatDateTime(date)
  const rel = formatRelativeTime(date)
  const iso = typeof date === 'string' ? date : date.toISOString()
  return (
    <time dateTime={iso} title={absolute ? rel : abs} className={className}>
      {absolute ? abs : rel}
    </time>
  )
}
