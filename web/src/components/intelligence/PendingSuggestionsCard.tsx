import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Sparkles } from 'lucide-react'

import { listPendingTagSuggestions } from '@/api/intelligence'
import { WarmCard } from '@/components/ui/crextio'
import { useAuthStore } from '@/store/authStore'

// Dashboard tile surfacing the async auto-tag pipeline's output: how
// many AI tag suggestions are waiting for a human decision, linking to
// the review queue (Admin → Tags). The backing endpoint is gated to
// owner/admin/compliance_officer, so other roles never even fire the
// request (and the call itself suppresses the global 403 toast as a
// second line of defence). Renders nothing while loading, when the
// count is zero, or on any error.
export function PendingSuggestionsCard() {
  const { t } = useTranslation('common')
  const role = useAuthStore((s) => s.user?.role)
  const mayReview = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  const q = useQuery({
    queryKey: ['pending-tag-suggestions-count'],
    queryFn: () => listPendingTagSuggestions({ limit: 1 }),
    // Short window on purpose: reviewing a suggestion invalidates this
    // key, but a minute-long staleTime also let the tile drift behind
    // the queue whenever the review happened in another tab.
    staleTime: 10_000,
    retry: false,
    enabled: mayReview,
  })

  // `!q.data.total`, not `=== 0`: a response missing `total` left the
  // banner rendering with an undefined count, and since the copy is a
  // PLURAL lookup, i18next could not pick a form and printed the raw key
  // — "dashboard.widgets.suggestions_waiting" — on the dashboard. A
  // card with no count to show has nothing to say either way.
  if (!mayReview || q.isError || !q.data || !q.data.total) return null

  return (
    // Informational banner, NOT a whole-card link — clicking the count
    // used to hard-navigate to the tags page, which testers found
    // surprising. Only the explicit "Review" affordance navigates.
    // flex-ROW explicitly: WarmCard's base is `flex flex-col`, and
    // tailwind-merge keeps the base's flex-col unless overridden.
    <WarmCard
      padded="sm"
      className="flex-row items-center gap-3"
      data-testid="pending-suggestions-card"
    >
      <span
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-[10px] bg-primary/10 text-primary"
        aria-hidden
      >
        <Sparkles className="h-4 w-4" />
      </span>
      <p className="min-w-0 truncate text-sm">
        <span className="font-semibold">{q.data.total}</span>
        {/* The count stays in the bold span above, so these plural forms
            carry no {{count}} — i18next still selects the right one from
            the `count` option. English resolves one/other; Arabic
            resolves zero/one/two/few/many/other. */}
        <span className="text-muted-foreground">
          {' '}{t('dashboard.widgets.suggestions_waiting', { count: q.data.total })}
        </span>
      </p>
      <Link
        to="/admin/tagging"
        className="ms-auto shrink-0 rounded-md px-2 py-1 text-sm font-medium text-primary hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {t('dashboard.widgets.suggestions_review')}
      </Link>
    </WarmCard>
  )
}
