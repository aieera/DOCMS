import { Link } from '@tanstack/react-router'
import type { LucideIcon } from 'lucide-react'

import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { Skeleton } from '@/components/ui/Skeleton'
import { useCountUp } from '@/hooks/useCountUp'
import { cn } from '@/lib/cn'
import { Sparkline } from './Sparkline'
import type { Point } from './metrics'

interface KpiTileProps {
  icon: LucideIcon
  label: string
  /** `undefined` means "still loading" — it must not render as 0. */
  value: number | undefined
  hint?: string
  hintTone?: 'muted' | 'alert'
  href: string
  /** Only supplied where a real series exists; no decorative fakes. */
  sparkline?: Point[]
  delayIndex?: number
}

export function KpiTile({
  icon: Icon, label, value, hint, hintTone = 'muted', href, sparkline, delayIndex = 0,
}: KpiTileProps) {
  const shown = useCountUp(value ?? 0)

  return (
    <Link
      to={href}
      aria-label={value === undefined ? label : `${label}: ${value}`}
      className={cn(
        'dash-rise group flex min-w-0 flex-col gap-3 rounded-lg bg-card p-5 text-start shadow-neu',
        'transition-[box-shadow,transform] duration-200',
        'hover:-translate-y-0.5 hover:shadow-neu-lg active:translate-y-0 active:shadow-neu-pressed',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
      )}
      style={{ '--dash-delay': `${delayIndex * 60}ms` } as React.CSSProperties}
    >
      <div className="flex items-center justify-between">
        <span
          className="flex h-9 w-9 items-center justify-center rounded-xl bg-muted text-foreground shadow-neu-inset"
          aria-hidden
        >
          <Icon className="h-[1.05rem] w-[1.05rem]" />
        </span>
        <DirectionalIcon
          name="ChevronRight"
          className="h-4 w-4 text-muted-foreground transition-transform group-hover:translate-x-0.5"
        />
      </div>

      {value === undefined ? (
        <div className="flex min-h-[64px] flex-col gap-2" data-testid="kpi-skeleton">
          <Skeleton className="h-9 w-20 rounded-md" />
          <Skeleton className="h-3 w-24 rounded-md" />
        </div>
      ) : (
        <div className="min-h-[64px]">
          <span className="block text-[34px] font-semibold leading-none tracking-[-0.025em] tabular-nums text-foreground">
            {shown}
          </span>
          <span className="mt-1.5 block text-[13px] font-medium text-muted-foreground">{label}</span>
        </div>
      )}

      {/* The hint is real data or nothing — never a placeholder trend. */}
      {hint && (
        <p className={cn('text-xs', hintTone === 'alert' ? 'text-destructive' : 'text-muted-foreground')}>
          {hint}
        </p>
      )}

      {sparkline && sparkline.length > 1 && (
        <Sparkline points={sparkline} className="text-primary" />
      )}
    </Link>
  )
}
