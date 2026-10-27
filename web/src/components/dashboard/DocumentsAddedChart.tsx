import { useTranslation } from 'react-i18next'
import { Bar, BarChart, CartesianGrid, LabelList, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import type { LabelProps } from 'recharts'

import { useDirection } from '@/hooks/useDirection'
import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { ChartDataTable } from './ChartDataTable'
import { WidgetCard } from './WidgetCard'
import { useChartTheme } from './chartTheme'
import { FACETS_UNAVAILABLE_KEY } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

// The loaded body is a stats row plus the chart. The loading skeleton reserves
// the same height, so a slow response does not shove the page down; only the
// skeleton does, so the empty, unavailable and error states size to their
// content instead of keeping a chart-sized hole:
//   stats  : 16px caption + 2px gap + 28px figure = 46px, + 16px below = 62px
//   chart  : 220px
//   padding: WidgetCard's body is `pb-5` = 20px, and `bodyClassName` lands on
//            that same border-box element, so the min-height includes it.
//   62 + 220 + 20 = 302. Tailwind only emits literal class names, so BODY_CLASS
//   cannot be built from these numbers.
const BODY_CLASS = 'min-h-[302px]'

// "1.2K" rather than "1,200": a bar label sits in a band ~25px wide on a phone.
const COMPACT = new Intl.NumberFormat(undefined, { notation: 'compact' })

/** Value above a bar. Nothing is drawn for a month with no documents. */
function BarValueLabel({ x, y, width, value }: LabelProps) {
  const n = Number(value)
  if (!(n > 0)) return null
  return (
    <text
      x={Number(x) + Number(width) / 2}
      y={Number(y) - 6}
      textAnchor="middle"
      fontSize={12}
      fontWeight={500}
      fill="var(--foreground)"
    >
      {COMPACT.format(n)}
    </text>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="truncate text-caption text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate text-xl leading-7 font-semibold tracking-[-0.01em] text-foreground tabular-nums">
        {value}
      </dd>
    </div>
  )
}

/**
 * Documents added per month, as bars. The interval is months, not days,
 * because the `created_at` facet is a date_histogram pinned to calendar
 * month server-side (search/internal/opensearch/facets.go) — so the axis
 * says what the data actually is.
 */
export function DocumentsAddedChart({ className }: { className?: string }) {
  const { t } = useTranslation('common')
  const { activity, isLoading, isError, isUnavailable, refetch } = useDashboardMetrics()
  const theme = useChartTheme()
  const reduced = usePrefersReducedMotion()
  const dir = useDirection()

  // `monthSeries` already returns [] for an all-zero window; checking the
  // values here as well keeps this component correct for any series it is
  // handed — a row of zero-height bars is not a chart.
  const hasData = !isUnavailable && activity.some((p) => p.value > 0)

  const total = activity.reduce((sum, p) => sum + p.value, 0)
  // Strict `>` keeps the first month on a tie. The JSX below is built even
  // when WidgetCard shows its empty state, so there must be no `activity[0]`
  // dereference without data.
  const busiest = hasData ? activity.reduce((best, p) => (p.value > best.value ? p : best)) : undefined

  // WidgetCard only withholds the subtitle while loading or failed; guard the
  // empty and unavailable cases too, where there is no first/last month.
  const from = hasData ? activity[0].label : ''
  const to = hasData ? activity[activity.length - 1].label : ''
  const subtitle = hasData ? t('dashboard.widgets.activity_range', { from, to }) : undefined
  // Built here, not with `trendSummary`, so the sentence is localised (that
  // helper is English-only). `count` is the number i18next picks the plural
  // form from; `total` is the same number formatted for display.
  const summary = hasData
    ? t('dashboard.widgets.activity_aria', {
      count: total, total: total.toLocaleString(), from, to, busiest: busiest?.label,
    })
    : undefined

  return (
    <WidgetCard
      title={t('dashboard.widgets.activity_title')}
      subtitle={subtitle}
      isLoading={isLoading}
      isError={isError}
      isEmpty={!hasData}
      // I3: the window can be empty while older documents exist, so the
      // copy names the window — never "yet".
      emptyLabel={isUnavailable ? t(FACETS_UNAVAILABLE_KEY) : t('dashboard.widgets.activity_empty')}
      onRetry={refetch}
      className={className}
      bodyClassName={isLoading ? BODY_CLASS : undefined}
    >
      <dl className="mb-4 flex gap-x-8">
        <Stat label={t('dashboard.widgets.last_12_months')} value={total.toLocaleString()} />
        <Stat label={t('dashboard.widgets.busiest_month')} value={busiest?.label ?? ''} />
      </dl>

      <div className="h-[220px] w-full" role="img" aria-label={summary}>
        <ResponsiveContainer width="100%" height="100%">
          {/* top: 20 leaves room for the value label above the tallest bar.
              Under RTL the months run right to left and the value axis moves
              to the right edge (ADR 0108); the bars' value labels are placed
              from each bar's own x/width, so they follow without extra work. */}
          <BarChart data={activity} margin={{ top: 20, right: 0, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={theme.grid} strokeDasharray="4 4" vertical={false} />
            <XAxis
              dataKey="label"
              reversed={dir === 'rtl'}
              tick={{ fontSize: 12, fill: theme.axis }}
              tickLine={false}
              axisLine={false}
            />
            <YAxis
              orientation={dir === 'rtl' ? 'right' : 'left'}
              tick={{ fontSize: 12, fill: theme.axis }}
              tickLine={false}
              axisLine={false}
              allowDecimals={false}
              tickFormatter={(v: number) => COMPACT.format(v)}
              width={36}
            />
            <Tooltip
              cursor={{ fill: 'var(--muted)', opacity: 0.6 }}
              contentStyle={{
                background: 'var(--popover)',
                border: '1px solid var(--border)',
                borderRadius: '12px',
                color: 'var(--popover-foreground)',
                fontSize: '12px',
              }}
              labelStyle={{ color: 'var(--muted-foreground)' }}
              formatter={(value: number) => [value.toLocaleString(), t('dashboard.widgets.col_documents')]}
            />
            <Bar
              dataKey="value"
              fill={theme.series[0]}
              radius={[6, 6, 2, 2]}
              maxBarSize={40}
              isAnimationActive={!reduced}
              animationDuration={800}
              animationEasing="ease-out"
            >
              <LabelList dataKey="value" position="top" content={BarValueLabel} />
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </div>

      <ChartDataTable
        caption={t('dashboard.widgets.activity_table_caption')}
        columns={[t('dashboard.widgets.col_month'), t('dashboard.widgets.col_documents')]}
        rows={activity.map((p) => ({ key: p.iso, label: p.label, value: p.value }))}
      />
    </WidgetCard>
  )
}
