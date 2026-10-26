import { useId, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Progress } from '@/components/ui/shadcn/progress'
import { cn } from '@/lib/cn'
import { BreakdownBars } from './BreakdownBars'
import { ChartDataTable } from './ChartDataTable'
import { LifecycleRing } from './LifecycleDonut'
import { WidgetCard } from './WidgetCard'
import { lifecycleColor } from './chartTheme'
import { FACETS_UNAVAILABLE_LABEL, type Slice } from './metrics'
import { useDashboardMetrics } from './useDashboardMetrics'

// The loaded body at its tallest typical column, Top contributors with five
// rows, and the card must not change height when the data arrives:
//   column head : 20px title + 2px gap + 16px caption + 16px margin = 54px
//   one row     : 24px avatar line + 6px gap + 6px progress bar     = 36px
//   five rows   : 5 x 36 + 4 x 12px gap                             = 228px
//   padding     : WidgetCard's body is `pb-5` = 20px, and `bodyClassName` lands
//                 on that same border-box element, so the min-height includes it.
//   54 + 228 + 20 = 302 (a 4-state Lifecycle column is 54 + 88 = 142, File types
//   54 + 8 + 16 + 5 x 16 + 4 x 10 = 198). Not measured in a browser. Below the
//   shell breakpoint the columns stack, so that layout is taller than this
//   reserves and can still grow the card. Tailwind only emits literal class
//   names, so BODY_CLASS cannot be built from these numbers.
const BODY_CLASS = 'min-h-[302px]'

/** A first letter for the avatar. Array.from keeps a surrogate pair whole. */
const initialOf = (name: string) => Array.from(name.trim())[0]?.toUpperCase() ?? '?'

/**
 * One panel of the card: a labelled group, not a landmark. The card is the
 * only region; three more would make the page outline noisy.
 *
 * Dividers are logical (`border-s`) rather than Tailwind's `divide-x`, which
 * is `border-left` in Tailwind 3 and would put the rule on the wrong side of
 * every column under dir="rtl". Below the shell breakpoint the columns stack
 * and the grid's `divide-y` rules sit between them instead.
 */
function Column({ title, caption, className, children }: {
  title: string
  caption: string
  className?: string
  children: ReactNode
}) {
  const titleId = useId()
  return (
    <div
      role="group"
      aria-labelledby={titleId}
      className={cn(
        'min-w-0 py-5 first:pt-0 last:pb-0',
        'shell:py-0 shell:ps-6 shell:pe-6 shell:first:ps-0 shell:last:pe-0',
        className,
      )}
    >
      <h3 id={titleId} className="text-sm font-medium leading-5 text-foreground">{title}</h3>
      <p className="mb-4 mt-0.5 text-caption text-muted-foreground">{caption}</p>
      {children}
    </div>
  )
}

function ColumnEmpty({ children }: { children: ReactNode }) {
  return <p className="rounded-lg bg-muted px-4 py-6 text-center text-sm text-muted-foreground">{children}</p>
}

function LifecycleColumn({ slices, total }: { slices: Slice[]; total: number }) {
  const { t } = useTranslation('common')
  return (
    <Column title={t('dashboard.widgets.lifecycle_title')} caption={t('dashboard.widgets.lifecycle_caption')}>
      {slices.length === 0 ? (
        <ColumnEmpty>{t('dashboard.widgets.lifecycle_empty')}</ColumnEmpty>
      ) : (
        <>
          {/* Ring beside the legend when the column is wide enough for both
              (88 + 16 + 144 = 248px), the legend under the ring when not. */}
          <div className="flex flex-wrap items-start gap-x-4 gap-y-3">
            <LifecycleRing slices={slices} total={total} />
            <ul className="flex min-w-[9rem] flex-1 flex-col gap-2">
              {slices.map((s) => (
                <li key={s.key} className="flex items-center gap-2 text-xs">
                  <span
                    data-testid="legend-dot"
                    className="h-2.5 w-2.5 shrink-0 rounded-full"
                    style={{ backgroundColor: lifecycleColor(s.key) }}
                    aria-hidden
                  />
                  <span className="min-w-0 flex-1 truncate text-foreground">{s.label}</span>
                  <span className="shrink-0 font-semibold tabular-nums text-foreground">{s.value.toLocaleString()}</span>
                </li>
              ))}
            </ul>
          </div>
          <ChartDataTable
            caption={t('dashboard.widgets.lifecycle_caption')}
            columns={[t('dashboard.widgets.col_state'), t('dashboard.widgets.col_documents')]}
            rows={slices.map((s) => ({ key: s.key, label: s.label, value: s.value }))}
          />
        </>
      )}
    </Column>
  )
}

function FileTypesColumn({ slices }: { slices: Slice[] }) {
  const { t } = useTranslation('common')
  return (
    <Column
      title={t('dashboard.widgets.file_types_title')}
      // M7 (spec §2.1.4): say what the share is of.
      caption={t('dashboard.widgets.of_indexed')}
      className="shell:border-s"
    >
      {slices.length === 0 ? (
        <ColumnEmpty>{t('dashboard.widgets.file_types_empty')}</ColumnEmpty>
      ) : (
        <BreakdownBars
          variant="stacked"
          slices={slices}
          unit={t('dashboard.widgets.unit_documents')}
          otherLabel={t('dashboard.widgets.other')}
        />
      )}
    </Column>
  )
}

function ContributorsColumn({ slices }: { slices: Slice[] }) {
  const { t } = useTranslation('common')
  const unit = t('dashboard.widgets.unit_documents')
  return (
    <Column
      title={t('dashboard.widgets.contributors_title')}
      caption={t('dashboard.widgets.of_indexed')}
      className="shell:border-s"
    >
      {slices.length === 0 ? (
        <ColumnEmpty>{t('dashboard.widgets.contributors_empty')}</ColumnEmpty>
      ) : (
        <ul className="flex flex-col gap-3">
          {slices.map((s) => {
            const pct = Math.round(s.share * 100)
            return (
              <li key={s.key} className="min-w-0">
                <div className="flex items-center gap-2.5">
                  <span
                    className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-muted text-[11px] font-medium text-foreground"
                    aria-hidden
                  >
                    {initialOf(s.label)}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-[13px] font-medium leading-5 text-foreground">{s.label}</span>
                  <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
                    <span className="font-semibold text-foreground">{s.value.toLocaleString()}</span>
                    <span className="sr-only"> {unit}, </span>
                    {' '}
                    {pct}%
                  </span>
                </div>
                {/* Indented to sit under the name (24px avatar + 10px gap). The
                    bar repeats the figure printed above it, so it is hidden
                    from assistive tech rather than given a name of its own.
                    A 2% floor keeps a small contributor visible, as the
                    plain bars do. */}
                <div className="mt-1.5 ps-[34px]" aria-hidden="true">
                  <Progress value={s.share > 0 ? Math.max(pct, 2) : 0} className="h-1.5" />
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </Column>
  )
}

/**
 * Lifecycle, file types and top contributors as one card with three panels.
 * All three read the same facet query, so they share one hook call — and so
 * the failed and unavailable states are shown once for the card, with one
 * Retry, rather than three times.
 */
export function LibraryBreakdown({ className }: { className?: string }) {
  const { t } = useTranslation('common')
  const { lifecycle, fileTypes, contributors, isLoading, isError, isUnavailable, refetch } = useDashboardMetrics()
  const total = lifecycle.reduce((sum, s) => sum + s.value, 0)
  // A panel with nothing to show says so itself; the card's empty state is for
  // when none has anything. Unavailable is not empty (I2): the facets were
  // dropped, so every derived list is [] and "nothing indexed" would be false.
  const isEmpty = isUnavailable || (lifecycle.length === 0 && fileTypes.length === 0 && contributors.length === 0)

  return (
    <WidgetCard
      title={t('dashboard.widgets.library_title')}
      isLoading={isLoading}
      isError={isError}
      isEmpty={isEmpty}
      emptyLabel={isUnavailable ? FACETS_UNAVAILABLE_LABEL : t('dashboard.widgets.lifecycle_empty')}
      onRetry={refetch}
      className={className}
      bodyClassName={BODY_CLASS}
    >
      <div className="grid grid-cols-1 divide-y divide-border shell:grid-cols-3 shell:divide-y-0">
        <LifecycleColumn slices={lifecycle} total={total} />
        <FileTypesColumn slices={fileTypes} />
        <ContributorsColumn slices={contributors} />
      </div>
    </WidgetCard>
  )
}
