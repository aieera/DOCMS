import type { ReactNode } from 'react'

import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/Skeleton'
import { cn } from '@/lib/cn'

interface WidgetCardProps {
  title: string
  subtitle?: string
  action?: ReactNode
  isLoading?: boolean
  isError?: boolean
  isEmpty?: boolean
  emptyLabel?: string
  emptyAction?: ReactNode
  onRetry?: () => void
  /** Stagger index for the entrance animation. */
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
  title, subtitle, action,
  isLoading, isError, isEmpty, emptyLabel, emptyAction, onRetry,
  delayIndex = 0, className, bodyClassName, children,
}: WidgetCardProps) {
  const settled = !isLoading && !isError
  return (
    <section
      aria-label={title}
      aria-busy={Boolean(isLoading)}
      className={cn(
        'dash-rise flex min-w-0 flex-col rounded-lg bg-card p-5 shadow-neu',
        'transition-shadow duration-200 hover:shadow-neu-lg',
        className,
      )}
      style={{ '--dash-delay': `${delayIndex * 60}ms` } as React.CSSProperties}
    >
      <header className="mb-4 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold text-foreground">{title}</h2>
          {/* R17: the subtitle sits outside the state switch below, so it
              gets its own gate — a data-derived subtitle ("12 indexed
              documents") must never render above a skeleton or an error
              card. The slot itself always renders (min-h-4 = one text-xs
              line) so the header keeps its height across states. */}
          <p className="mt-0.5 min-h-4 truncate text-xs text-muted-foreground">{settled ? subtitle : null}</p>
        </div>
        {action && !isError && <div className="shrink-0">{action}</div>}
      </header>

      {/* flex-col so the loading skeleton can fill the body's min-height. */}
      <div className={cn('flex min-w-0 flex-1 flex-col', bodyClassName)}>
        {isError ? (
          <ErrorState size="sm" message="Couldn't load this." onRetry={onRetry} />
        ) : isLoading ? (
          <>
            <span className="sr-only" role="status">{`Loading ${title}`}</span>
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
          <div className="flex flex-col items-center justify-center gap-2 rounded-2xl bg-muted px-4 py-10 text-center shadow-neu-inset">
            <p className="text-sm font-medium text-foreground">{emptyLabel ?? 'Nothing here yet'}</p>
            {emptyAction}
          </div>
        ) : (
          children
        )}
      </div>
    </section>
  )
}
