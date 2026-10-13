import { useTranslation } from 'react-i18next'
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { AlertTriangle, CheckCircle2, FileSearch, MessageSquarePlus } from 'lucide-react'

import {
  getDocumentOcrQuality,
  reviewOcrPage,
  type OcrPageScore,
  type QualityGrade,
} from '@/api/ocr-quality'
import { readErrorMessage } from '@/api/client'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/shadcn/sheet'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Document being reviewed. When null, the sheet renders no content. */
  documentId: string | null
  /**
   * Optional grade/score hint already known from the admin queue row, used
   * for the header before the per-doc fetch resolves so the user sees
   * something immediately on open instead of a blank skeleton header.
   */
  hint?: { grade?: QualityGrade; flagged?: number; total?: number }
}

const GRADE_DOT: Record<QualityGrade, string> = {
  excellent: 'bg-success',
  good:      'bg-info',
  fair:      'bg-warning',
  poor:      'bg-destructive',
}

interface ReviewVars {
  pageNumber: number
  versionId: string
  note?: string
}

interface ReviewCtx {
  previous?: { summary?: unknown; pages: OcrPageScore[] }
}

/**
 * Page-level OCR review drawer surfaced from the admin review queue.
 * Distinct from the in-context `<OcrQualityPanel/>` on the document
 * detail page in two ways:
 *   1. Supports an optional per-page reviewer note (backend already
 *      accepts it; the in-context panel doesn't expose it).
 *   2. Invalidates the admin queue + stats queries on settle so the
 *      counts on the OCR review queue page update without a manual
 *      reload after a review action.
 *
 * Review mutation is OPTIMISTIC: onMutate marks the page reviewed in
 * the cache so the row updates instantly; onError rolls back and
 * surfaces the backend message via readErrorMessage.
 */
export function OcrPageReviewDrawer({ open, onOpenChange, documentId, hint }: Props) {
  const { t } = useTranslation('common')
  const qc = useQueryClient()

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['ocr-quality', documentId],
    queryFn: () => getDocumentOcrQuality(documentId!),
    enabled: open && !!documentId,
  })

  const review = useAppMutation<OcrPageScore, unknown, ReviewVars, ReviewCtx>({
    mutationFn: ({ pageNumber, versionId, note }) =>
      reviewOcrPage(documentId!, versionId, pageNumber, note),
    onMutate: async ({ pageNumber }) => {
      await qc.cancelQueries({ queryKey: ['ocr-quality', documentId] })
      const previous = qc.getQueryData<{ summary?: unknown; pages: OcrPageScore[] }>(['ocr-quality', documentId])
      if (previous) {
        qc.setQueryData(['ocr-quality', documentId], {
          ...previous,
          pages: previous.pages.map((p) =>
            p.page_number === pageNumber ? { ...p, reviewed: true } : p,
          ),
        })
      }
      return { previous }
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.previous) {
        qc.setQueryData(['ocr-quality', documentId], ctx.previous)
      }
      toast.error(readErrorMessage(err) ?? t('adm.ocrr.err_mark'))
    },
    onSuccess: () => {
      toast.success(t('adm.ocrr.marked'))
    },
    onSettled: () => {
      // Re-fetch the source-of-truth for the per-doc view AND the
      // admin queue/stats so counts move without a manual reload.
      qc.invalidateQueries({ queryKey: ['ocr-quality', documentId] })
      qc.invalidateQueries({ queryKey: ['ocr-review-queue'] })
      qc.invalidateQueries({ queryKey: ['ocr-quality-stats'] })
    },
  })

  // Show flagged-but-unreviewed first, then reviewed flagged, then OK pages.
  // Keeps the user's eye on the work item.
  const sortedPages = useMemo(() => {
    const pages = data?.pages ?? []
    return [...pages].sort((a, b) => {
      const pri = (p: OcrPageScore) =>
        p.needs_review && !p.reviewed ? 0 : p.reviewed ? 1 : 2
      const dp = pri(a) - pri(b)
      if (dp !== 0) return dp
      return a.page_number - b.page_number
    })
  }, [data?.pages])

  const summary = data?.summary
  const flaggedRemaining = (data?.pages ?? []).filter((p) => p.needs_review && !p.reviewed).length

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full max-w-2xl flex-col overflow-hidden sm:max-w-2xl" data-testid="ocr-page-review-drawer">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <FileSearch className="h-4 w-4 text-violet-500" />
            {t('adm.ocrr.drawer_title')}
          </SheetTitle>
          <SheetDescription>
            {documentId ? (
              <span dir="ltr" className="font-mono text-xs">{documentId}</span>
            ) : (
              <span>{t('adm.ocrr.no_document')}</span>
            )}
          </SheetDescription>
        </SheetHeader>

        {documentId && (
          <div className="mt-4 flex items-center gap-3 rounded-md border border-border bg-muted/30 px-3 py-2 text-sm">
            <span aria-hidden className={`inline-block h-2 w-2 rounded-full ${GRADE_DOT[(summary?.quality_grade ?? hint?.grade ?? 'fair') as QualityGrade]}`} />
            <span className="tracking-wide">{t(`adm.ocrr.g_${summary?.quality_grade ?? hint?.grade ?? ''}`, { defaultValue: summary?.quality_grade ?? hint?.grade ?? '—' })}</span>
            {summary && (
              <span className="text-muted-foreground tabular-nums">({summary.avg_score.toFixed(2)})</span>
            )}
            <span className="ms-auto text-xs text-muted-foreground">
              {summary
                ? t('adm.ocrr.need_review', { flagged: summary.pages_needing_review, total: summary.total_pages })
                : hint
                ? t('adm.ocrr.need_review', { flagged: hint.flagged ?? '—', total: hint.total ?? '—' })
                : ''}
            </span>
          </div>
        )}

        <div className="mt-4 flex-1 overflow-auto" data-testid="ocr-page-review-body">
          {isLoading && (
            <div className="space-y-2">
              {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-14 w-full" />)}
            </div>
          )}

          {isError && (
            <div className="flex items-start gap-3 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
              <div>
                <p className="font-medium text-destructive">{t('adm.ocrr.err_detail')}</p>
                <p className="text-muted-foreground">{(error as Error)?.message ?? t('adm.ocrr.err_unknown')}</p>
              </div>
            </div>
          )}

          {!isLoading && !isError && (data?.pages?.length ?? 0) === 0 && (
            <EmptyState
              title={t('adm.ocrr.no_scores')}
              description={t('adm.ocrr.no_scores_desc')}
            />
          )}

          {!isLoading && !isError && (data?.pages?.length ?? 0) > 0 && (
            <ul className="divide-y divide-border rounded-md border border-border" data-testid="ocr-page-review-list">
              {sortedPages.map((p) => (
                <ReviewRow
                  key={p.id}
                  page={p}
                  disabled={review.isPending || !summary?.version_id}
                  onSubmit={(note) =>
                    review.mutate({
                      pageNumber: p.page_number,
                      versionId: summary!.version_id,
                      note: note || undefined,
                    })
                  }
                />
              ))}
            </ul>
          )}
        </div>

        {summary && flaggedRemaining === 0 && (data?.pages?.length ?? 0) > 0 && (
          <div className="mt-4 flex items-center gap-2 rounded-md border border-success/40 bg-success/5 px-3 py-2 text-sm text-success">
            <CheckCircle2 className="h-4 w-4" />
            {t('adm.ocrr.all_reviewed')}
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}

interface ReviewRowProps {
  page: OcrPageScore
  disabled: boolean
  onSubmit: (note: string) => void
}

function ReviewRow({ page, disabled, onSubmit }: ReviewRowProps) {
  const { t } = useTranslation('common')
  const [noteOpen, setNoteOpen] = useState(false)
  const [note, setNote] = useState('')

  const pct = Math.round(page.overall_score * 100)
  const scoreColor =
    page.overall_score >= 0.9 ? 'bg-success'
    : page.overall_score >= 0.75 ? 'bg-info'
    : page.overall_score >= 0.6 ? 'bg-warning'
    : 'bg-destructive'

  return (
    <li className="px-3 py-3 text-sm" data-testid={`ocr-page-row-${page.page_number}`}>
      <div className="flex items-center gap-3">
        <span className="w-12 shrink-0 text-xs text-muted-foreground tabular-nums">{t('adm.ocrr.page_n', { n: page.page_number })}</span>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <div className="h-1.5 w-24 overflow-hidden rounded bg-muted">
            <div className={`h-full ${scoreColor}`} style={{ width: `${pct}%` }} />
          </div>
          <span className="w-10 text-end text-xs tabular-nums text-muted-foreground">{t('adm.ocrr.pct', { value: pct })}</span>
          {page.issues.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {page.issues.map((i) => (
                <Badge key={i} variant="in_review" className="text-[10px]">
                  {t(`adm.ocrr.i_${i}`, { defaultValue: i.replace(/_/g, ' ') })}
                </Badge>
              ))}
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {page.reviewed ? (
            <span className="inline-flex items-center gap-1 text-xs text-success">
              <CheckCircle2 className="h-3 w-3" /> {t('adm.ocrr.reviewed')}
            </span>
          ) : page.needs_review ? (
            <>
              {!noteOpen && (
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 px-2 text-xs"
                  disabled={disabled}
                  onClick={() => setNoteOpen(true)}
                  aria-label={t('adm.ocrr.add_note')}
                  data-testid={`ocr-page-add-note-${page.page_number}`}
                >
                  <MessageSquarePlus className="me-1 h-3 w-3" /> {t('adm.ocrr.note')}
                </Button>
              )}
              <Button
                size="sm"
                variant="outline"
                className="h-7 px-2 text-xs"
                disabled={disabled}
                onClick={() => onSubmit(note.trim())}
                data-testid={`ocr-page-mark-reviewed-${page.page_number}`}
              >
                {t('adm.ocrr.mark_reviewed')}
              </Button>
            </>
          ) : (
            <span className="text-xs text-muted-foreground">{t('adm.ocrr.ok')}</span>
          )}
        </div>
      </div>
      {noteOpen && !page.reviewed && (
        <div className="ms-12 mt-2 flex items-start gap-2">
          <textarea
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
            placeholder={t('adm.ocrr.note_ph')}
            maxLength={500}
            className="flex-1 rounded border border-border bg-background p-2 text-xs"
            data-testid={`ocr-page-note-${page.page_number}`}
          />
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2 text-xs"
            onClick={() => { setNoteOpen(false); setNote('') }}
            disabled={disabled}
          >
            {t('adm.ocrr.cancel')}
          </Button>
        </div>
      )}
      {page.reviewed && page.review_note && (
        <div className="ms-12 mt-1 text-xs italic text-muted-foreground" data-testid={`ocr-page-note-display-${page.page_number}`}>
          “{page.review_note}”
        </div>
      )}
    </li>
  )
}
