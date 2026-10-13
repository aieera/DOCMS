import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/cn'

export interface Segment {
  /** Label shown inside the segment (e.g. "30%") */
  label: ReactNode
  /** Visual variant — "accent" = mustard, "dark" = charcoal, "empty" = hatched */
  variant?: 'accent' | 'dark' | 'empty'
}

export interface SegmentedProgressProps extends HTMLAttributes<HTMLDivElement> {
  segments: Segment[]
  /** Caption rendered under the bars (e.g. "task") */
  caption?: ReactNode
}

// Three-segment horizontal pill row from the Onboarding card. Each
// segment is equal-width by default; vary `gridTemplateColumns` via
// the `style` prop on the wrapper if you need weighted segments.

export const SegmentedProgress = forwardRef<HTMLDivElement, SegmentedProgressProps>(
  ({ className, segments, caption, ...props }, ref) => (
    <div ref={ref} className={cn('flex flex-col gap-2.5', className)} {...props}>
      <div
        className="grid gap-1.5"
        style={{ gridTemplateColumns: `repeat(${segments.length}, minmax(0, 1fr))` }}
      >
        {segments.map((s, i) => {
          const variant = s.variant ?? 'empty'
          return (
            <div
              key={i}
              className={cn(
                'relative flex h-[38px] items-center overflow-hidden rounded-[14px] px-3.5',
                'font-mono text-[11.5px] font-semibold tracking-[0.02em]',
                variant === 'accent' && 'bg-primary text-primary-foreground shadow-neu-sm',
                variant === 'dark' && 'bg-foreground text-background shadow-neu-sm',
                variant === 'empty' && 'bg-muted text-muted-foreground shadow-neu-inset',
              )}
            >
              {variant === 'empty' && (
                // Token-colored repeating gradient, not a fixed-stroke SVG
                // data URI — see metric-bar.tsx for why (dark-mode --muted
                // has the same luminance as a hardcoded near-black stroke).
                <span
                  aria-hidden
                  className="pointer-events-none absolute inset-0"
                  style={{
                    backgroundImage:
                      'repeating-linear-gradient(45deg, hsl(var(--muted-foreground) / 0.16) 0, hsl(var(--muted-foreground) / 0.16) 1px, transparent 1px, transparent 6px)',
                  }}
                />
              )}
              <span className="relative">{s.label}</span>
            </div>
          )
        })}
      </div>
      {caption && (
        <span className="text-[12px] tracking-[0.04em] text-muted-foreground lowercase">{caption}</span>
      )}
    </div>
  ),
)
SegmentedProgress.displayName = 'SegmentedProgress'
