import { useTranslation } from 'react-i18next'
import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import {
  listIngestionItems,
  listReviewQueue,
  resolveReviewItem,
  type ResolveBody,
  type ReviewItem,
} from '@/api/ingestion'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/shadcn/tabs'
import { toast } from 'sonner'

// Pre-commit ingestion (WS3) + native review queue (WS4). Mirrors the OCR
// config/queue page: one tab for the human triage queue, one read-only tab for
// the staging pipeline. Tab state via ?tab=.
type Tab = 'review' | 'items'
interface S {
  tab?: Tab
}

function IngestionPage() {
  const { t } = useTranslation('common')
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'review'
  return (
    <div>
      <PageHeader
        title={t('adm.ing.title')}
        description={t('adm.ing.description')}
      />
      <Tabs value={active} onValueChange={(v) => navigate({ to: '/admin/ingestion', search: { tab: v as Tab } })}>
        <TabsList>
          <TabsTrigger value="review">{t('adm.ing.tab_review')}</TabsTrigger>
          <TabsTrigger value="items">{t('adm.ing.tab_items')}</TabsTrigger>
        </TabsList>
        <TabsContent value="review" className="mt-4">
          <ReviewQueueTab />
        </TabsContent>
        <TabsContent value="items" className="mt-4">
          <StagedItemsTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function ReviewQueueTab() {
  const { t } = useTranslation('common')
  const q = useInfiniteQuery({
    queryKey: ['review-queue', 'pending'],
    queryFn: ({ pageParam }) => listReviewQueue({ status: 'pending', cursor: pageParam, limit: 50 }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor || undefined,
    refetchInterval: 30_000,
  })
  const items = q.data?.pages.flatMap((p) => p.items) ?? []

  if (q.isLoading) return <p className="text-sm text-muted-foreground">{t('adm.ing.loading')}</p>
  if (q.error) return <p className="text-sm text-destructive">{t('adm.ing.err_queue')}</p>
  if (items.length === 0) {
    return (
      <div className="rounded border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
        {t('adm.ing.empty_queue')}
      </div>
    )
  }

  return (
    <div className="space-y-3">
      {items.map((it) => (
        <ReviewCard key={it.id} item={it} />
      ))}
      {q.hasNextPage && (
        <div className="text-center">
          <Button variant="ghost" size="sm" disabled={q.isFetchingNextPage} onClick={() => q.fetchNextPage()}>
            {q.isFetchingNextPage ? t('adm.ing.loading') : t('adm.ing.load_more')}
          </Button>
        </div>
      )}
    </div>
  )
}

function ReviewCard({ item }: { item: ReviewItem }) {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const [target, setTarget] = useState('')
  const [key, setKey] = useState(item.extracted_external_key)

  const resolve = useMutation({
    mutationFn: (body: ResolveBody) => resolveReviewItem(item.id, body),
    onSuccess: (res) => {
      toast.success(t('adm.ing.resolved', { status: t(`adm.ing.s_${res.status}`, { defaultValue: res.status }) }))
      qc.invalidateQueries({ queryKey: ['review-queue'] })
    },
    onError: () => toast.error(t('adm.ing.err_resolve')),
  })

  return (
    <div className="rounded border border-border p-3 text-sm">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="min-w-0">
          <span className="font-medium">{item.target_customer_ref || '—'}</span>
          <span className="ms-2 text-xs text-muted-foreground">
            {t('adm.ing.reason_line', {
              reason: t(`adm.ing.r_${item.reason}`, { defaultValue: item.reason.replace(/_/g, ' ') }),
              pct: (item.confidence * 100).toFixed(0),
            })}
          </span>
        </div>
        <Badge variant="secondary">{item.document_class || t('adm.ing.unclassified')}</Badge>
      </div>
      {item.extracted_external_key && (
        <p className="mb-1 text-xs">
          {t('adm.ing.extracted_key')}<span dir="ltr" className="font-mono">{item.extracted_external_key}</span>
          {item.suggested_match_document_id && (
            <span className="ms-2 text-muted-foreground">
              {t('adm.ing.suggested_match', { id: item.suggested_match_document_id.slice(0, 8) })}
            </span>
          )}
        </p>
      )}
      {item.ocr_text && (
        <pre className="mb-2 max-h-24 overflow-auto rounded bg-muted p-2 text-[11px] text-muted-foreground">
          {item.ocr_text}
        </pre>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <input
          dir="ltr"
          aria-label={t('adm.ing.target_aria')}
          placeholder={t('adm.ing.target_ph')}
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          className="w-56 rounded border border-input bg-background px-2 py-1 text-xs"
        />
        <Button
          size="sm"
          disabled={!target || resolve.isPending}
          onClick={() => resolve.mutate({ decision: 'new_version', target_document_id: target })}
        >
          {t('adm.ing.new_version')}
        </Button>
        <input
          dir="ltr"
          aria-label={t('adm.ing.key_aria')}
          placeholder={t('adm.ing.key_ph')}
          value={key}
          onChange={(e) => setKey(e.target.value)}
          className="w-44 rounded border border-input bg-background px-2 py-1 text-xs"
        />
        <Button
          size="sm"
          variant="outline"
          disabled={resolve.isPending}
          onClick={() => resolve.mutate({ decision: 'new_document', external_key: key })}
        >
          {t('adm.ing.new_document')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          className="text-destructive"
          disabled={resolve.isPending}
          onClick={() => resolve.mutate({ decision: 'reject', notes: 'rejected in review' })}
        >
          {t('adm.ing.reject')}
        </Button>
      </div>
    </div>
  )
}

const STATUS_VARIANT: Record<string, string> = {
  received: 'info',
  ocr_running: 'info',
  processed: 'info',
  routed: 'success',
  committed: 'success',
  needs_review: 'in_review',
  rejected: 'disposed',
}

function StagedItemsTab() {
  const { t } = useTranslation('common')
  const { data, isLoading, error } = useQuery({
    queryKey: ['ingestion-items'],
    queryFn: () => listIngestionItems({ limit: 100 }),
    refetchInterval: 30_000,
  })
  if (isLoading) return <p className="text-sm text-muted-foreground">{t('adm.ing.loading')}</p>
  if (error) return <p className="text-sm text-destructive">{t('adm.ing.err_items')}</p>
  const items = data?.items ?? []
  if (items.length === 0) {
    return (
      <div className="rounded border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
        {t('adm.ing.empty_items')}
      </div>
    )
  }
  return (
    <div className="overflow-x-auto rounded border border-border">
      <table className="w-full text-sm">
        <thead className="text-xs uppercase text-muted-foreground">
          <tr>
            <th className="px-4 py-2 text-start">{t('adm.ing.col_customer')}</th>
            <th className="px-4 py-2 text-start">{t('adm.ing.col_class')}</th>
            <th className="px-4 py-2 text-start">{t('adm.ing.col_key')}</th>
            <th className="px-4 py-2 text-end">{t('adm.ing.col_confidence')}</th>
            <th className="px-4 py-2 text-start">{t('adm.ing.col_status')}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((it) => (
            <tr key={it.id} className="border-t border-border">
              <td className="px-4 py-2">{it.target_customer_ref || '—'}</td>
              <td className="px-4 py-2">{it.document_class || '—'}</td>
              <td dir="ltr" className="px-4 py-2 font-mono text-xs">{it.extracted_external_key || '—'}</td>
              <td className="px-4 py-2 text-end tabular-nums">
                {it.confidence ? t('adm.ing.pct', { value: (it.confidence * 100).toFixed(0) }) : '—'}
              </td>
              <td className="px-4 py-2">
                <Badge variant={STATUS_VARIANT[it.status] ?? 'secondary'}>{t(`adm.ing.s_${it.status}`, { defaultValue: it.status.replace(/_/g, ' ') })}</Badge>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/ingestion')({
  component: IngestionPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return t === 'review' || t === 'items' ? { tab: t } : {}
  },
})
