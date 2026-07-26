import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Sparkles } from 'lucide-react'

import { listPendingTagSuggestions } from '@/api/intelligence'
import { WarmCard } from '@/components/ui/crextio'

// Dashboard tile surfacing the async auto-tag pipeline's output: how
// many AI tag suggestions are waiting for a human decision, linking to
// the review queue (Admin → Tags). Renders nothing while loading, when
// the count is zero, or on ANY error — the /admin/tag-suggestions
// endpoint 403s for non-admin roles and that must not break the
// dashboard.
export function PendingSuggestionsCard() {
  const q = useQuery({
    queryKey: ['pending-tag-suggestions-count'],
    queryFn: () => listPendingTagSuggestions({ limit: 1 }),
    staleTime: 60_000,
    retry: false,
  })

  if (q.isError || !q.data || q.data.total === 0) return null

  return (
    <Link
      to="/admin/tags"
      className="block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background rounded-[24px]"
      data-testid="pending-suggestions-card"
    >
      <WarmCard padded="md" className="flex items-center gap-4 transition-all hover:-translate-y-0.5 hover:border-primary/50">
        <span
          className="flex h-9 w-9 shrink-0 items-center justify-center rounded-[12px] bg-muted text-foreground"
          aria-hidden
        >
          <Sparkles className="h-[1.1rem] w-[1.1rem]" />
        </span>
        <div className="min-w-0">
          <p className="text-2xl font-semibold leading-tight">{q.data.total}</p>
          <p className="truncate text-sm text-muted-foreground">
            AI tag suggestions waiting — review them
          </p>
        </div>
      </WarmCard>
    </Link>
  )
}
