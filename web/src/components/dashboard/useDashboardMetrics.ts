import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'

import { search } from '@/api/search'
import { getMimeTypeLabel, lifecycleStateLabel } from '@/lib/formatters'
import type { FacetBucket } from '@/types/api'
import { bucketTotal, monthSeries, toSlices, type Point, type Slice } from './metrics'

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
  /**
   * The response has documents but no `facets` key. Above 1M readable
   * documents the search service strips the aggregations
   * (facetSkipThreshold, search/internal/service) and `facets` is
   * `omitempty`, so it simply disappears — every derived array is then
   * `[]`. That is UNAVAILABLE, not empty: the widgets must say so rather
   * than claim "No documents added" beside a 1,200,000 Documents KPI.
   */
  isUnavailable: boolean
  refetch: () => void
}

export function useDashboardMetrics(): DashboardMetrics {
  const query = useQuery({
    queryKey: dashboardMetricsKey,
    queryFn: () => search({ query: '', facets: [...FACETS], page_size: 1, search_mode: 'lexical' }),
    staleTime: 120_000,
  })

  const facets = query.data?.facets
  const derived = useMemo(() => {
    // I1: doc_type and author come back truncated to their top 20 buckets,
    // so their own sum undercounts a tenant with more than 20 values.
    // lifecycle_state (size 10, seven states) is never truncated, so its
    // sum is the document total. max() keeps every share at or below 100%
    // if some documents were indexed without a lifecycle_state.
    const lifecycleTotal = bucketTotal(facets?.lifecycle_state)
    const denominator = (buckets: FacetBucket[] | undefined) => Math.max(lifecycleTotal, bucketTotal(buckets))
    return {
      activity: monthSeries(facets?.created_at),
      lifecycle: toSlices(facets?.lifecycle_state, lifecycleStateLabel, 8),
      fileTypes: toSlices(facets?.doc_type, getMimeTypeLabel, 5, denominator(facets?.doc_type)),
      contributors: toSlices(facets?.author, (v) => v, 5, denominator(facets?.author)),
    }
  }, [facets])

  return {
    ...derived,
    isLoading: query.isPending,
    isError: query.isError,
    // Number(): C1 showed these response types can't be taken on trust.
    isUnavailable: query.isSuccess && Number(query.data.total_count) > 0 && !query.data.facets,
    refetch: () => { void query.refetch() },
  }
}
