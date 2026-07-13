import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/cn'

// Big-number stat with an optional small leading icon, used for the
// "78 Employees / 56 Hirings / 203 Projects" row in the reference.
// The display number uses a serif font for the warm editorial feel.

export interface HeadlineMetricProps extends HTMLAttributes<HTMLDivElement> {
  /** Small icon shown above the number — e.g. a 14px Lucide SVG */
  icon?: ReactNode
  /** The big number — string or formatted ReactNode */
  value: ReactNode
  /** Caption under the number */
  label: ReactNode
  /** Subtle vertical divider on the left edge (true for non-first items in a row) */
  withDivider?: boolean
}

export const HeadlineMetric = forwardRef<HTMLDivElement, HeadlineMetricProps>(
  ({ className, icon, value, label, withDivider = false, ...props }, ref) => (
    <div
      ref={ref}
      className={cn(
        'flex flex-col gap-1',
        withDivider && 'border-s border-[rgba(26,26,26,0.14)] ps-[14px]',
        className,
      )}
      {...props}
    >
      {icon && (
        <span className="inline-flex items-center gap-1.5 text-[11.5px] text-[#8C8273]">
          {icon}
        </span>
      )}
      <span
        className="font-serif text-[56px] font-light leading-[0.9] tracking-[-0.045em] text-[#1A1A1A]"
        style={{ fontVariationSettings: '"opsz" 144' }}
      >
        {value}
      </span>
      <span className="-mt-0.5 text-[12px] text-[#8C8273]">{label}</span>
    </div>
  ),
)
HeadlineMetric.displayName = 'HeadlineMetric'
