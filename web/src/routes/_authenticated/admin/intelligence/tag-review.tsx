import { useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Check, X } from 'lucide-react'

import {
  listPendingTagSuggestions,
  reviewTagSuggestions,
  type TagSuggestion,
} from '@/api/intelligence'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { EmptyState } from '@/components/ui/EmptyState'

const PAGE_SIZE = 50

function TagReviewQueuePage() {
  const qc = useQueryClient()
  const [minConf, setMinConf] = useState(0)
  const [page, setPage] = useState(0)

  const { data, isLoading } = useQuery({
    queryKey: ['admin-tag-suggestions', minConf, page],
    queryFn: () =>
      listPendingTagSuggestions({
        min_confidence: minConf,
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
      }),
    refetchInterval: 15_000,
  })

  // Group by document for batch review.
  const grouped = useMemo(() => {
    const map = new Map<string, TagSuggestion[]>()
    for (const s of data?.suggestions ?? []) {
      const arr = map.get(s.document_id) ?? []
      arr.push(s)
      map.set(s.document_id, arr)
    }
    return Array.from(map.entries())
  }, [data])

  const review = useMutation({
    mutationFn: ({
      documentId,
      actions,
    }: {
      documentId: string
      actions: { suggestion_id: string; action: 'accept' | 'reject' }[]
    }) => reviewTagSuggestions(documentId, actions),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin-tag-suggestions'] })
      toast.success('Reviewed')
    },
    onError: () => toast.error('Review failed'),
  })

  const total = data?.total ?? 0
  const showing = data?.suggestions?.length ?? 0
  const hasMore = (page + 1) * PAGE_SIZE < total

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="Tag review queue"
        description={`${total} pending suggestion${total === 1 ? '' : 's'} across the tenant`}
      />

      <div className="mt-4 flex items-center gap-3 text-sm">
        <label className="text-muted-foreground">Min confidence</label>
        <input
          type="range"
          min={0}
          max={1}
          step={0.05}
          value={minConf}
          onChange={(e) => {
            setMinConf(Number(e.target.value))
            setPage(0)
          }}
          className="w-48"
        />
        <span className="tabular-nums text-muted-foreground">{minConf.toFixed(2)}</span>
      </div>

      <div className="mt-6 space-y-4">
        {isLoading ? (
          <div className="text-sm text-muted-foreground">Loading…</div>
        ) : grouped.length === 0 ? (
          <EmptyState title="Inbox zero" description="No pending tag suggestions in this range." />
        ) : (
          grouped.map(([docId, items]) => (
            <DocumentGroup
              key={docId}
              documentId={docId}
              items={items}
              busy={review.isPending}
              onReview={(actions) => review.mutate({ documentId: docId, actions })}
            />
          ))
        )}
      </div>

      {(page > 0 || hasMore) && (
        <div className="mt-4 flex items-center justify-between text-sm">
          <span className="text-muted-foreground">
            Showing {showing} of {total}
          </span>
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={page === 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
            >
              Previous
            </Button>
            <Button size="sm" variant="outline" disabled={!hasMore} onClick={() => setPage((p) => p + 1)}>
              Next
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

function DocumentGroup({
  documentId,
  items,
  busy,
  onReview,
}: {
  documentId: string
  items: TagSuggestion[]
  busy: boolean
  onReview: (actions: { suggestion_id: string; action: 'accept' | 'reject' }[]) => void
}) {
  const acceptAll = () => onReview(items.map((s) => ({ suggestion_id: s.id, action: 'accept' })))
  const rejectAll = () => onReview(items.map((s) => ({ suggestion_id: s.id, action: 'reject' })))

  return (
    <div className="rounded border border-border">
      <div className="flex items-center justify-between border-b border-border px-4 py-2">
        <div className="text-sm">
          <span className="font-medium">Document </span>
          <a
            href={`/workspaces/_/documents/${documentId}`}
            className="text-violet-600 hover:underline"
          >
            {documentId.slice(0, 8)}…
          </a>
          <span className="ml-2 text-muted-foreground">{items.length} pending</span>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="outline" disabled={busy} onClick={acceptAll}>
            Accept all
          </Button>
          <Button size="sm" variant="ghost" disabled={busy} onClick={rejectAll}>
            Reject all
          </Button>
        </div>
      </div>
      <ul className="divide-y divide-zinc-100 dark:divide-zinc-900">
        {items.map((s) => {
          const pct = Math.round(s.confidence * 100)
          return (
            <li key={s.id} className="flex items-center gap-3 px-4 py-2 text-sm">
              <span className="flex-1 truncate" title={s.tag_name}>
                {s.tag_name}
              </span>
              <Badge variant="outline" className="text-[10px] uppercase">
                {s.source}
              </Badge>
              <span className="w-12 text-right tabular-nums text-muted-foreground">{pct}%</span>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => onReview([{ suggestion_id: s.id, action: 'accept' }])}
                aria-label={`Accept ${s.tag_name}`}
              >
                <Check className="h-4 w-4 text-success" />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => onReview([{ suggestion_id: s.id, action: 'reject' }])}
                aria-label={`Reject ${s.tag_name}`}
              >
                <X className="h-4 w-4 text-muted-foreground" />
              </Button>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/tag-review')({
  component: TagReviewQueuePage,
})
