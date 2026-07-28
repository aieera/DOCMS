import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, keepPreviousData } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Search, Bookmark, X, ChevronDown } from 'lucide-react'
import { search } from '@/api/search'
import { getVersions } from '@/api/documents'
import {
  useCreateSavedSearch,
} from '@/hooks/useSavedSearches'
import { useWorkspaces } from '@/hooks/useWorkspaces'
import { PageHeader } from '@/components/shared/PageHeader'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/shadcn/input'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { formatFileSize, formatRelativeTime, lifecycleStateLabel } from '@/lib/formatters'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { parseFieldSyntax } from '@/lib/searchParser'
import { recordRecentSearch } from '@/lib/recentSearches'
import { sanitizeHighlight } from '@/lib/sanitizeHighlight'

// ADR 0082 — facet sidebar state lives entirely in the URL so any
// search-with-filters is bookmarkable. The route's validateSearch
// types the params so a typo on either side is a build error.

const DEFAULT_FACETS = ['tag', 'author', 'classification', 'lifecycle_state', 'doc_type']

// Sort options surface the backend's sort_by/sort_order support
// (relevance | created_at | updated_at | title | size_bytes). Kept as a
// single URL token (?sort=newest) so the choice is bookmarkable.
const SORT_OPTIONS: { value: string; label: string; sortBy?: string; sortOrder?: string }[] = [
  { value: 'relevance', label: 'Relevance' },
  { value: 'newest', label: 'Newest first', sortBy: 'created_at', sortOrder: 'desc' },
  { value: 'oldest', label: 'Oldest first', sortBy: 'created_at', sortOrder: 'asc' },
  { value: 'updated', label: 'Recently updated', sortBy: 'updated_at', sortOrder: 'desc' },
  { value: 'title', label: 'Title (A–Z)', sortBy: 'title', sortOrder: 'asc' },
  { value: 'largest', label: 'Largest first', sortBy: 'size_bytes', sortOrder: 'desc' },
  { value: 'smallest', label: 'Smallest first', sortBy: 'size_bytes', sortOrder: 'asc' },
]

// Size presets map to the backend's size_min_bytes / size_max_bytes
// range filter. Single URL token (?size=1m-10m) for bookmarkability.
const KB = 1024
const MB = 1024 * 1024
const SIZE_OPTIONS: { value: string; label: string; min?: number; max?: number }[] = [
  { value: '', label: 'Any size' },
  { value: 'lt100k', label: 'Under 100 KB', max: 100 * KB },
  { value: '100k-1m', label: '100 KB – 1 MB', min: 100 * KB, max: MB },
  { value: '1m-10m', label: '1 MB – 10 MB', min: MB, max: 10 * MB },
  { value: '10m-100m', label: '10 MB – 100 MB', min: 10 * MB, max: 100 * MB },
  { value: 'gt100m', label: 'Over 100 MB', min: 100 * MB },
]

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
  // Date-range filter (inclusive). YYYY-MM-DD in the URL; converted to
  // RFC3339 at request-build time.
  created_after?: string
  created_before?: string
  // Size preset key (see SIZE_OPTIONS).
  size?: string
  // Sort key (see SORT_OPTIONS); absent = relevance.
  sort?: string
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
  // Every executed search (topbar, deep link, typing here) lands in the
  // topbar dropdown's Recent list. Settle for 1.5s first so partial
  // keystroke states don't pollute it.
  useEffect(() => {
    const q = urlQuery.trim()
    if (!q) return
    const t = window.setTimeout(() => recordRecentSearch(q), 1500)
    return () => window.clearTimeout(t)
  }, [urlQuery])
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
    // Date range: YYYY-MM-DD → RFC3339, widening to whole-day bounds so
    // a single picked day is inclusive on both ends.
    if (params.created_after) filters.created_after = `${params.created_after}T00:00:00Z`
    if (params.created_before) filters.created_before = `${params.created_before}T23:59:59Z`
    // Size preset → byte bounds the backend understands.
    const sizeOpt = SIZE_OPTIONS.find((o) => o.value === params.size)
    if (sizeOpt?.min != null) filters.size_min_bytes = sizeOpt.min
    if (sizeOpt?.max != null) filters.size_max_bytes = sizeOpt.max
    // Hybrid mode fuses BM25 with dense-vector (Qdrant) results so the
    // page delivers the "Full-text + semantic" search the dashboard
    // advertises. The backend degrades to lexical-only when the vector
    // path is unavailable (no embeddings yet, intelligence down), so
    // this is always safe — worst case it behaves like the old lexical
    // default.
    const body: Record<string, unknown> = { query, facets: facetsToShow, filters, highlight: true, search_mode: 'hybrid' }
    const sortOpt = SORT_OPTIONS.find((o) => o.value === params.sort)
    if (sortOpt?.sortBy) {
      body.sort_by = sortOpt.sortBy
      body.sort_order = sortOpt.sortOrder
    }
    return body
  }, [
    query, facetsToShow,
    params.tag, params.author, params.classification, params.region_pin,
    params.lifecycle_state, params.mime_type, params.workspace_id,
    params.created_after, params.created_before, params.size, params.sort,
  ])

  const enabled = (query?.length ?? 0) >= 2 || asArray(params.tag).length > 0
    || asArray(params.author).length > 0 || asArray(params.classification).length > 0
    || asArray(params.region_pin).length > 0 || asArray(params.lifecycle_state).length > 0
    || asArray(params.mime_type).length > 0 || !!params.workspace_id
    || !!params.created_after || !!params.created_before || !!params.size
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

  const createSavedMut = useCreateSavedSearch()

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

  // Set (or clear, when value is falsy) a single scalar URL param —
  // used by the workspace select, date inputs, size + sort dropdowns.
  const setParam = (key: keyof SearchParams, value: string | undefined) => {
    navigate({ search: (s: SearchParams) => ({ ...s, [key]: value || undefined }) })
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
    // Sort is a presentation choice, not a filter — preserve it across
    // a filter clear.
    navigate({ search: () => ({ q: query || undefined, sort: params.sort }) })
  }

  const [saveOpen, setSaveOpen] = useState(false)
  // Per-hit inline versions expander ("N versions" affordance). The
  // index is one row per document (collapse-by-construction), so the
  // version list is fetched lazily from the document service on expand.
  const [expandedVersions, setExpandedVersions] = useState<Set<string>>(new Set())
  const toggleVersions = (documentID: string) => {
    setExpandedVersions((prev) => {
      const next = new Set(prev)
      if (next.has(documentID)) next.delete(documentID)
      else next.add(documentID)
      return next
    })
  }

  const handleSave = async (name: string, alertMe: boolean, intervalMinutes: number) => {
    try {
      await createSavedMut.mutateAsync({
        name: name.trim(),
        query: query || '',
        filters: searchBody.filters as Record<string, unknown>,
        workspace_id: params.workspace_id,
        // ADR 0085 — saving with the alert toggle creates the alert in
        // one step; the workflow service's reconcile loop picks up the
        // notify flag and creates the Temporal schedule.
        notify: alertMe,
        notify_interval_minutes: alertMe ? intervalMinutes : undefined,
      })
      setSaveOpen(false)
      toast.success(alertMe ? 'Saved — alerting on new matches' : 'Saved')
    } catch {
      toast.error('Failed to save')
    }
  }

  const activeFilterCount =
    asArray(params.tag).length +
    asArray(params.author).length +
    asArray(params.classification).length +
    asArray(params.region_pin).length +
    asArray(params.lifecycle_state).length +
    asArray(params.mime_type).length +
    (params.workspace_id ? 1 : 0) +
    (params.created_after ? 1 : 0) +
    (params.created_before ? 1 : 0) +
    (params.size ? 1 : 0)

  const { data: workspaces } = useWorkspaces()
  const workspaceName = (id: string) =>
    workspaces?.find((w) => w.id === id)?.name ?? id

  return (
    // minmax(0,1fr) lets the results column actually shrink (without it
    // long titles/snippets push the grid wider than the viewport and the
    // results header clips). Below lg the rail stacks under the results.
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-[260px_minmax(0,1fr)]">
      {/* ---- Sidebar — sticky on desktop so filters stay reachable while
          scrolling long result lists; stacks below the results on small
          screens (order-last). ------------------------------------------ */}
      <aside
        data-testid="facet-sidebar"
        className="order-last space-y-3 lg:order-none lg:sticky lg:top-4 lg:self-start lg:max-h-[calc(100dvh-7rem)] lg:overflow-y-auto lg:pe-1"
      >
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

        {/* Structured filters — always available, not gated on facet
            buckets returning. Each maps to a backend filter the index
            already supports (workspace_id, created_after/before,
            size_min/max_bytes). */}
        {workspaces && workspaces.length > 0 && (
          <div className="rounded-md border border-border bg-card p-3" data-testid="filter-workspace">
            <label htmlFor="ws-select" className="mb-1.5 block text-sm font-medium">Workspace</label>
            <select
              id="ws-select"
              value={params.workspace_id ?? ''}
              onChange={(e) => setParam('workspace_id', e.target.value)}
              className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs"
              data-testid="filter-workspace-select"
            >
              <option value="">All workspaces</option>
              {workspaces.map((w) => (
                <option key={w.id} value={w.id}>{w.name}</option>
              ))}
            </select>
          </div>
        )}

        <div className="rounded-md border border-border bg-card p-3" data-testid="filter-date">
          <span className="mb-1.5 block text-sm font-medium">Created</span>
          <div className="space-y-1.5">
            <div className="flex items-center gap-2">
              <label htmlFor="date-from" className="w-9 text-[11px] text-muted-foreground">From</label>
              <input
                id="date-from"
                type="date"
                value={params.created_after ?? ''}
                max={params.created_before || undefined}
                onChange={(e) => setParam('created_after', e.target.value)}
                className="flex-1 rounded-md border border-border bg-background px-2 py-1 text-xs"
                data-testid="filter-date-from"
              />
            </div>
            <div className="flex items-center gap-2">
              <label htmlFor="date-to" className="w-9 text-[11px] text-muted-foreground">To</label>
              <input
                id="date-to"
                type="date"
                value={params.created_before ?? ''}
                min={params.created_after || undefined}
                onChange={(e) => setParam('created_before', e.target.value)}
                className="flex-1 rounded-md border border-border bg-background px-2 py-1 text-xs"
                data-testid="filter-date-to"
              />
            </div>
          </div>
        </div>

        <div className="rounded-md border border-border bg-card p-3" data-testid="filter-size">
          <label htmlFor="size-select" className="mb-1.5 block text-sm font-medium">Size</label>
          <select
            id="size-select"
            value={params.size ?? ''}
            onChange={(e) => setParam('size', e.target.value)}
            className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs"
            data-testid="filter-size-select"
          >
            {SIZE_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>{o.label}</option>
            ))}
          </select>
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
            onClick={() => setSaveOpen(true)}
            disabled={(!query && activeFilterCount === 0) || createSavedMut.isPending}
            data-testid="save-search"
            className="h-12 gap-2"
          >
            <Bookmark className="h-4 w-4" /> Save
          </Button>
          {saveOpen && (
            <SaveSearchDialog
              defaultName={query || 'untitled'}
              saving={createSavedMut.isPending}
              onClose={() => setSaveOpen(false)}
              onSave={handleSave}
            />
          )}
        </div>

        <ActiveFilterChips
          params={params}
          onRemove={removeFilter}
          onClearScalar={(k) => setParam(k as keyof SearchParams, undefined)}
          workspaceName={workspaceName}
        />

        {/* Saved-search chips removed — saved searches now live in the
            topbar search dropdown (run/save/unsave), with full management
            on /saved-searches. */}

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
                : `No documents match "${query}"`
            }
          />
        )}

        {data && (data.results?.length ?? 0) > 0 && (
          <div className="space-y-2" data-testid="search-results">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <p className="whitespace-nowrap text-sm text-muted-foreground">
                {data.total_count} results in {data.latency_ms}ms
              </p>
              <div className="flex shrink-0 items-center gap-1.5">
                <label htmlFor="sort-select" className="text-xs text-muted-foreground">Sort</label>
                <select
                  id="sort-select"
                  value={params.sort ?? 'relevance'}
                  onChange={(e) => setParam('sort', e.target.value === 'relevance' ? undefined : e.target.value)}
                  className="h-8 rounded-md border border-border bg-background px-2 text-xs"
                  data-testid="sort-select"
                >
                  {SORT_OPTIONS.map((o) => (
                    <option key={o.value} value={o.value}>{o.label}</option>
                  ))}
                </select>
              </div>
            </div>
            {(data.results ?? []).map((hit) => {
              // Each hit links to the workspace's document viewer.
              // A valid document always has a workspace_id, but an
              // orphaned/bad index record can carry an empty one — and
              // linking that generates "/workspaces//documents/<id>"
              // (router warns "matched route undefined" and 404s).
              // Render those rare records as a non-clickable card.
              const cardClass =
                'flex items-start gap-3 rounded-lg border border-border bg-card p-4 transition hover:border-primary hover:bg-muted'
              const inner = (
                <>
                <FileIcon mime={hit.mime_type} className="mt-0.5" />
                <div className="min-w-0 flex-1">
                  {hit.highlights?.title?.[0] ? (
                    // Highlight fragment is server-emitted with <mark>
                    // wrappers around the matched run. Safe to inject
                    // because the backend escapes everything else
                    // before wrapping (see opensearch highlight config).
                    <p
                      className="truncate font-medium"
                      dangerouslySetInnerHTML={{ __html: sanitizeHighlight(hit.highlights.title[0]) }}
                    />
                  ) : (
                    // No highlight → raw title from the doc. React's
                    // default text-node escaping is what we want here;
                    // a filename like `<script>` must render literally.
                    // Fall back so a hit whose title field is missing/empty in
                    // the index still renders a line instead of a blank card.
                    <p className="truncate font-medium">{hit.title?.trim() || 'Untitled document'}</p>
                  )}
                  {hit.highlights?.content?.[0] && (
                    <p
                      className="mt-1 line-clamp-2 text-sm text-muted-foreground"
                      dangerouslySetInnerHTML={{ __html: sanitizeHighlight(hit.highlights.content[0]) }}
                    />
                  )}
                  <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1">
                    {hit.lifecycle_state && (
                      <Badge variant={hit.lifecycle_state}>{lifecycleStateLabel(hit.lifecycle_state)}</Badge>
                    )}
                    {/* Index rows without an uploaded blob carry size 0 —
                        "0 B" reads as breakage, so show nothing. */}
                    {Number(hit.size_bytes) > 0 && (
                      <span className="text-xs text-muted-foreground">{formatFileSize(hit.size_bytes)}</span>
                    )}
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
                    {(hit.version_count ?? 0) > 1 && (
                      <button
                        type="button"
                        className="text-xs font-medium text-primary hover:underline"
                        data-testid={`versions-toggle-${hit.document_id}`}
                        onClick={(e) => {
                          // The whole card is a <Link> — the expander
                          // must not navigate.
                          e.preventDefault()
                          e.stopPropagation()
                          toggleVersions(hit.document_id)
                        }}
                      >
                        {expandedVersions.has(hit.document_id)
                          ? 'Hide versions'
                          : `${hit.version_count} versions`}
                      </button>
                    )}
                  </div>
                </div>
                </>
              )
              const card = hit.workspace_id ? (
                <Link
                  to="/workspaces/$workspaceId/documents/$documentId"
                  params={{ workspaceId: hit.workspace_id, documentId: hit.document_id }}
                  className={cardClass}
                  data-testid={`search-hit-${hit.document_id}`}
                >
                  {inner}
                </Link>
              ) : (
                <div className={cardClass} data-testid={`search-hit-${hit.document_id}`}>
                  {inner}
                </div>
              )
              return (
                <div key={hit.document_id}>
                  {card}
                  {expandedVersions.has(hit.document_id) && (
                    <VersionsInline documentId={hit.document_id} />
                  )}
                </div>
              )
            })}
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
  onClearScalar,
  workspaceName,
}: {
  params: SearchParams
  onRemove: (key: string, value: string) => void
  onClearScalar: (key: string) => void
  workspaceName: (id: string) => string
}) {
  // `scalar` chips clear the whole param (workspace/date/size); the
  // others remove a single value from a repeated param.
  type ChipDef = { key: string; display: string; value: string; scalar?: boolean }
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

  if (params.workspace_id) {
    chips.push({ key: 'workspace_id', display: `Workspace: ${workspaceName(params.workspace_id)}`, value: params.workspace_id, scalar: true })
  }
  if (params.created_after) {
    chips.push({ key: 'created_after', display: `From: ${params.created_after}`, value: params.created_after, scalar: true })
  }
  if (params.created_before) {
    chips.push({ key: 'created_before', display: `To: ${params.created_before}`, value: params.created_before, scalar: true })
  }
  if (params.size) {
    const label = SIZE_OPTIONS.find((o) => o.value === params.size)?.label ?? params.size
    chips.push({ key: 'size', display: `Size: ${label}`, value: params.size, scalar: true })
  }

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
            onClick={() => (chip.scalar ? onClearScalar(chip.key) : onRemove(chip.key, chip.value))}
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
      created_after: str('created_after'),
      created_before: str('created_before'),
      size: str('size'),
      sort: str('sort'),
      closed: arr('closed'),
    }
  },
})

function SaveSearchDialog({
  defaultName, saving, onClose, onSave,
}: {
  defaultName: string
  saving: boolean
  onClose: () => void
  onSave: (name: string, alertMe: boolean, intervalMinutes: number) => void
}) {
  const [name, setName] = useState(defaultName)
  const [alertMe, setAlertMe] = useState(false)
  const [interval, setInterval] = useState('15')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Save this search">
      <div className="space-y-3">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} data-testid="save-name" />
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={alertMe}
            onChange={(e) => setAlertMe(e.target.checked)}
            data-testid="save-alert-me"
          />
          Alert me when new documents match
        </label>
        {alertMe && (
          <Input
            label="Check every (minutes)"
            type="number"
            min={1}
            value={interval}
            onChange={(e) => setInterval(e.target.value)}
            data-testid="save-alert-interval"
          />
        )}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button
            onClick={() => onSave(name, alertMe, Math.max(1, Number(interval) || 15))}
            disabled={saving || !name.trim()}
            data-testid="save-confirm"
          >
            Save
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

// Inline version list under an expanded search hit. Lazy: mounts (and
// fetches) only when the user opens the "N versions" affordance —
// relevance ranking is untouched because the search response itself
// never changes.
function VersionsInline({ documentId }: { documentId: string }) {
  const { data: versions, isLoading, isError } = useQuery({
    queryKey: ['search-hit-versions', documentId],
    queryFn: () => getVersions(documentId),
    staleTime: 60_000,
  })

  if (isLoading) {
    return (
      <div className="ms-8 mt-1 space-y-1 rounded-md border border-border bg-muted/40 p-3" data-testid={`versions-panel-${documentId}`}>
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
      </div>
    )
  }
  if (isError || !versions) {
    return (
      <div className="ms-8 mt-1 rounded-md border border-border bg-muted/40 p-3 text-xs text-muted-foreground" data-testid={`versions-panel-${documentId}`}>
        Could not load versions.
      </div>
    )
  }
  return (
    <ul className="ms-8 mt-1 divide-y divide-border rounded-md border border-border bg-muted/40" data-testid={`versions-panel-${documentId}`}>
      {versions.map((v) => (
        <li key={v.id} className="flex items-center gap-3 px-3 py-2 text-sm">
          <span className="font-medium">v{v.version_number}</span>
          {v.label ? <Badge variant="secondary">{v.label}</Badge> : null}
          <span className="min-w-0 flex-1 truncate text-muted-foreground">
            {v.change_summary || '—'}
          </span>
          <span className="whitespace-nowrap text-xs text-muted-foreground">
            {formatFileSize(v.size_bytes)}
          </span>
          <span className="whitespace-nowrap text-xs text-muted-foreground">
            {v.created_by_name ? `${v.created_by_name} · ` : ''}{formatRelativeTime(v.created_at)}
          </span>
        </li>
      ))}
    </ul>
  )
}
