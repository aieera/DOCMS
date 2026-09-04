import { forwardRef, type HTMLAttributes } from 'react'
import { cn } from '@/lib/cn'

export interface VerticalBarDatum {
  /** Short axis label, e.g. "M" for Monday */
  label: string
  /** 0-100, where 100 = full bar height */
  value: number
  /** Render this bar in the theme accent (--primary) color */
  accent?: boolean
  /** Optional floating tooltip text above the bar */
  tooltip?: string
}

export interface VerticalBarChartProps extends HTMLAttributes<HTMLDivElement> {
  data: VerticalBarDatum[]
  /** axis-label color override (default uses the muted-foreground token) */
  labelClassName?: string
  /** className for each bar — useful if the host overrides the default fill */
  barClassName?: string
}

// Vertical bar chart with optional accent bar + floating tooltip
// (the "weekly Work Time" chart from the reference). No external
// chart library — Recharts is overkill for a 7-bar display and we'd
// fight its tooltip styling.

export const VerticalBarChart = forwardRef<HTMLDivElement, VerticalBarChartProps>(
  ({ className, data, labelClassName, barClassName, ...props }, ref) => (
    <div
      ref={ref}
      className={cn('relative flex flex-1 flex-col gap-1.5', className)}
      {...props}
    >
      <div
        className="grid flex-1 items-end gap-2"
        style={{ gridTemplateColumns: `repeat(${data.length}, minmax(0, 1fr))` }}
      >
        {data.map((d, i) => (
          <div key={i} className="relative flex h-full items-end">
            <div
              className={cn(
                'relative w-full rounded-full',
                d.accent ? 'bg-primary shadow-neu-sm' : 'bg-muted',
                barClassName,
              )}
              style={{ height: `${Math.max(d.value, 4)}%`, minHeight: 8 }}
              role="img"
              aria-label={d.tooltip ? `${d.label}: ${d.tooltip}` : `${d.label}: ${d.value}%`}
            >
              {d.tooltip && (
                <span
                  className={cn(
                    'absolute start-1/2 top-[-34px] -translate-x-1/2 whitespace-nowrap',
                    'rounded-full bg-popover px-2.5 py-1 font-mono text-[10.5px] font-semibold',
                    'tracking-[0.02em] text-popover-foreground shadow-neu-sm',
                    'after:absolute after:bottom-[-4px] after:start-1/2 after:h-2 after:w-2',
                    'after:-translate-x-1/2 after:rotate-45 after:bg-popover',
                  )}
                >
                  {d.tooltip}
                </span>
              )}
            </div>
          </div>
        ))}
      </div>
      <div
        className="grid mt-1.5"
        style={{ gridTemplateColumns: `repeat(${data.length}, minmax(0, 1fr))` }}
      >
        {data.map((d, i) => (
          <span
            key={i}
            className={cn(
              'text-center text-[11px] font-medium text-muted-foreground',
              labelClassName,
            )}
          >
            {d.label}
          </span>
        ))}
      </div>
    </div>
  ),
)
VerticalBarChart.displayName = 'VerticalBarChart'
