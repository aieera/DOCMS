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
      {/* flex-ROW explicitly: WarmCard's base is `flex flex-col`, and
          tailwind-merge keeps the base's flex-col unless overridden —
          without this the icon stacked above centered text and the card
          ballooned to a full hero row. */}
      <WarmCard padded="sm" className="flex-row items-center gap-3 transition-all hover:-translate-y-0.5 hover:border-primary/50">
        <span
          className="flex h-8 w-8 shrink-0 items-center justify-center rounded-[10px] bg-primary/10 text-primary"
          aria-hidden
        >
          <Sparkles className="h-4 w-4" />
        </span>
        <p className="min-w-0 truncate text-sm">
          <span className="font-semibold">{q.data.total}</span>
          <span className="text-muted-foreground"> AI tag suggestions waiting</span>
        </p>
        <span className="ms-auto shrink-0 text-sm font-medium text-primary">Review</span>
      </WarmCard>
    </Link>
  )
}
