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
                variant === 'accent' && cn(
                  'bg-[#F5C13B] text-[#1A1A1A]',
                  'shadow-[inset_0_1px_0_rgba(255,255,255,0.4)]',
                ),
                variant === 'dark' && 'bg-[#1A1A1A] text-[#FAFAFA]',
                variant === 'empty' && 'bg-[#F4E8C8] text-[#B9AC95]',
              )}
            >
              {variant === 'empty' && (
                <span
                  aria-hidden
                  className="pointer-events-none absolute inset-0"
                  style={{
                    backgroundImage:
                      "url(\"data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='6' height='6'><path d='M-1,1 l2,-2 M0,6 l6,-6 M5,7 l2,-2' stroke='%231A1A1A' stroke-width='0.6' opacity='0.16'/></svg>\")",
                    backgroundSize: '6px 6px',
                  }}
                />
              )}
              <span className="relative">{s.label}</span>
            </div>
          )
        })}
      </div>
      {caption && (
        <span className="text-[12px] tracking-[0.04em] text-[#8C8273] lowercase">{caption}</span>
      )}
    </div>
  ),
)
SegmentedProgress.displayName = 'SegmentedProgress'
