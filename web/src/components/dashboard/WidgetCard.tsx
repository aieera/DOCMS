import { useId, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/Skeleton'
import { cn } from '@/lib/cn'

interface WidgetCardProps {
  title: string
  subtitle?: string
  /**
   * A decorative tile at the START edge of the header, before the title (the
   * design's "icon tile, title"). Cards without one render exactly as before.
   * Pass something `aria-hidden`: the title already names the card.
   */
  icon?: ReactNode
  action?: ReactNode
  isLoading?: boolean
  isError?: boolean
  isEmpty?: boolean
  emptyLabel?: string
  emptyAction?: ReactNode
  onRetry?: () => void
  /**
   * @deprecated The entrance stagger was removed with the card redesign; the
   * prop is accepted and ignored so existing callers keep compiling.
   */
  delayIndex?: number
  className?: string
  bodyClassName?: string
  children: ReactNode
}

/**
 * The shared dashboard widget shell. It owns the state precedence so no
 * individual widget can get it wrong: FAILED beats LOADING beats EMPTY
 * beats content. The UX audit found six screens rendering the
 * success-empty state on a query failure ("You're all caught up" when
 * the fetch actually died) — centralising the branch here is what stops
 * that recurring.
 */
export function WidgetCard({
  title, subtitle, icon, action,
  isLoading, isError, isEmpty, emptyLabel, emptyAction, onRetry,
  className, bodyClassName, children,
}: WidgetCardProps) {
  const { t } = useTranslation('common')
  const titleId = useId()
  const settled = !isLoading && !isError
  const heading = (
    <div className="min-w-0">
      <h2 id={titleId} className="truncate text-base leading-5 font-semibold tracking-[-0.01em] text-foreground">{title}</h2>
      {/* R17: the subtitle sits outside the state switch below, so it
          gets its own gate — a data-derived subtitle ("12 indexed
          documents") must never render above a skeleton or an error
          card. The slot itself always renders (min-h-4 = one text-caption
          line) so the header keeps its height across states. */}
      <p className="mt-0.5 min-h-4 truncate text-caption text-muted-foreground">{settled ? subtitle : null}</p>
    </div>
  )
  return (
    <section
      aria-labelledby={titleId}
      aria-busy={Boolean(isLoading)}
      className={cn(
        'flex min-w-0 flex-col rounded-xl border border-border bg-card shadow-sm',
        className,
      )}
    >
      <header className="flex items-start justify-between gap-3 px-5 pb-3 pt-5">
        {icon ? (
          // Only when an icon is supplied: the tile and the title block share
          // one flex item, so `justify-between` still parks the action at the
          // far edge and every other card's header markup is unchanged.
          <div className="flex min-w-0 items-center gap-3">
            <div className="shrink-0">{icon}</div>
            {heading}
          </div>
        ) : heading}
        {action && !isError && <div className="shrink-0">{action}</div>}
      </header>

      {/* flex-col so the loading skeleton can fill the body's min-height. */}
      <div className={cn('flex min-w-0 flex-1 flex-col px-5 pb-5', bodyClassName)}>
        {isError ? (
          <ErrorState size="sm" message={t('dashboard.widgets.load_error')} onRetry={onRetry} />
        ) : isLoading ? (
          <>
            <span className="sr-only" role="status">{t('dashboard.widgets.loading_title', { title })}</span>
            {/* The skeleton fills the body, and each widget sets the body's
                min-height (via `bodyClassName`) to its typical loaded
                height, so a slow response does not push the page down.
                Not a strict CLS-0 guarantee: content taller than that
                reservation (more lifecycle states, longer lists) can still
                grow the card when it arrives. */}
            <div className="flex flex-1 flex-col gap-3" aria-hidden>
              <Skeleton className="h-4 w-2/5 rounded-md" />
              <Skeleton className="min-h-12 w-full flex-1 rounded-xl" />
              <Skeleton className="h-4 w-3/5 rounded-md" />
            </div>
          </>
        ) : isEmpty ? (
          <div className="flex flex-col items-center justify-center gap-2 rounded-lg bg-muted px-4 py-10 text-center">
            <p className="text-sm font-medium text-foreground">{emptyLabel ?? t('dashboard.widgets.empty_default')}</p>
            {emptyAction}
          </div>
        ) : (
          children
        )}
      </div>
    </section>
  )
}
