import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'

interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  className?: string
  // When true, drops the bottom margin so a sticky page toolbar
  // can dock directly underneath without a gap.
  noMargin?: boolean
}

// Standard page heading row used at the top of every authenticated
// route. Title + optional description on the left, optional action
// buttons on the right. Stack on small viewports so the actions
// don't squeeze the title.
export function PageHeader({ title, description, actions, className, noMargin }: PageHeaderProps) {
  return (
    <div
      className={cn(
        'flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between',
        noMargin ? '' : 'mb-6',
        className,
      )}
    >
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">{title}</h1>
        {description && (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
