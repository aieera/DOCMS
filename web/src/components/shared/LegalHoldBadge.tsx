import { Shield } from 'lucide-react'
import { cn } from '@/lib/cn'

// Yellow-amber shield with screen-reader-friendly label. Rendered on
// document detail when hold_count > 0 OR under_legal_hold === true.
// Disables every mutating action upstream — this component is purely
// the visual signal.
export function LegalHoldBadge({ count, className }: { count?: number; className?: string }) {
  const label =
    count && count > 1
      ? `On legal hold — ${count} holds active. Cannot be modified.`
      : 'On legal hold — cannot be modified.'
  return (
    <span
      role="status"
      aria-label={label}
      data-testid="legal-hold-badge"
      title={label}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md bg-amber-100 px-2 py-1 text-xs font-medium text-amber-900 ring-1 ring-amber-300 dark:bg-amber-950/50 dark:text-amber-200 dark:ring-amber-900',
        className,
      )}
    >
      <Shield className="h-3.5 w-3.5" aria-hidden="true" />
      <span>On legal hold</span>
      {count && count > 1 && <span className="opacity-75">· {count}</span>}
    </span>
  )
}
