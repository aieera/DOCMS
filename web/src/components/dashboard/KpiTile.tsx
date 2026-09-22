import { Link } from '@tanstack/react-router'
import { AlertTriangle, type LucideIcon } from 'lucide-react'

import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { Skeleton } from '@/components/ui/Skeleton'
import { useCountUp } from '@/hooks/useCountUp'
import { cn } from '@/lib/cn'
import { Sparkline } from './Sparkline'
import type { Point } from './metrics'

interface KpiTileProps {
  icon: LucideIcon
  label: string
  /** `undefined` means "still loading" — it must not render as 0. Also
   * `undefined` when `isError` is true; the failed state below takes
   * over rendering in that case. */
  value: number | undefined
  hint?: string
  hintTone?: 'muted' | 'alert'
  href: string
  /** Only supplied where a real series exists; no decorative fakes. */
  sparkline?: Point[]
  delayIndex?: number
  /**
   * True when the query behind this tile failed. Fix round 1 (review):
   * a failed fetch must be visually and programmatically distinct from
   * both "still loading" and a genuine 0 — it used to fall through to
   * `docTotal ?? 0` and render a confident, lying "0".
   */
  isError?: boolean
}

export function KpiTile({
  icon: Icon, label, value, hint, hintTone = 'muted', href, sparkline, delayIndex = 0, isError = false,
}: KpiTileProps) {
  const shown = useCountUp(value ?? 0)
  // Precedence: failed > loading > ready. A tile is only "loading" once
  // we know it isn't failed — otherwise a rejected query (value
  // undefined, isError true) would render the loading skeleton, which
  // is a different lie than the "0" this replaces but still a lie.
  const loading = value === undefined && !isError
  // The hint must never describe data that hasn't arrived yet (fix
  // round 1, Critical A: "across 0 workspaces" while the workspaces
  // query is still in flight). On failure the hint IS shown — it's the
  // "unable to load" copy — but forced to the alert tone regardless of
  // what the caller passed, since a failed query is never routine.
  const showHint = Boolean(hint) && !loading
  const tone = isError ? 'alert' : hintTone
  // L106: the sparkline belongs to the numeral. Beside a skeleton or a
  // failed "—" it would be a real trend under a number we don't have.
  const numeralShown = !loading && !isError

  const accessibleName = loading
    ? label
    : isError
      // Fix round 1, Critical B: aria-label replaces the accessible
      // name computed from content, so a screen-reader user must be
      // told the fetch failed here — never left to infer it from a
      // numeral that isn't there.
      ? `${label}: unavailable`
      // Fix round 1, Important C: the hint (e.g. "2 overdue") is
      // visually alongside the numeral, so it belongs in the
      // accessible name too, or it never reaches assistive tech.
      : showHint
        ? `${label}: ${value?.toLocaleString()}, ${hint}`
        : `${label}: ${value?.toLocaleString()}`

  return (
    <Link
      to={href}
      aria-label={accessibleName}
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

      {isError ? (
        <div className="flex min-h-[64px] flex-col justify-center gap-1" data-testid="kpi-failed">
          <span className="flex items-center gap-1.5 text-destructive">
            <AlertTriangle className="h-5 w-5" />
            <span className="text-2xl font-semibold leading-none">—</span>
          </span>
          <span className="block text-[13px] font-medium text-muted-foreground">{label}</span>
        </div>
      ) : loading ? (
        <div className="flex min-h-[64px] flex-col gap-2" data-testid="kpi-skeleton">
          <Skeleton className="h-9 w-20 rounded-md" />
          <Skeleton className="h-3 w-24 rounded-md" />
        </div>
      ) : (
        <div className="min-h-[64px]">
          <span className="block text-[34px] font-semibold leading-none tracking-[-0.025em] tabular-nums text-foreground">
            {/* M3: count up numerically, group digits only at render. */}
            {shown.toLocaleString()}
          </span>
          <span className="mt-1.5 block text-[13px] font-medium text-muted-foreground">{label}</span>
        </div>
      )}

      {/* The hint is real data or nothing — never a placeholder while the
          query is still loading. On failure it's forced to alert tone.
          I4: the line itself always renders (min-h-4 = one text-xs line)
          so the tile does not grow when the data lands. */}
      <p
        className={cn('min-h-4 text-xs', tone === 'alert' ? 'text-destructive' : 'text-muted-foreground')}
        data-testid="kpi-hint"
      >
        {showHint ? hint : null}
      </p>

      {/* I4: whenever a series is supplied, its h-8 slot is reserved from
          the first render, so the tile does not grow when the search lands. */}
      {sparkline && (
        <div className="h-8" data-testid="kpi-sparkline">
          {numeralShown && sparkline.length > 1 && (
            <Sparkline points={sparkline} className="text-primary" />
          )}
        </div>
      )}
    </Link>
  )
}
