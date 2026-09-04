import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { CheckCircle2, Gauge } from 'lucide-react'

import {
  getDocumentOcrQuality,
  reviewOcrPage,
  type OcrPageScore,
  type QualityGrade,
} from '@/api/ocr-quality'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'

interface Props {
  documentId: string
  /** Optional: parent (e.g. PDF viewer) handler — clicking a page row
   * scrolls/highlights that page in the viewer. */
  onJumpToPage?: (page: number) => void
}

const GRADE_DOT: Record<QualityGrade, string> = {
  excellent: 'bg-success',
  good:      'bg-info',
  fair:      'bg-warning',
  poor:      'bg-destructive',
}

const ISSUE_LABEL: Record<string, string> = {
  low_confidence: 'low confidence',
  sparse_text:    'sparse text',
  skewed:         'skewed',
  noisy:          'noisy',
  garbled:        'garbled',
}

export function OcrQualityPanel({ documentId, onJumpToPage }: Props) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['ocr-quality', documentId],
    queryFn: () => getDocumentOcrQuality(documentId),
  })

  const review = useAppMutation({
    mutationFn: (page: OcrPageScore) =>
      reviewOcrPage(documentId, data!.summary!.version_id, page.page_number),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ocr-quality', documentId] })
      toast.success('Page marked reviewed')
    },
    onError: () => toast.error('Review failed'),
  })

  const reviewAll = useAppMutation({
    mutationFn: async () => {
      const versionId = data!.summary!.version_id
      const pending = (data?.pages ?? []).filter((p) => p.needs_review && !p.reviewed)
      for (const p of pending) {
        await reviewOcrPage(documentId, versionId, p.page_number)
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ocr-quality', documentId] })
      toast.success('All flagged pages marked reviewed')
    },
    onError: () => toast.error('Bulk review failed'),
  })

  if (isLoading) return <div className="p-4 text-sm text-muted-foreground">Loading OCR quality…</div>
  if (!data?.summary) {
    return (
      <div className="rounded-lg bg-card p-4 text-sm text-muted-foreground shadow-neu-sm">
        OCR quality scoring hasn't run yet for this document.
      </div>
    )
  }

  const s = data.summary
  const pages = data.pages ?? []
  const flagged = pages.filter((p) => p.needs_review && !p.reviewed)

  return (
    <div className="rounded-lg bg-card shadow-neu">
      <div className="flex items-center justify-between border-b border-border px-4 py-3 text-sm">
        <div className="flex items-center gap-2 font-medium">
          <Gauge className="h-4 w-4 text-violet-500" />
          OCR quality
          <span aria-hidden className={`ms-2 inline-block h-2 w-2 rounded-full ${GRADE_DOT[s.quality_grade]}`} />
          <span className="uppercase tracking-wide">{s.quality_grade}</span>
          <span className="ms-1 text-muted-foreground tabular-nums">({s.avg_score.toFixed(2)})</span>
        </div>
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          <span>
            {s.pages_needing_review}/{s.total_pages} need review
          </span>
          {s.auto_retried && (
            <Badge variant="outline" className="text-[10px]">
              auto-retried
            </Badge>
          )}
        </div>
      </div>

      <table className="w-full text-sm">
        <thead className="text-start text-xs uppercase text-muted-foreground">
          <tr>
            <th className="px-4 py-2">Page</th>
            <th className="px-4 py-2">Score</th>
            <th className="px-4 py-2">Issues</th>
            <th className="px-4 py-2">Status</th>
            <th className="px-4 py-2"></th>
          </tr>
        </thead>
        <tbody>
          {pages.length === 0 && (
            <tr>
              <td colSpan={5} className="px-4 py-4 text-center text-muted-foreground">
                No per-page scores.
              </td>
            </tr>
          )}
          {pages.map((p) => (
            <tr
              key={p.id}
              className={[
                'border-t border-border',
                onJumpToPage ? 'cursor-pointer hover:bg-muted' : '',
              ].join(' ')}
              onClick={() => onJumpToPage?.(p.page_number)}
            >
              <td className="px-4 py-2 tabular-nums">{p.page_number}</td>
              <td className="px-4 py-2">
                <ScoreBar score={p.overall_score} />
              </td>
              <td className="px-4 py-2">
                {p.issues.length === 0 ? (
                  <span className="text-muted-foreground">—</span>
                ) : (
                  <div className="flex flex-wrap gap-1">
                    {p.issues.map((i) => (
                      <Badge key={i} variant="in_review" className="text-[10px]">
                        {ISSUE_LABEL[i] ?? i}
                      </Badge>
                    ))}
                  </div>
                )}
              </td>
              <td className="px-4 py-2">
                {p.reviewed ? (
                  <span className="inline-flex items-center gap-1 text-xs text-success">
                    <CheckCircle2 className="h-3 w-3" /> Reviewed
                  </span>
                ) : p.needs_review ? (
                  <span className="text-xs text-warning-strong">Needs review</span>
                ) : (
                  <span className="text-xs text-muted-foreground">OK</span>
                )}
              </td>
              <td className="px-4 py-2 text-end" onClick={(e) => e.stopPropagation()}>
                {p.needs_review && !p.reviewed && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={review.isPending}
                    onClick={() => review.mutate(p)}
                  >
                    Mark reviewed
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      {flagged.length > 1 && (
        <div className="flex justify-end border-t border-border px-4 py-2">
          <Button
            size="sm"
            variant="outline"
            disabled={reviewAll.isPending}
            onClick={() => reviewAll.mutate()}
          >
            Mark all {flagged.length} reviewed
          </Button>
        </div>
      )}
    </div>
  )
}

function ScoreBar({ score }: { score: number }) {
  const pct = Math.round(score * 100)
  const color =
    score >= 0.9 ? 'bg-success'
    : score >= 0.75 ? 'bg-info'
    : score >= 0.6 ? 'bg-warning'
    : 'bg-destructive'
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-24 overflow-hidden rounded bg-muted">
        <div className={`h-full ${color}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="w-10 text-end text-xs tabular-nums text-muted-foreground">{pct}%</span>
    </div>
  )
}
