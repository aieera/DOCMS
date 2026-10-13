import { cn } from '@/lib/cn'

// Theme-aware loading placeholder. Uses the muted token so the
// shimmer matches whatever surface it's parked on (Card, page bg,
// sidebar) in both light + dark.
export function Skeleton({ className }: { className?: string }) {
  return <div className={cn('animate-pulse rounded-md bg-muted shadow-neu-inset', className)} />
}
