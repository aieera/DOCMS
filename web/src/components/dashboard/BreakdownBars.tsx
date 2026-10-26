import { useTranslation } from 'react-i18next'

import { WidgetCard } from './WidgetCard'
import { OTHER_SEGMENT_COLOR, useChartTheme } from './chartTheme'
import { FACETS_UNAVAILABLE_KEY, withOther, type Slice } from './metrics'

interface CommonProps {
  slices: Slice[]
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
  /** Label for the segment that folds everything the palette has no colour for. */
  otherLabel: string
  /**
   * The documents the whole facet holds, which `slices` (a top-N cut) may not
   * add up to. Whatever they leave out is shown as "Other", so the bar closes
   * at 100%. Omit it to fold only slices past the fifth.
   */
  total?: number
}

/** A share as a percentage, without float noise (0.3 * 100 is 30.000000000000004). */
const percent = (share: number) => Number((share * 100).toFixed(2))

/**
 * A document count and its share, e.g. "25 50%". The visible figure is just
 * the numerals; assistive tech gets a sentence built from catalogue keys
 * instead, because the noun cannot be a bare unit appended to the number
 * (Arabic has six count forms, and its list comma and percent sign differ).
 * `count_share` takes the already-pluralised phrase, so the pre-formatted
 * string is passed as its `count`: i18next only treats a numeric `count` as a
 * plural selector.
 */
export function CountShare({ value, share }: { value: number; share: number }) {
  const { t } = useTranslation('common')
  const percent = Math.round(share * 100)
  return (
    <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
      <span aria-hidden="true">
        <span className="font-semibold text-foreground">{value.toLocaleString()}</span>
        {' '}
        {percent}%
      </span>
      <span className="sr-only">
        {t('dashboard.widgets.count_share', { count: t('dashboard.widgets.doc_count', { count: value }), percent })}
      </span>
    </span>
  )
}

/**
 * One 8px bar split by share, then a list with a colour key, count and %.
 * No card frame, title or load/error handling: it lives inside a card whose
 * owner has already decided there is something to show. Colours go by rank
 * (`chart-1…5`); everything the palette has no colour for is one neutral
 * "Other" segment, so a sixth type never reuses a hue (see `withOther`). The
 * bar repeats what the list says, so it is hidden from assistive tech.
 */
function StackedBreakdown({ slices, otherLabel, total }: CommonProps & { otherLabel: string; total?: number }) {
  const { series } = useChartTheme()
  const rows = withOther(slices, series.length, otherLabel, total)
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
            <CountShare value={s.value} share={s.share} />
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
  const { t } = useTranslation('common')
  if (props.variant === 'stacked') {
    return <StackedBreakdown slices={props.slices} otherLabel={props.otherLabel} total={props.total} />
  }
  const {
    title, subtitle, slices, isLoading, isError, isUnavailable = false, onRetry, emptyLabel, delayIndex = 0,
  } = props
  return (
    <WidgetCard
      title={title}
      subtitle={subtitle}
      isLoading={isLoading}
      isError={isError}
      isEmpty={isUnavailable || slices.length === 0}
      emptyLabel={isUnavailable ? t(FACETS_UNAVAILABLE_KEY) : emptyLabel}
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
              <CountShare value={s.value} share={s.share} />
            </div>
            <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
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
