import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import {
  getOcrQualityStats,
  listOcrReviewQueue,
  type QualityGrade,
} from '@/api/ocr-quality'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'

const GRADES: QualityGrade[] = ['excellent', 'good', 'fair', 'poor']

const GRADE_VARIANT: Record<QualityGrade, string> = {
  excellent: 'active',
  good:      'superseded',
  fair:      'in_review',
  poor:      'disposed',
}

const PAGE_SIZE = 50

function OcrReviewPage() {
  const [grade, setGrade] = useState<string>('')
  const [page, setPage] = useState(0)

  const { data: stats } = useQuery({
    queryKey: ['ocr-quality-stats'],
    queryFn: getOcrQualityStats,
    refetchInterval: 30_000,
  })

  const { data: queue, isLoading } = useQuery({
    queryKey: ['ocr-review-queue', grade, page],
    queryFn: () =>
      listOcrReviewQueue({ grade: grade || undefined, limit: PAGE_SIZE, offset: page * PAGE_SIZE }),
    refetchInterval: 30_000,
  })

  const total = queue?.total ?? 0
  const hasMore = (page + 1) * PAGE_SIZE < total

  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHeader
        title="OCR review queue"
        description="Documents whose OCR pipeline flagged at least one page for human review (ADR 0057)."
      />

      {stats && (
        <div className="mt-6 grid grid-cols-4 gap-4">
          <Metric label="Documents scanned" value={stats.total_documents} />
          <Metric label="Open review pages" value={stats.open_review_pages} />
          <Metric label="Auto-retried docs" value={stats.auto_retried_documents} />
          <Metric label="Poor-grade docs" value={stats.documents_by_grade?.poor ?? 0} />
        </div>
      )}

      {stats && (
        <div className="mt-6 rounded border border-border p-4">
          <h2 className="mb-3 text-sm font-medium">Documents by grade</h2>
          <div className="grid grid-cols-4 gap-3">
            {GRADES.map((g) => (
              <button
                key={g}
                onClick={() => {
                  setGrade(grade === g ? '' : g)
                  setPage(0)
                }}
                className={[
                  'rounded border px-3 py-2 text-left',
                  grade === g
                    ? 'border-violet-500 bg-violet-50 dark:bg-violet-950'
                    : 'border-border',
                ].join(' ')}
              >
                <div className="text-xs uppercase tracking-wide text-muted-foreground">{g}</div>
                <div className="mt-1 text-2xl font-semibold tabular-nums">
                  {stats.documents_by_grade?.[g] ?? 0}
                </div>
              </button>
            ))}
          </div>
        </div>
      )}

      <div className="mt-8 rounded border border-border">
        <div className="flex items-center justify-between border-b border-border px-4 py-2 text-sm">
          <div className="font-medium">Documents needing review</div>
          {grade && (
            <Button size="sm" variant="ghost" onClick={() => { setGrade(''); setPage(0) }}>
              Clear filter
            </Button>
          )}
        </div>
        <table className="w-full text-sm">
          <thead className="text-left text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-2">Document</th>
              <th className="px-4 py-2">Grade</th>
              <th className="px-4 py-2 text-right">Avg score</th>
              <th className="px-4 py-2 text-right">Pages flagged</th>
              <th className="px-4 py-2">Scored</th>
            </tr>
          </thead>
          <tbody>
            {isLoading && (
              <tr><td colSpan={5} className="px-4 py-4 text-center text-muted-foreground">Loading…</td></tr>
            )}
            {!isLoading && (queue?.items ?? []).length === 0 && (
              <tr><td colSpan={5} className="px-4 py-4 text-center text-muted-foreground">Inbox zero.</td></tr>
            )}
            {(queue?.items ?? []).map((it) => (
              <tr key={it.document_id} className="border-t border-border">
                <td className="px-4 py-2 font-mono text-xs text-muted-foreground">{it.document_id.slice(0, 8)}…</td>
                <td className="px-4 py-2">
                  <Badge variant={GRADE_VARIANT[it.quality_grade]}>{it.quality_grade}</Badge>
                </td>
                <td className="px-4 py-2 text-right tabular-nums">{(it.avg_score * 100).toFixed(0)}%</td>
                <td className="px-4 py-2 text-right tabular-nums">
                  {it.pages_needing_review}/{it.total_pages}
                </td>
                <td className="px-4 py-2 text-muted-foreground">{new Date(it.scored_at).toLocaleString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {(page > 0 || hasMore) && (
          <div className="flex items-center justify-between border-t border-border px-4 py-2 text-sm">
            <span className="text-muted-foreground">Showing {(queue?.items?.length ?? 0)} of {total}</span>
            <div className="flex gap-2">
              <Button size="sm" variant="outline" disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}>
                Previous
              </Button>
              <Button size="sm" variant="outline" disabled={!hasMore} onClick={() => setPage((p) => p + 1)}>
                Next
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded border border-border p-4">
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value.toLocaleString()}</div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/ocr-review')({
  component: OcrReviewPage,
})
