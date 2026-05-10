import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Search, Bookmark, X, ChevronDown, ChevronRight } from 'lucide-react'

import { search } from '@/api/search'
import {
  useSavedSearches,
  useCreateSavedSearch,
  useDeleteSavedSearch,
} from '@/hooks/useSavedSearches'
import { PageHeader } from '@/components/shared/PageHeader'
import { Input } from '@/components/ui/Input'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/Badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'

// ADR 0082 — facet sidebar state lives entirely in the URL so any
// search-with-filters is bookmarkable. The route's validateSearch
// types the params so a typo on either side is a build error.

const DEFAULT_FACETS = ['tag', 'author', 'classification', 'lifecycle_state', 'doc_type']

interface SearchParams {
  q?: string
  // Facet display: comma-separated list of facet names to render.
  facet?: string
  // Selected filter values — multi-valued via repeated params.
  tag?: string[]
  author?: string[]
  classification?: string[]
  region_pin?: string[]
  lifecycle_state?: string[]
  mime_type?: string[]
  workspace_id?: string
  // Open/closed sidebar groups (ux state, NOT a filter — kept in URL
  // so reload preserves the user's expansion choices).
  closed?: string[]
}

const FACET_LABELS: Record<string, string> = {
  tag: 'Tag',
  author: 'Author',
  classification: 'Classification',
  lifecycle_state: 'Lifecycle',
  doc_type: 'Document type',
  content_type: 'Content type',
  region_pin: 'Region',
  workspace_id: 'Workspace',
}

// Each facet name in the URL maps to a SearchFilters field on the
// backend. Centralized so the URL→body translation lives in one
// place.
const FACET_TO_FILTER: Record<string, keyof SearchParams> = {
  tag: 'tag',
  author: 'author',
  classification: 'classification',
  lifecycle_state: 'lifecycle_state',
  doc_type: 'mime_type',
  content_type: 'mime_type',
  region_pin: 'region_pin',
  workspace_id: 'workspace_id',
}

function asArray(v: string | string[] | undefined): string[] {
  if (!v) return []
  return Array.isArray(v) ? v : [v]
}

function SearchPage() {
  const navigate = useNavigate({ from: '/search' })
  const params = Route.useSearch() as SearchParams

  const query = params.q ?? ''
  const facetsToShow = useMemo(
    () => (params.facet ? params.facet.split(',') : DEFAULT_FACETS),
    [params.facet],
  )
  const closedGroups = new Set(asArray(params.closed))

  // Build the SearchRequest body from the URL params. Empty filter
  // arrays are omitted so the request payload only carries dimensions
  // the user actually selected — keeps the wire body small and
  // backend logs readable.
  const searchBody = useMemo(() => {
    const tags = asArray(params.tag)
    const authors = asArray(params.author)
    const classifications = asArray(params.classification)
    const regions = asArray(params.region_pin)
    const lifecycles = asArray(params.lifecycle_state)
    const mimeTypes = asArray(params.mime_type)
    const filters: Record<string, unknown> = {}
    if (tags.length) filters.tags = tags
    if (classifications.length) filters.document_class = classifications
    if (lifecycles.length) filters.lifecycle_state = lifecycles
    if (mimeTypes.length) filters.mime_type = mimeTypes
    if (authors.length) filters.created_by_name = authors
    if (regions.length) filters.region_pin = regions
    if (params.workspace_id) filters.workspace_id = params.workspace_id
    return { query, facets: facetsToShow, filters, highlight: true }
  }, [
    query, facetsToShow,
    params.tag, params.author, params.classification, params.region_pin,
    params.lifecycle_state, params.mime_type, params.workspace_id,
  ])

  const enabled = (query?.length ?? 0) >= 2 || asArray(params.tag).length > 0
    || asArray(params.author).length > 0 || asArray(params.classification).length > 0
    || asArray(params.region_pin).length > 0 || asArray(params.lifecycle_state).length > 0
  const { data, isLoading } = useQuery({
    queryKey: ['search', searchBody],
    queryFn: () => search(searchBody),
    enabled,
    staleTime: 30_000,
  })

  const { data: savedSearches } = useSavedSearches()
  const createSavedMut = useCreateSavedSearch()
  const deleteSavedMut = useDeleteSavedSearch()

  const setQuery = (q: string) => {
    navigate({ search: (s: SearchParams) => ({ ...s, q: q || undefined }) })
  }

  const toggleFilter = (facet: string, value: string) => {
    const filterKey = FACET_TO_FILTER[facet] ?? facet
    navigate({
      search: (s: SearchParams) => {
        const cur = asArray((s as Record<string, string | string[] | undefined>)[filterKey])
        const next = cur.includes(value)
          ? cur.filter((v) => v !== value)
          : [...cur, value]
        return { ...s, [filterKey]: next.length > 0 ? next : undefined }
      },
    })
  }

  const toggleGroup = (facet: string) => {
    navigate({
      search: (s: SearchParams) => {
        const cur = new Set(asArray(s.closed))
        if (cur.has(facet)) cur.delete(facet)
        else cur.add(facet)
        return { ...s, closed: cur.size > 0 ? Array.from(cur) : undefined }
      },
    })
  }

  const clearAllFilters = () => {
    navigate({ search: () => ({ q: query || undefined }) })
  }

  const handleSave = async () => {
    const name = prompt('Name this saved search:', query || 'untitled')
    if (!name?.trim()) return
    try {
      await createSavedMut.mutateAsync({
        name: name.trim(),
        query: query || '',
        filters: searchBody.filters as Record<string, unknown>,
      })
      toast.success('Saved')
    } catch {
      toast.error('Failed to save')
    }
  }

  const handleApplySaved = (s: { query: string; filters?: Record<string, unknown> }) => {
    const f = (s.filters ?? {}) as Record<string, unknown>
    navigate({
      search: () => ({
        q: s.query || undefined,
        tag:             arrayOf(f.tags),
        classification:  arrayOf(f.document_class),
        lifecycle_state: arrayOf(f.lifecycle_state),
        mime_type:       arrayOf(f.mime_type),
        author:          arrayOf(f.created_by_name),
        region_pin:      arrayOf(f.region_pin),
      }),
    })
  }

  const activeFilterCount =
    asArray(params.tag).length +
    asArray(params.author).length +
    asArray(params.classification).length +
    asArray(params.region_pin).length +
    asArray(params.lifecycle_state).length +
    asArray(params.mime_type).length

  return (
    <div className="grid grid-cols-[260px_1fr] gap-6">
      {/* ---- Sidebar ------------------------------------------------- */}
      <aside data-testid="facet-sidebar" className="space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">Filters</h3>
          {activeFilterCount > 0 && (
            <button
              type="button"
              onClick={clearAllFilters}
              className="text-xs text-muted-foreground underline-offset-2 hover:underline"
              data-testid="clear-filters"
            >
              Clear ({activeFilterCount})
            </button>
          )}
        </div>

        {facetsToShow.map((facet) => {
          const buckets = data?.facets?.[facet] ?? []
          if (buckets.length === 0 && !isLoading) return null
          const filterKey = FACET_TO_FILTER[facet] ?? facet
          const selected = new Set(asArray((params as Record<string, string | string[] | undefined>)[filterKey]))
          const isOpen = !closedGroups.has(facet)
          return (
            <div
              key={facet}
              className="rounded-md border border-border bg-card"
              data-testid={`facet-group-${facet}`}
            >
              <button
                type="button"
                onClick={() => toggleGroup(facet)}
                className="flex w-full items-center justify-between px-3 py-2 text-sm font-medium"
              >
                <span>{FACET_LABELS[facet] ?? facet}</span>
                {isOpen ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
              </button>
              {isOpen && (
                <ul className="space-y-1 px-3 pb-2">
                  {buckets.slice(0, 10).map((b) => {
                    const checked = selected.has(b.value)
                    return (
                      <li key={b.value}>
                        <label className="flex cursor-pointer items-center justify-between gap-2 text-xs">
                          <span className="flex items-center gap-2 truncate">
                            <input
                              type="checkbox"
                              checked={checked}
                              onChange={() => toggleFilter(facet, b.value)}
                              data-testid={`facet-${facet}-${b.value}`}
                              className="h-3.5 w-3.5"
                            />
                            <span className="truncate">{b.value}</span>
                          </span>
                          <span className="text-muted-foreground">{b.count}</span>
                        </label>
                      </li>
                    )
                  })}
                </ul>
              )}
            </div>
          )
        })}
      </aside>

      {/* ---- Main column ------------------------------------------- */}
      <div>
        <PageHeader title="Search" description="Find documents across all workspaces. URL-bookmarkable filters." />

        <div className="mb-4 flex gap-2">
          <div className="flex-1">
            <Input
              icon={<Search className="h-4 w-4" />}
              placeholder="Search documents..."
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              autoFocus
              data-testid="search-input"
            />
          </div>
          <Button
            variant="ghost"
            onClick={handleSave}
            disabled={(!query && activeFilterCount === 0) || createSavedMut.isPending}
            data-testid="save-search"
          >
            <Bookmark className="mr-1 h-4 w-4" /> Save
          </Button>
        </div>

        {savedSearches && savedSearches.length > 0 && (
          <div className="mb-6 flex flex-wrap gap-2" data-testid="saved-searches">
            {savedSearches.map((s) => (
              <div
                key={s.id}
                className="flex items-center gap-1 rounded-full border border-border bg-card px-3 py-1 text-xs"
              >
                <button
                  type="button"
                  onClick={() => handleApplySaved(s)}
                  className="font-medium"
                  data-testid={`apply-saved-${s.name}`}
                >
                  {s.name}
                </button>
                <button
                  type="button"
                  onClick={() => deleteSavedMut.mutate(s.id)}
                  aria-label={`Delete saved search ${s.name}`}
                  className="ml-1 text-muted-foreground hover:text-destructive"
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
            ))}
          </div>
        )}

        {isLoading && (
          <div className="space-y-3">
            {Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
          </div>
        )}

        {data && (data.results?.length ?? 0) === 0 && enabled && (
          <EmptyState
            title="No results"
            description={
              activeFilterCount > 0
                ? 'Try clearing some filters or broadening your query.'
                : `No documents match"${query}"`
            }
          />
        )}

        {data && (data.results?.length ?? 0) > 0 && (
          <div className="space-y-2" data-testid="search-results">
            <p className="mb-3 text-sm text-muted-foreground">
              {data.total_count} results in {data.latency_ms}ms
            </p>
            {(data.results ?? []).map((hit) => (
              // Each hit links to the workspace's document viewer.
              // workspace_id + document_id are guaranteed populated
              // by the indexer; clicking opens the same PDFLayoutViewer
              // route that the workspace tree uses.
              <Link
                key={hit.document_id}
                to="/workspaces/$workspaceId/documents/$documentId"
                params={{ workspaceId: hit.workspace_id, documentId: hit.document_id }}
                className="flex items-start gap-3 rounded-lg border border-border bg-card p-4 transition hover:border-primary hover:bg-muted"
                data-testid={`search-hit-${hit.document_id}`}
              >
                <FileIcon mime={hit.mime_type} className="mt-0.5" />
                <div className="min-w-0 flex-1">
                  {hit.highlights?.title?.[0] ? (
                    // Highlight fragment is server-emitted with <mark>
                    // wrappers around the matched run. Safe to inject
                    // because the backend escapes everything else
                    // before wrapping (see opensearch highlight config).
                    <p
                      className="font-medium"
                      dangerouslySetInnerHTML={{ __html: hit.highlights.title[0] }}
                    />
                  ) : (
                    // No highlight → raw title from the doc. React's
                    // default text-node escaping is what we want here;
                    // a filename like `<script>` must render literally.
                    <p className="font-medium">{hit.title}</p>
                  )}
                  {hit.highlights?.content?.[0] && (
                    <p
                      className="mt-1 text-sm text-muted-foreground"
                      dangerouslySetInnerHTML={{ __html: hit.highlights.content[0] }}
                    />
                  )}
                  <div className="mt-2 flex items-center gap-2">
                    <Badge variant={hit.lifecycle_state}>{hit.lifecycle_state}</Badge>
                    <span className="text-xs text-muted-foreground">{formatFileSize(hit.size_bytes)}</span>
                    <span className="text-xs text-muted-foreground">{formatRelativeTime(hit.created_at)}</span>
                  </div>
                </div>
              </Link>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// Coerce an unknown JSON value (from a saved-search filter blob)
// into the URL's repeated-string-param shape. Empty array → undefined
// so clearing a filter doesn't leave a trailing `?tag=` in the URL.
function arrayOf(v: unknown): string[] | undefined {
  if (!v) return undefined
  if (Array.isArray(v)) return v.length ? v.map(String) : undefined
  return [String(v)]
}

export const Route = createFileRoute('/_authenticated/search')({
  component: SearchPage,
  // ADR 0082 — typed URL state. Strings come through as `string`, but
  // repeated params (?tag=a&tag=b) arrive as `string[]`. Both shapes
  // accepted; asArray() in the component normalizes to []string.
  validateSearch: (raw: Record<string, unknown>): SearchParams => {
    const arr = (k: string) => {
      const v = raw[k]
      if (v == null) return undefined
      if (Array.isArray(v)) return v.map(String)
      return [String(v)]
    }
    const str = (k: string) => {
      const v = raw[k]
      return v == null ? undefined : String(v)
    }
    return {
      q: str('q'),
      facet: str('facet'),
      tag: arr('tag'),
      author: arr('author'),
      classification: arr('classification'),
      region_pin: arr('region_pin'),
      lifecycle_state: arr('lifecycle_state'),
      mime_type: arr('mime_type'),
      workspace_id: str('workspace_id'),
      closed: arr('closed'),
    }
  },
})
