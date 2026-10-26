import { WidgetCard } from './WidgetCard'
import { OTHER_SEGMENT_COLOR, useChartTheme } from './chartTheme'
import { FACETS_UNAVAILABLE_LABEL, withOther, type Slice } from './metrics'

interface CommonProps {
  slices: Slice[]
  unit: string
}

interface BarsProps extends CommonProps {
  variant?: 'bars'
  title: string
  subtitle?: string
  isLoading: boolean
  isError: boolean
  /** The facets were not returned at all (I2): not the same as empty. */
  isUnavailable?: boolean
  onRetry: () => void
  emptyLabel: string
  delayIndex?: number
}

interface StackedProps extends CommonProps {
  variant: 'stacked'
  /** Label for the segment that folds every slice past the fifth. */
  otherLabel: string
}

/** A share as a percentage, without float noise (0.3 * 100 is 30.000000000000004). */
const percent = (share: number) => Number((share * 100).toFixed(2))

/**
 * One 8px bar split by share, then a list with a colour key, count and %.
 * No card frame, title or load/error handling: it lives inside a card whose
 * owner has already decided there is something to show. Colours go by rank
 * (`chart-1…5`); anything past the palette is one neutral "Other" segment,
 * so a sixth type never reuses a hue. The bar repeats what the list says, so
 * it is hidden from assistive tech.
 */
function StackedBreakdown({ slices, unit, otherLabel }: CommonProps & { otherLabel: string }) {
  const { series } = useChartTheme()
  const rows = withOther(slices, series.length, otherLabel)
  const colorAt = (i: number) => (i < series.length ? series[i] : OTHER_SEGMENT_COLOR)
  return (
    <>
      <div
        data-testid="stacked-bar"
        aria-hidden="true"
        className="flex h-2 w-full gap-0.5 overflow-hidden rounded-full bg-muted"
      >
        {rows.map((s, i) => (
          <div
            key={s.key}
            className="h-full min-w-[2px]"
            style={{ width: `${percent(s.share)}%`, backgroundColor: colorAt(i) }}
          />
        ))}
      </div>
      <ul className="mt-4 flex flex-col gap-2.5">
        {rows.map((s, i) => (
          <li key={s.key} className="flex items-center gap-2 text-xs">
            <span
              className="h-2.5 w-2.5 shrink-0 rounded-full"
              style={{ backgroundColor: colorAt(i) }}
              aria-hidden
            />
            <span className="min-w-0 flex-1 truncate text-foreground">{s.label}</span>
            <span className="shrink-0 tabular-nums text-muted-foreground">
              <span className="font-semibold text-foreground">{s.value.toLocaleString()}</span>
              <span className="sr-only"> {unit}, </span>
              {' '}
              {Math.round(s.share * 100)}%
            </span>
          </li>
        ))}
      </ul>
    </>
  )
}

/**
 * Horizontal proportion bars. Plain divs rather than a chart library:
 * the bars carry their own label and figure, so recharts would add a
 * measurement pass and a render tree for something a flex row already
 * does — and the scaleX transform keeps the growth animation on the
 * compositor.
 *
 * `slices` may legitimately sum to less than 100% of `share` — Slice.share
 * is each item's share of the document total, computed before the list is
 * truncated to the top N (see toSlices in metrics.ts). That's intentional;
 * this component does not renormalise, so the bars read as "this many of
 * the total" rather than inflating a small contributor's share.
 *
 * `variant="stacked"` is the compact form for a card that holds several
 * panels: no frame, no state handling (the caller owns both), one stacked
 * bar and a list. See StackedBreakdown.
 */
export function BreakdownBars(props: BarsProps | StackedProps) {
  if (props.variant === 'stacked') {
    return <StackedBreakdown slices={props.slices} unit={props.unit} otherLabel={props.otherLabel} />
  }
  const {
    title, subtitle, slices, isLoading, isError, isUnavailable = false, onRetry, emptyLabel, unit, delayIndex = 0,
  } = props
  return (
    <WidgetCard
      title={title}
      subtitle={subtitle}
      isLoading={isLoading}
      isError={isError}
      isEmpty={isUnavailable || slices.length === 0}
      emptyLabel={isUnavailable ? FACETS_UNAVAILABLE_LABEL : emptyLabel}
      onRetry={onRetry}
      delayIndex={delayIndex}
      // I4: five bars (toSlices top=5), measured live.
      bodyClassName="min-h-[224px]"
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
