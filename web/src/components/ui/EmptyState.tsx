import type { ReactNode } from 'react'
import { Button } from './shadcn/button'

interface EmptyStateProps {
  icon?: ReactNode
  title: ReactNode
  description?: ReactNode
  actionLabel?: string
  onAction?: () => void
  // Optional ReactNode action slot for callers that need anything
  // beyond a single label+onClick (link buttons, multi-button rows).
  // Takes precedence over actionLabel/onAction when provided.
  action?: ReactNode
}

export function EmptyState({ icon, title, description, actionLabel, onAction, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 px-6 py-16 text-center">
      {icon && (
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
          {icon}
        </div>
      )}
      <h3 className="text-base font-semibold text-foreground">{title}</h3>
      {description && <p className="max-w-sm text-sm text-muted-foreground">{description}</p>}
      {(action ?? (actionLabel && onAction)) && (
        <div className="mt-1">
          {action ?? <Button onClick={onAction}>{actionLabel}</Button>}
        </div>
      )}
    </div>
  )
}
