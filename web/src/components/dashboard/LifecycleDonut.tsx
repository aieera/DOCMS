import { useTranslation } from 'react-i18next'
import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip } from 'recharts'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { ChartDataTable } from './ChartDataTable'
import { WidgetCard } from './WidgetCard'
import { lifecycleColor } from './chartTheme'
import { FACETS_UNAVAILABLE_LABEL, type Slice } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

// Compact: the figure sits in the 60px hole of an 88px ring, and a library
// can run to seven digits.
const COMPACT = new Intl.NumberFormat(undefined, { notation: 'compact' })

/**
 * The lifecycle ring on its own: 88px, no legend, no card. LibraryBreakdown
 * puts its legend beside it. The whole ring is announced as one image
 * carrying the total — the numbers per state live in the sibling
 * ChartDataTable — so the SVG underneath is hidden from assistive tech.
 * Each state keeps its colour (see `lifecycleColor`) wherever it sits in the
 * list. Colours are `var()` references, so the ring follows the theme with
 * no re-render, and a donut needs no mirroring under RTL.
 */
export function LifecycleRing({ slices, total }: { slices: Slice[]; total: number }) {
  const { t } = useTranslation('common')
  const reduced = usePrefersReducedMotion()
  return (
    <div
      className="relative h-[88px] w-[88px] shrink-0"
      role="img"
      aria-label={t('dashboard.widgets.lifecycle_alt', { count: total })}
    >
      {/* aria-hidden for the reason given in LifecycleDonut: Recharts' Sector
          is an unnamed role="img" (axe svg-img-alt). Fixed width and height
          rather than a ResponsiveContainer: the size never changes, so there
          is no measurement pass and the ring paints on the first render. */}
      <div aria-hidden="true" className="h-full w-full">
        <PieChart width={88} height={88}>
          <Pie
            data={slices}
            dataKey="value"
            nameKey="label"
            cx="50%"
            cy="50%"
            innerRadius={30}
            outerRadius={44}
            paddingAngle={2}
            stroke="none"
            isAnimationActive={!reduced}
            animationDuration={800}
            rootTabIndex={-1}
          >
            {slices.map((s) => <Cell key={s.key} fill={lifecycleColor(s.key)} />)}
          </Pie>
        </PieChart>
        <span className="pointer-events-none absolute inset-0 flex items-center justify-center text-sm font-semibold tabular-nums text-foreground">
          {COMPACT.format(total)}
        </span>
      </div>
    </div>
  )
}

/**
 * Where every indexed document sits in the lifecycle. Below `sm` the
 * donut is replaced by a stacked proportion bar — at 330px a donut's
 * labels collide and the legend is the only thing anyone reads anyway.
 */
export function LifecycleDonut({ delayIndex = 0 }: { delayIndex?: number }) {
  const { t } = useTranslation('common')
  const { lifecycle, isLoading, isError, isUnavailable, refetch } = useDashboardMetrics()
  const reduced = usePrefersReducedMotion()

  const total = lifecycle.reduce((sum, s) => sum + s.value, 0)

  // WidgetCard renders the subtitle unconditionally in its header — the
  // FAILED > LOADING > EMPTY > content precedence only governs the body.
  // `lifecycle` is `[]` both while loading and after a failed fetch, so
  // an unguarded `${total} indexed documents` would render a confident,
  // lying "0 indexed documents" under the skeleton or the error card
  // (the exact hazard class fixed in KpiStrip's fix round 1). The same
  // goes for unavailable facets (I2): `total` is 0 there too, and false.
  const subtitle = isLoading || isError || isUnavailable
    ? undefined
    : t('dashboard.widgets.lifecycle_subtitle', { count: total })

  return (
    <WidgetCard
      title={t('dashboard.widgets.lifecycle_title')}
      subtitle={subtitle}
      isLoading={isLoading}
      isError={isError}
      isEmpty={isUnavailable || lifecycle.length === 0}
      emptyLabel={isUnavailable ? FACETS_UNAVAILABLE_LABEL : t('dashboard.widgets.lifecycle_empty')}
      onRetry={refetch}
      delayIndex={delayIndex}
      // I4: the loaded body with four states, measured live — sm+: 190px
      // donut + 16px gap + 88px legend; below sm the 12px stacked bar
      // replaces the donut. More states can still grow it.
      bodyClassName="min-h-[116px] sm:min-h-[294px]"
    >
      <div className="hidden h-[190px] w-full sm:block" role="img" aria-label={t('dashboard.widgets.lifecycle_alt', { count: total })}>
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
                {lifecycle.map((s) => <Cell key={s.key} fill={lifecycleColor(s.key)} />)}
              </Pie>
              <Tooltip
                contentStyle={{
                  background: 'var(--popover)',
                  border: '1px solid var(--border)',
                  borderRadius: '12px',
                  color: 'var(--popover-foreground)',
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
          {lifecycle.map((s) => (
            <div key={s.key} style={{ width: `${s.share * 100}%`, background: lifecycleColor(s.key) }} />
          ))}
        </div>
      </div>

      <ul className="mt-4 flex flex-col gap-2">
        {lifecycle.map((s) => (
          <li key={s.key} className="flex items-center gap-2 text-xs">
            <span
              className="h-2.5 w-2.5 shrink-0 rounded-full"
              style={{ background: lifecycleColor(s.key) }}
              aria-hidden
            />
            <span className="min-w-0 flex-1 truncate text-foreground">{s.label}</span>
            <span className="shrink-0 font-semibold tabular-nums text-foreground">{s.value.toLocaleString()}</span>
          </li>
        ))}
      </ul>

      <ChartDataTable
        caption={t('dashboard.widgets.lifecycle_caption')}
        columns={[t('dashboard.widgets.col_state'), t('dashboard.widgets.col_documents')]}
        rows={lifecycle.map((s) => ({ key: s.key, label: s.label, value: s.value }))}
      />
    </WidgetCard>
  )
}
