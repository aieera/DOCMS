import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'

import { search } from '@/api/search'
import { getMimeTypeLabel, lifecycleStateLabel } from '@/lib/formatters'
import { monthSeries, toSlices, type Point, type Slice } from './metrics'

export const dashboardMetricsKey = ['dashboard', 'metrics'] as const

// One read-only call to the existing POST /search feeds four widgets.
// An empty query builds match_all server-side
// (search/internal/opensearch/query.go). `page_size` cannot be 0 — the
// service coerces <=0 to its default page size — so ask for the
// smallest legal page and ignore `results`.
const FACETS = ['created_at', 'lifecycle_state', 'doc_type', 'author'] as const

export interface DashboardMetrics {
  activity: Point[]
  lifecycle: Slice[]
  fileTypes: Slice[]
  contributors: Slice[]
  /**
   * True until the facet response exists. Derived from react-query's
   * `isPending`, NOT `isLoading`: a query that starts offline is paused
   * (`isLoading: false`, `data: undefined`), and gating on `isLoading`
   * rendered four false empty states. The prop keeps its name so the
   * widgets' `WidgetCard isLoading` wiring is unchanged.
   */
  isLoading: boolean
  isError: boolean
  refetch: () => void
}

export function useDashboardMetrics(): DashboardMetrics {
  const query = useQuery({
    queryKey: dashboardMetricsKey,
    queryFn: () => search({ query: '', facets: [...FACETS], page_size: 1, search_mode: 'lexical' }),
    staleTime: 120_000,
  })

  const facets = query.data?.facets
  const derived = useMemo(() => ({
    activity: monthSeries(facets?.created_at),
    lifecycle: toSlices(facets?.lifecycle_state, lifecycleStateLabel, 8),
    fileTypes: toSlices(facets?.doc_type, getMimeTypeLabel, 5),
    contributors: toSlices(facets?.author, (v) => v, 5),
  }), [facets])

  return {
    ...derived,
    isLoading: query.isPending,
    isError: query.isError,
    refetch: () => { void query.refetch() },
  }
}
