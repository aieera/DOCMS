import { useTranslation } from 'react-i18next'
import { Cell, Pie, PieChart } from 'recharts'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { lifecycleColor } from './chartTheme'
import type { Slice } from './metrics'

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
      {/* aria-hidden because Recharts' Sector hardcodes an unnamed
          role="img" on every slice (es6/shape/Sector.js; axe svg-img-alt),
          and the numbers live in the sibling ChartDataTable. Fixed width and height
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
