import { AlertTriangle } from 'lucide-react'
import { Button } from './Button'

interface ErrorStateProps { message?: string; onRetry?: () => void }

export function ErrorState({ message = 'Something went wrong', onRetry }: ErrorStateProps) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-16 text-center">
      <AlertTriangle className="h-10 w-10 text-red-500" />
      <p className="text-sm text-[var(--color-text-secondary)]">{message}</p>
      {onRetry && <Button variant="outline" onClick={onRetry}>Retry</Button>}
    </div>
  )
}
