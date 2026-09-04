import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/cn'

// Slim horizontal percentage bar with diagonal hatched empty fill
// (the "Interviews 15% / Hired 15% / Project time 60%" row from the
// reference). The empty portion uses a stroked SVG pattern so the
// bar reads as "in progress" even at low values.

const fillVariants = cva(
  'absolute inset-0 flex items-center rounded-full font-mono text-[11px] font-semibold tracking-[0.03em]',
  {
    variants: {
      variant: {
        dark: 'bg-foreground text-background px-[14px] shadow-neu-sm',
        accent: 'bg-primary text-primary-foreground px-[14px] shadow-neu-sm',
        muted: 'bg-background text-muted-foreground px-[14px] shadow-neu-sm',
      },
      compact: { true: 'px-[10px] text-[10px]', false: '' },
    },
    defaultVariants: { variant: 'dark', compact: false },
  },
)

export interface MetricBarProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'children'>,
    VariantProps<typeof fillVariants> {
  label: ReactNode
  /** 0-100 */
  value: number
  /** label shown inside the fill (defaults to "{value}%") */
  fillLabel?: ReactNode
  /** show the label/value caption above the bar (default true) */
  showCaption?: boolean
}

export const MetricBar = forwardRef<HTMLDivElement, MetricBarProps>(
  ({ className, label, value, fillLabel, showCaption = true, variant, compact, ...props }, ref) => {
    // Clamp visual width so a 0% bar still shows a sliver of fill
    // (otherwise the label disappears behind the rounded edge).
    const visualWidth = Math.max(Math.min(value, 100), value > 0 ? 14 : 0)
    return (
      <div ref={ref} className={cn('flex flex-col gap-2', className)} {...props}>
        {showCaption && (
          <span className="text-[11.5px] font-medium tracking-[0.02em] text-muted-foreground">{label}</span>
        )}
        <div
          className="relative h-7 overflow-hidden rounded-full bg-muted shadow-neu-inset"
          role="progressbar"
          aria-valuenow={value}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-label={typeof label === 'string' ? label : undefined}
        >
          {/* hatched empty fill */}
          <span
            aria-hidden
            className="pointer-events-none absolute inset-0"
            style={{
              backgroundImage:
                "url(\"data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='6' height='6'><path d='M-1,1 l2,-2 M0,6 l6,-6 M5,7 l2,-2' stroke='%231A1A1A' stroke-width='0.6' opacity='0.18'/></svg>\")",
              backgroundSize: '6px 6px',
            }}
          />
          {value > 0 && (
            <span
              className={cn(fillVariants({ variant, compact: compact ?? value < 25 }))}
              style={{ width: `${visualWidth}%` }}
            >
              {fillLabel ?? `${value}%`}
            </span>
          )}
        </div>
      </div>
    )
  },
)
MetricBar.displayName = 'MetricBar'
