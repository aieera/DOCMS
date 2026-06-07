import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, keepPreviousData } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Search, Bookmark, X, ChevronDown } from 'lucide-react'
import { search } from '@/api/search'
import {
  useSavedSearches,
  useCreateSavedSearch,
  useDeleteSavedSearch,
} from '@/hooks/useSavedSearches'
import { PageHeader } from '@/components/shared/PageHeader'
import { Input } from '@/components/ui/shadcn/input'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { formatFileSize, formatRelativeTime, lifecycleStateLabel } from '@/lib/formatters'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { parseFieldSyntax } from '@/lib/searchParser'
import type { SavedSearch } from '@/api/savedSearches'

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

  const urlQuery = params.q ?? ''
  // Debounce keystrokes → URL writes so the search query fires once per
  // settled value, not once per keystroke. Keep the input value local
  // so typing stays responsive; URL state catches up after 250ms.
  const [inputValue, setInputValue] = useState(urlQuery)
  const debounceRef = useRef<number | null>(null)
  useEffect(() => { setInputValue(urlQuery) }, [urlQuery])
  useEffect(() => () => {
    if (debounceRef.current) window.clearTimeout(debounceRef.current)
  }, [])
  const query = inputValue
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
    // Hybrid mode fuses BM25 with dense-vector (Qdrant) results so the
    // page delivers the "Full-text + semantic" search the dashboard
    // advertises. The backend degrades to lexical-only when the vector
    // path is unavailable (no embeddings yet, intelligence down), so
    // this is always safe — worst case it behaves like the old lexical
    // default.
    return { query, facets: facetsToShow, filters, highlight: true, search_mode: 'hybrid' }
  }, [
    query, facetsToShow,
    params.tag, params.author, params.classification, params.region_pin,
    params.lifecycle_state, params.mime_type, params.workspace_id,
  ])

  const enabled = (query?.length ?? 0) >= 2 || asArray(params.tag).length > 0
    || asArray(params.author).length > 0 || asArray(params.classification).length > 0
    || asArray(params.region_pin).length > 0 || asArray(params.lifecycle_state).length > 0
  // keepPreviousData prevents the results list from collapsing to a
  // spinner on every keystroke; the previous page stays visible until
  // the new one resolves so the user can see what changed instead of
  // a thrashing skeleton.
  const { data, isLoading } = useQuery({
    queryKey: ['search', searchBody],
    queryFn: () => search(searchBody),
    enabled,
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  })

  const { data: savedSearches } = useSavedSearches()
  const createSavedMut = useCreateSavedSearch()
  const deleteSavedMut = useDeleteSavedSearch()

  const setQuery = (raw: string) => {
    const { free, fields } = parseFieldSyntax(raw)
    navigate({
      search: (s: SearchParams) => {
        const next: SearchParams = { ...s, q: free || undefined }
        // Merge parsed field tokens with existing URL params — sidebar
        // selections made independently are preserved.
        if (fields.tag?.length) next.tag = [...new Set([...asArray(s.tag), ...fields.tag])]
        if (fields.author?.length) next.author = [...new Set([...asArray(s.author), ...fields.author])]
        if (fields.classification?.length) next.classification = [...new Set([...asArray(s.classification), ...fields.classification])]
        if (fields.region_pin?.length) next.region_pin = [...new Set([...asArray(s.region_pin), ...fields.region_pin])]
        if (fields.lifecycle_state?.length) next.lifecycle_state = [...new Set([...asArray(s.lifecycle_state), ...fields.lifecycle_state])]
        if (fields.mime_type?.length) next.mime_type = [...new Set([...asArray(s.mime_type), ...fields.mime_type])]
        if (fields.workspace_id) next.workspace_id = fields.workspace_id
        return next
      },
    })
  }

  const removeFilter = (key: string, value: string) => {
    navigate({
      search: (s: SearchParams) => {
        const cur = asArray((s as Record<string, string | string[] | undefined>)[key])
        const next = cur.filter((v) => v !== value)
        return { ...s, [key]: next.length > 0 ? next : undefined }
      },
    })
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
        workspace_id: params.workspace_id,
      })
      toast.success('Saved')
    } catch {
      toast.error('Failed to save')
    }
  }

  const handleApplySaved = (s: SavedSearch) => {
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
        workspace_id:    s.workspace_id ? String(s.workspace_id) : undefined,
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
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Filters</h3>
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
        {/* No-results-yet hint so the sidebar doesn't render as just a
            naked "Filters" label when the page first opens. The facet
            buckets only populate after the first successful search,
            and previously the empty sidebar made it look broken. */}
        {!enabled && (
          <p className="rounded-md border border-dashed border-border bg-muted/20 p-3 text-[11px] text-muted-foreground">
            Start typing in the search bar (or pick a filter once results load) to see facets like tags, authors, and classifications here.
          </p>
        )}

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
                {isOpen ? <ChevronDown className="h-4 w-4" /> : <DirectionalIcon name="ChevronRight" className="h-4 w-4" />}
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
              icon={<Search className="h-5 w-5" />}
              placeholder="Search by filename, content, tags…"
              value={query}
              onChange={(e) => {
                const v = e.target.value
                setInputValue(v)
                if (debounceRef.current) window.clearTimeout(debounceRef.current)
                debounceRef.current = window.setTimeout(() => setQuery(v), 250)
              }}
              autoFocus
              data-testid="search-input"
              className="h-12 text-base shadow-sm"
            />
          </div>
          <Button
            variant="outline"
            onClick={handleSave}
            disabled={(!query && activeFilterCount === 0) || createSavedMut.isPending}
            data-testid="save-search"
            className="h-12 gap-2"
          >
            <Bookmark className="h-4 w-4" /> Save
          </Button>
        </div>

        <ActiveFilterChips params={params} onRemove={removeFilter} />

        {savedSearches && savedSearches.length > 0 && (
          <div className="mb-6 flex flex-wrap gap-2" data-testid="saved-searches">
            {savedSearches.map((s) => (
              <div
                key={s.id}
                className="flex items-center gap-1 rounded-full border border-border bg-card py-1 ps-3 pe-1 text-xs"
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
                  title={`Remove ${s.name}`}
                  className="ms-1 inline-flex h-5 w-5 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        )}

        {/* Item 40 — pre-query empty canvas. Renders only when nothing
            else is on screen (no skeleton, no hint, no results, no
            "type 2 chars" notice). */}
        {!isLoading && !enabled && query.length === 0 && (
          <EmptyState
            icon={<Search className="h-8 w-8" />}
            title="Search across all workspaces"
            description="Type a query above to find documents by filename, content, or tags. Or pick a filter from the left sidebar once results load."
          />
        )}

        {isLoading && (
          <div className="space-y-3">
            {Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
          </div>
        )}

        {/* M-5: explicit hint for the 1-char-query window. Previously
            the input swallowed single keystrokes silently because
            `enabled` only flips at length >= 2 — looked like search
            was broken. Filter-only searches (`enabled === true` via
            facets) still bypass the hint. */}
        {!enabled && query.length > 0 && query.length < 2 && (
          <EmptyState
            icon={<Search className="h-6 w-6" />}
            title="Type at least 2 characters to search"
            description="Or pick a filter from the sidebar — searches with active filters don't need a query string."
          />
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
                    // Fall back so a hit whose title field is missing/empty in
                    // the index still renders a line instead of a blank card.
                    <p className="font-medium">{hit.title?.trim() || 'Untitled document'}</p>
                  )}
                  {hit.highlights?.content?.[0] && (
                    <p
                      className="mt-1 text-sm text-muted-foreground"
                      dangerouslySetInnerHTML={{ __html: hit.highlights.content[0] }}
                    />
                  )}
                  <div className="mt-2 flex items-center gap-2">
                    {hit.lifecycle_state && (
                      <Badge variant={hit.lifecycle_state}>{lifecycleStateLabel(hit.lifecycle_state)}</Badge>
                    )}
                    <span className="text-xs text-muted-foreground">{formatFileSize(hit.size_bytes)}</span>
                    {(() => {
                      // The index can hand back a zero created_at (0001-01-01)
                      // because the indexer doesn't populate it yet — that
                      // rendered as a nonsense relative time. Treat the zero
                      // value as absent and fall back to the indexed updated_at.
                      const real = (s?: string) => {
                        if (!s) return undefined
                        const t = Date.parse(s)
                        return Number.isNaN(t) || new Date(t).getUTCFullYear() < 1990 ? undefined : s
                      }
                      const when = real(hit.created_at) ?? real(hit.updated_at)
                      return when ? <span className="text-xs text-muted-foreground">{formatRelativeTime(when)}</span> : null
                    })()}
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

// Chip bar showing all active filter values as removable badges.
// Surfaces both field-syntax parsed tokens and sidebar checkbox
// selections so the full filter state is always visible at a glance.
function ActiveFilterChips({
  params,
  onRemove,
}: {
  params: SearchParams
  onRemove: (key: string, value: string) => void
}) {
  type ChipDef = { key: string; display: string; value: string }
  const chips: ChipDef[] = []

  const addChips = (
    key: string,
    label: string,
    values: string[],
    display?: (v: string) => string,
  ) => {
    for (const v of values) {
      chips.push({ key, display: `${label}: ${display ? display(v) : v}`, value: v })
    }
  }

  addChips('tag', 'Tag', asArray(params.tag))
  addChips('lifecycle_state', 'Status', asArray(params.lifecycle_state), lifecycleStateLabel)
  addChips('mime_type', 'Type', asArray(params.mime_type))
  addChips('author', 'Author', asArray(params.author))
  addChips('region_pin', 'Region', asArray(params.region_pin))
  addChips('classification', 'Class', asArray(params.classification))

  if (chips.length === 0) return null

  return (
    <div className="mb-3 flex flex-wrap gap-1.5" data-testid="active-filter-chips" aria-label="Active filters">
      {chips.map((chip) => (
        <span
          key={`${chip.key}:${chip.value}`}
          className="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2.5 py-0.5 text-xs font-medium text-primary"
        >
          {chip.display}
          <button
            type="button"
            onClick={() => onRemove(chip.key, chip.value)}
            aria-label={`Remove ${chip.display} filter`}
            className="ms-0.5 text-primary/70 hover:text-primary"
          >
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
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
