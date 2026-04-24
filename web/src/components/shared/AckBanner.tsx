import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ClipboardCheck } from 'lucide-react'

import { getMyPending } from '@/api/acknowledgements'

/**
 * Dashboard banner surfacing the caller's pending acknowledgement
 * count. Hidden when the queue is empty so it doesn't add noise.
 *
 * Polls every 60 s so a newly-assigned campaign shows up without a
 * full page reload. Cached under 'ack-my-pending' so the dedicated
 * /acknowledgements page and this banner share the underlying query.
 */
export function AckBanner() {
  const { data } = useQuery({
    queryKey: ['ack-my-pending'],
    queryFn: getMyPending,
    refetchInterval: 60_000,
    staleTime: 30_000,
  })
  const count = data?.length ?? 0
  if (count === 0) return null

  return (
    <Link
      to="/acknowledgements"
      className="mb-4 flex items-center justify-between rounded-lg border border-amber-300 bg-amber-50 p-4 text-amber-900 hover:bg-amber-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100 dark:hover:bg-amber-900"
      aria-label={`You have ${count} pending acknowledgement${count === 1 ? '' : 's'}`}
    >
      <div className="flex items-center gap-3">
        <ClipboardCheck className="h-5 w-5 shrink-0" aria-hidden="true" />
        <div>
          <p className="text-sm font-medium">
            {count} pending {count === 1 ? 'acknowledgement' : 'acknowledgements'}
          </p>
          <p className="text-xs opacity-80">
            Review and attest to outstanding policy documents.
          </p>
        </div>
      </div>
      <span className="text-sm font-medium underline">Open inbox</span>
    </Link>
  )
}
