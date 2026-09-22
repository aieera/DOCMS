import { WidgetCard } from './WidgetCard'
import type { Slice } from './metrics'

/**
 * Horizontal proportion bars. Plain divs rather than a chart library:
 * the bars carry their own label and figure, so recharts would add a
 * measurement pass and a render tree for something a flex row already
 * does — and the scaleX transform keeps the growth animation on the
 * compositor.
 *
 * `slices` may legitimately sum to less than 100% of `share` — Slice.share
 * is each item's share of ALL positive buckets, computed before the list
 * is truncated to the top N (see metrics.ts). That's intentional; this
 * component does not renormalise, so the bars read as "this many of the
 * total" rather than inflating a small contributor's share once truncated.
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
                <span className="font-semibold text-foreground">{s.value.toLocaleString()}</span>
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
