import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useSearch } from '@/hooks/useSearch'
import { useSavedSearches, useCreateSavedSearch, useDeleteSavedSearch } from '@/hooks/useSavedSearches'
import { PageHeader } from '@/components/shared/PageHeader'
import { Input } from '@/components/ui/Input'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { Search, Bookmark, X } from 'lucide-react'
import toast from 'react-hot-toast'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'

function SearchPage() {
  const [query, setQuery] = useState('')
  const { data, isLoading } = useSearch({ query }, query.length >= 2)

  const { data: savedSearches } = useSavedSearches()
  const createSavedMut = useCreateSavedSearch()
  const deleteSavedMut = useDeleteSavedSearch()

  const handleSave = async () => {
    const name = prompt('Name this saved search:', query)
    if (!name?.trim() || !query) return
    try {
      await createSavedMut.mutateAsync({ name: name.trim(), query })
      toast.success('Search saved')
    } catch {
      toast.error('Failed to save')
    }
  }

  const handleDeleteSaved = async (id: string) => {
    try {
      await deleteSavedMut.mutateAsync(id)
    } catch {
      toast.error('Failed to delete')
    }
  }

  return (
    <div>
      <PageHeader title="Search" description="Find documents across all workspaces" />
      <div className="mb-4 flex gap-2">
        <div className="flex-1">
          <Input
            icon={<Search className="h-4 w-4" />}
            placeholder="Search documents..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            autoFocus
          />
        </div>
        <Button variant="ghost" onClick={handleSave} disabled={!query || createSavedMut.isPending} data-testid="save-search">
          <Bookmark className="mr-1 h-4 w-4" /> Save
        </Button>
      </div>

      {savedSearches && savedSearches.length > 0 && (
        <div className="mb-6 flex flex-wrap gap-2">
          {savedSearches.map((s) => (
            <div
              key={s.id}
              className="flex items-center gap-1 rounded-full border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-3 py-1 text-xs"
            >
              <button type="button" onClick={() => setQuery(s.query)} className="font-medium">
                {s.name}
              </button>
              <button
                type="button"
                onClick={() => handleDeleteSaved(s.id)}
                aria-label={`Delete saved search ${s.name}`}
                className="ml-1 text-[var(--color-text-secondary)] hover:text-[var(--color-danger)]"
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
      {data && (data.results?.length ?? 0) === 0 && query && (
        <EmptyState title="No results" description={`No documents match "${query}"`} />
      )}
      {data && (data.results?.length ?? 0) > 0 && (
        <div className="space-y-2">
          <p className="mb-3 text-sm text-[var(--color-text-secondary)]">
            {data.total_count} results in {data.latency_ms}ms
          </p>
          {(data.results ?? []).map((hit) => (
            <div key={hit.document_id} className="flex items-start gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
              <FileIcon mime={hit.mime_type} className="mt-0.5" />
              <div className="min-w-0 flex-1">
                <p className="font-medium" dangerouslySetInnerHTML={{
                  __html: hit.highlights?.title?.[0] || hit.title,
                }} />
                {hit.highlights?.content?.[0] && (
                  <p className="mt-1 text-sm text-[var(--color-text-secondary)]" dangerouslySetInnerHTML={{
                    __html: hit.highlights.content[0],
                  }} />
                )}
                <div className="mt-2 flex items-center gap-2">
                  <Badge variant={hit.lifecycle_state}>{hit.lifecycle_state}</Badge>
                  <span className="text-xs text-[var(--color-text-secondary)]">{formatFileSize(hit.size_bytes)}</span>
                  <span className="text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(hit.created_at)}</span>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
      {data?.facets && Object.keys(data.facets).length > 0 && (
        <div className="mt-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
          <h3 className="mb-3 text-sm font-semibold">Facets</h3>
          <div className="grid grid-cols-3 gap-4">
            {Object.entries(data.facets).map(([name, buckets]) => (
              <div key={name}>
                <p className="mb-1 text-xs font-medium text-[var(--color-text-secondary)]">{name}</p>
                {buckets.slice(0, 5).map((b) => (
                  <div key={b.value} className="flex justify-between text-xs">
                    <span>{b.value}</span>
                    <span className="text-[var(--color-text-secondary)]">{b.count}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/search')({ component: SearchPage })
