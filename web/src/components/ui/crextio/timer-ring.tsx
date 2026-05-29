import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/cn'

export interface TimerRingProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  /** 0-100 — portion of the ring that's filled */
  value: number
  /** Center text — typically the elapsed time, e.g. "02:35" */
  label: ReactNode
  /** Small caption below the center text, e.g. "Work Time" */
  sublabel?: ReactNode
  /** Pixel size of the ring (square) */
  size?: number
  /** Stroke width in SVG units (radius is 60 - strokeWidth) */
  strokeWidth?: number
  /** Color of the active (filled) arc */
  trackColor?: string
  /** Color of the inactive arc */
  bgColor?: string
}

// Circular progress ring — the "Time tracker" card in the reference.
// Pure SVG, no canvas. `strokeDashoffset` is computed from value so
// the ring animates if the host wraps it in a CSS transition.

export const TimerRing = forwardRef<HTMLDivElement, TimerRingProps>(
  ({
    className,
    value,
    label,
    sublabel,
    size = 168,
    strokeWidth = 10,
    trackColor = '#F5C13B',
    bgColor = '#F4E8C8',
    ...props
  }, ref) => {
    const r = 60 - strokeWidth / 2 - 2
    const circumference = 2 * Math.PI * r
    const offset = circumference * (1 - Math.max(Math.min(value, 100), 0) / 100)
    return (
      <div
        ref={ref}
        className={cn('relative inline-flex items-center justify-center', className)}
        style={{ width: size, height: size }}
        {...props}
      >
        <svg viewBox="0 0 120 120" width={size} height={size} style={{ transform: 'rotate(-90deg)' }} aria-hidden>
          <circle cx={60} cy={60} r={r} fill="none" stroke={bgColor} strokeWidth={strokeWidth} />
          <circle
            cx={60}
            cy={60}
            r={r}
            fill="none"
            stroke={trackColor}
            strokeWidth={strokeWidth}
            strokeLinecap="round"
            strokeDasharray={circumference}
            strokeDashoffset={offset}
            style={{
              filter: 'drop-shadow(0 4px 10px rgba(245,193,59,0.35))',
              transition: 'stroke-dashoffset 600ms cubic-bezier(.2,.7,.2,1)',
            }}
          />
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-0.5">
          <span
            className="font-serif text-[36px] font-light leading-none tracking-[-0.035em]"
            style={{ fontVariationSettings: '"opsz" 144' }}
          >
            {label}
          </span>
          {sublabel && (
            <span className="text-[11px] tracking-[0.03em] text-[#8C8273]">{sublabel}</span>
          )}
        </div>
      </div>
    )
  },
)
TimerRing.displayName = 'TimerRing'
