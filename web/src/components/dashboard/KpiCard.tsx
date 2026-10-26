import { Link } from '@tanstack/react-router'
import { AlertTriangle, type LucideIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { useCountUp } from '@/hooks/useCountUp'
import { cn } from '@/lib/cn'
import type { Point } from './metrics'

/** Months drawn by the sparkline. */
const SPARK_BARS = 12

interface KpiCardBaseProps {
  icon: LucideIcon
  label: string
  /** `undefined` means "still loading" — it must not render as 0. Also
   * `undefined` when `isError` is true; the failed state below takes
   * over rendering in that case. */
  value: number | undefined
  hint?: string
  hintTone?: 'muted' | 'alert'
  /** Only supplied where a real series exists; no decorative fakes. */
  sparkline?: Point[]
  /**
   * True when the query behind this card failed. A failed fetch must be
   * visually and programmatically distinct from both "still loading" and a
   * genuine 0 — never a confident, lying "0".
   */
  isError?: boolean
}

/** A card either navigates (`href`, optional `search`) or acts (`onClick`). */
type KpiCardTarget =
  | { href: string; search?: Record<string, string>; onClick?: undefined }
  | { onClick: () => void; href?: undefined; search?: undefined }

export type KpiCardProps = KpiCardBaseProps & KpiCardTarget

// Everything inside the card is a <span>: the onClick variant is a <button>,
// whose content model is phrasing content only.
export function KpiCard({
  icon: Icon, label, value, hint, hintTone = 'muted', sparkline, isError = false,
  href, search, onClick,
}: KpiCardProps) {
  const { t } = useTranslation('common')
  const shown = useCountUp(value ?? 0)
  // Precedence: failed > loading > ready. A card is only "loading" once we
  // know it isn't failed — otherwise a rejected query (value undefined,
  // isError true) would render the loading skeleton.
  const loading = value === undefined && !isError
  // The hint must never describe data that hasn't arrived yet ("across 0
  // workspaces" while the workspaces query is in flight). On failure the hint
  // IS shown — it's the "unable to load" copy — forced to the alert tone.
  const showHint = Boolean(hint) && !loading
  const tone = isError ? 'alert' : hintTone
  // The sparkline belongs to the numeral: beside a skeleton or a failed "—"
  // it would be a real trend under a number we don't have.
  const numeralShown = !loading && !isError

  // aria-label replaces the name computed from content, so it carries the
  // failure ("unavailable") and the hint ("2 overdue") explicitly.
  const accessibleName = loading
    ? label
    : isError
      ? t('dashboard.kpi.unavailable_aria', { label })
      : showHint
        ? `${label}: ${value?.toLocaleString()}, ${hint}`
        : `${label}: ${value?.toLocaleString()}`

  const bars = sparkline?.slice(-SPARK_BARS) ?? []
  const barMax = Math.max(0, ...bars.map((p) => p.value))

  const className = cn(
    'flex min-w-0 flex-col gap-2 rounded-xl border border-border bg-card p-5 text-start text-card-foreground shadow-sm',
    'transition-[border-color,box-shadow] duration-150 hover:border-foreground/20',
    'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring focus-visible:shadow-focus-halo',
  )

  const body = (
    <>
      <span className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-medium text-muted-foreground">{label}</span>
        <Icon className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
      </span>

      {/* One 32px row: the numeral (or its skeleton / failed mark) at the
          start, the sparkline slot at the end. */}
      <span className="flex min-h-8 items-end justify-between gap-3">
        {isError ? (
          <span className="flex h-8 items-center gap-1.5 text-destructive" data-testid="kpi-failed">
            <AlertTriangle className="h-5 w-5" aria-hidden />
            <span className="text-metric">—</span>
          </span>
        ) : loading ? (
          <span className="flex h-8 items-center" data-testid="kpi-skeleton">
            <span className="block h-7 w-20 animate-pulse rounded-md bg-muted" />
          </span>
        ) : (
          // Count up numerically, group digits only at render.
          <span className="text-metric text-foreground">{shown.toLocaleString()}</span>
        )}

        {/* Whenever a series is supplied its slot is reserved from the first
            render, so the card does not change shape when the data lands. */}
        {sparkline && (
          <span className="flex h-6 w-24 shrink-0 items-end gap-0.5" data-testid="kpi-sparkline" aria-hidden>
            {numeralShown && bars.length > 1 && bars.map((p) => (
              <span
                key={p.iso}
                data-testid="kpi-sparkline-bar"
                className={cn('block min-h-0.5 flex-1 rounded-[2px]', p.value > 0 ? 'bg-chart-1' : 'bg-border')}
                style={{ height: `${barMax > 0 ? (p.value * 100) / barMax : 0}%` }}
              />
            ))}
          </span>
        )}
      </span>

      {/* The hint is real data or nothing — never a placeholder while the
          query is still loading. The line always renders (min-h-4 = one
          caption line) so the card does not grow when the data lands. */}
      <span
        className={cn(
          'block min-h-4 truncate text-caption',
          // dark: --destructive is short of AA for 12px text on --card, so
          // the alert hint falls back to the foreground token there.
          tone === 'alert' ? 'text-destructive dark:text-foreground' : 'text-muted-foreground',
        )}
        data-testid="kpi-hint"
      >
        {showHint ? hint : null}
      </span>
    </>
  )

  if (onClick) {
    return (
      <button type="button" onClick={onClick} aria-label={accessibleName} className={className}>
        {body}
      </button>
    )
  }
  return (
    <Link to={href} search={search} aria-label={accessibleName} className={className}>
      {body}
    </Link>
  )
}
