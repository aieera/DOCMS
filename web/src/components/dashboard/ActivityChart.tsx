import { useId } from 'react'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { ChartDataTable } from './ChartDataTable'
import { WidgetCard } from './WidgetCard'
import { useChartTheme } from './chartTheme'
import { trendSummary } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

/**
 * Documents added per month. The interval is months, not days, because
 * the `created_at` facet is a date_histogram pinned to calendar month
 * server-side (search/internal/opensearch/facets.go). A daily series
 * would need a backend change, which this phase does not make — so the
 * axis says what the data actually is.
 */
export function ActivityChart({ delayIndex = 0, className }: { delayIndex?: number; className?: string }) {
  const { activity, isLoading, isError, refetch } = useDashboardMetrics()
  const theme = useChartTheme()
  const reduced = usePrefersReducedMotion()
  const gradientId = useId()

  return (
    <WidgetCard
      title="Documents added"
      subtitle="Last 12 months, of indexed documents"
      isLoading={isLoading}
      isError={isError}
      isEmpty={activity.length === 0}
      emptyLabel="No documents added yet"
      onRetry={refetch}
      delayIndex={delayIndex}
      className={className}
    >
      <div className="h-[220px] w-full" role="img" aria-label={trendSummary(activity)}>
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={activity} margin={{ top: 4, right: 4, bottom: 0, left: -20 }}>
            <defs>
              <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={theme.area} stopOpacity={0.35} />
                <stop offset="100%" stopColor={theme.area} stopOpacity={0.02} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke={theme.grid} strokeDasharray="3 3" vertical={false} />
            <XAxis
              dataKey="label"
              tick={{ fontSize: 11, fill: theme.axis }}
              tickLine={false}
              axisLine={false}
            />
            <YAxis
              tick={{ fontSize: 11, fill: theme.axis }}
              tickLine={false}
              axisLine={false}
              allowDecimals={false}
              width={44}
            />
            <Tooltip
              cursor={{ stroke: theme.grid }}
              contentStyle={{
                background: 'hsl(var(--popover))',
                border: '1px solid hsl(var(--border))',
                borderRadius: '12px',
                color: 'hsl(var(--popover-foreground))',
                fontSize: '12px',
              }}
              labelStyle={{ color: 'hsl(var(--muted-foreground))' }}
              formatter={(value: number) => [value, 'Documents']}
            />
            <Area
              type="monotone"
              dataKey="value"
              stroke={theme.area}
              strokeWidth={2}
              fill={`url(#${gradientId})`}
              isAnimationActive={!reduced}
              animationDuration={1200}
              animationEasing="ease-out"
              dot={false}
              activeDot={{ r: 4, strokeWidth: 0 }}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>

      <ChartDataTable
        caption="Documents added per month"
        columns={['Month', 'Documents']}
        rows={activity.map((p) => ({ key: p.iso, label: p.label, value: p.value }))}
      />
    </WidgetCard>
  )
}
