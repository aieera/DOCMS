import { AlertTriangle } from 'lucide-react'
import { Button } from './shadcn/button'

interface ErrorStateProps { message?: string; onRetry?: () => void }

export function ErrorState({ message = 'Something went wrong', onRetry }: ErrorStateProps) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-2xl bg-muted px-6 py-16 text-center shadow-neu-inset">
      <AlertTriangle className="h-10 w-10 text-destructive" />
      <p className="text-sm text-muted-foreground">{message}</p>
      {onRetry && <Button variant="outline" onClick={onRetry}>Retry</Button>}
    </div>
  )
}
