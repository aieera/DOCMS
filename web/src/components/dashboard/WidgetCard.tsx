import type { ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'
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
  return (
    <section
      aria-label={title}
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
          {subtitle && <p className="mt-0.5 truncate text-xs text-muted-foreground">{subtitle}</p>}
        </div>
        {action && !isError && <div className="shrink-0">{action}</div>}
      </header>

      <div className={cn('min-w-0 flex-1', bodyClassName)}>
        {isError ? (
          <div className="flex flex-col items-center justify-center gap-3 rounded-2xl bg-muted px-4 py-10 text-center shadow-neu-inset">
            <AlertTriangle className="h-8 w-8 text-destructive" aria-hidden />
            <p className="text-sm text-muted-foreground">Couldn&apos;t load this.</p>
            {onRetry && (
              <Button variant="outline" size="sm" onClick={onRetry}>Retry</Button>
            )}
          </div>
        ) : isLoading ? (
          // Geometry-matched so the loaded card occupies the same box:
          // CLS stays at 0 on a slow connection.
          <div className="flex flex-col gap-3" aria-hidden>
            <Skeleton className="h-4 w-2/5 rounded-md" />
            <Skeleton className="h-28 w-full rounded-xl" />
            <Skeleton className="h-4 w-3/5 rounded-md" />
          </div>
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
