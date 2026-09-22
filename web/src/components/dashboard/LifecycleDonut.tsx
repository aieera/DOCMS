import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip } from 'recharts'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { ChartDataTable } from './ChartDataTable'
import { WidgetCard } from './WidgetCard'
import { useChartTheme } from './chartTheme'
import { FACETS_UNAVAILABLE_LABEL } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

/**
 * Where every indexed document sits in the lifecycle. Below `sm` the
 * donut is replaced by a stacked proportion bar — at 330px a donut's
 * labels collide and the legend is the only thing anyone reads anyway.
 */
export function LifecycleDonut({ delayIndex = 0 }: { delayIndex?: number }) {
  const { lifecycle, isLoading, isError, isUnavailable, refetch } = useDashboardMetrics()
  const theme = useChartTheme()
  const reduced = usePrefersReducedMotion()

  const total = lifecycle.reduce((sum, s) => sum + s.value, 0)
  const color = (i: number) => theme.series[i % theme.series.length]

  // WidgetCard renders the subtitle unconditionally in its header — the
  // FAILED > LOADING > EMPTY > content precedence only governs the body.
  // `lifecycle` is `[]` both while loading and after a failed fetch, so
  // an unguarded `${total} indexed documents` would render a confident,
  // lying "0 indexed documents" under the skeleton or the error card
  // (the exact hazard class fixed in KpiStrip's fix round 1). The same
  // goes for unavailable facets (I2): `total` is 0 there too, and false.
  const subtitle = isLoading || isError || isUnavailable ? undefined : `${total.toLocaleString()} indexed documents`

  return (
    <WidgetCard
      title="Lifecycle"
      subtitle={subtitle}
      isLoading={isLoading}
      isError={isError}
      isEmpty={isUnavailable || lifecycle.length === 0}
      emptyLabel={isUnavailable ? FACETS_UNAVAILABLE_LABEL : 'Nothing indexed yet'}
      onRetry={refetch}
      delayIndex={delayIndex}
    >
      <div className="hidden h-[190px] w-full sm:block" role="img" aria-label={`Documents by lifecycle state, ${total.toLocaleString()} in total`}>
        {/*
          aria-hidden on the recharts subtree: Recharts' Pie renders each
          slice via `Sector`, which hardcodes `role="img"` on its `<path>`
          with no accessible name (recharts es6/shape/Sector.js) — axe's
          svg-img-alt then fires per slice. The parent div above already
          carries one composite role="img" + aria-label for the whole
          chart (numbers live in the ChartDataTable below), so the
          interactive SVG itself is redundant to assistive tech and is
          hidden rather than given per-slice labels it can't use.
        */}
        <div aria-hidden="true" className="h-full w-full">
          <ResponsiveContainer width="100%" height="100%">
            <PieChart>
              <Pie
                data={lifecycle}
                dataKey="value"
                nameKey="label"
                cx="50%"
                cy="50%"
                innerRadius={52}
                outerRadius={78}
                paddingAngle={2}
                stroke="none"
                isAnimationActive={!reduced}
                animationDuration={800}
                rootTabIndex={-1}
              >
                {lifecycle.map((s, i) => <Cell key={s.key} fill={color(i)} />)}
              </Pie>
              <Tooltip
                contentStyle={{
                  background: 'hsl(var(--popover))',
                  border: '1px solid hsl(var(--border))',
                  borderRadius: '12px',
                  color: 'hsl(var(--popover-foreground))',
                  fontSize: '12px',
                }}
              />
            </PieChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* Below sm: one stacked bar carrying the same proportions. */}
      <div className="sm:hidden" aria-hidden>
        <div className="flex h-3 w-full overflow-hidden rounded-full bg-muted shadow-neu-inset">
          {lifecycle.map((s, i) => (
            <div key={s.key} style={{ width: `${s.share * 100}%`, background: color(i) }} />
          ))}
        </div>
      </div>

      <ul className="mt-4 flex flex-col gap-2">
        {lifecycle.map((s, i) => (
          <li key={s.key} className="flex items-center gap-2 text-xs">
            <span
              className="h-2.5 w-2.5 shrink-0 rounded-full"
              style={{ background: color(i) }}
              aria-hidden
            />
            <span className="min-w-0 flex-1 truncate text-foreground">{s.label}</span>
            <span className="shrink-0 font-semibold tabular-nums text-foreground">{s.value.toLocaleString()}</span>
          </li>
        ))}
      </ul>

      <ChartDataTable
        caption="Documents by lifecycle state"
        columns={['State', 'Documents']}
        rows={lifecycle.map((s) => ({ key: s.key, label: s.label, value: s.value }))}
      />
    </WidgetCard>
  )
}
