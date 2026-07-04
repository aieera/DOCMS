import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { AlertTriangle, Check, Pencil } from 'lucide-react'

import { getOCR, correctOCRPage, parseOCRBoxes, type OCRPage, type OCRBox } from '@/api/ocr'
import { useAppMutation } from '@/hooks/useAppMutation'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { RerunOcrButton } from './RerunOcrButton'

// Recognised-text review: a per-page confidence heat-map over the OCR regions
// plus an inline manual-correction field. Reads the persisted ocr_results via
// GET .../ocr (engine + per-box confidence already included) and writes
// corrections via PATCH .../ocr/{page}. The engine label distinguishes
// printed (surya/paddle) from handwriting (trocr / trocr+surya).

// Map confidence 0..1 to a hue: low = red (hot), high = green. Used both for
// the heat-map fill and the per-line confidence chips.
function confColor(c: number, alpha = 0.35): string {
  const clamped = Math.max(0, Math.min(1, c))
  const hue = Math.round(clamped * 120)
  return `hsla(${hue}, 80%, 45%, ${alpha})`
}

function ConfidenceHeatmap({ boxes }: { boxes: OCRBox[] }) {
  // Derive the page extent from the boxes themselves (the engine emits pixel
  // coords without page dimensions), then place each region as a percentage
  // so the map scales to its container.
  const ext = useMemo(() => {
    const w = Math.max(1, ...boxes.map((b) => b.x2))
    const h = Math.max(1, ...boxes.map((b) => b.y2))
    return { w, h }
  }, [boxes])
  if (boxes.length === 0) {
    return <p className="text-xs text-muted-foreground">No region boxes for this page.</p>
  }
  return (
    <div
      className="relative w-full overflow-hidden rounded border border-border bg-muted/20"
      style={{ aspectRatio: `${ext.w} / ${ext.h}` }}
      data-testid="ocr-heatmap"
    >
      {boxes.map((b, i) => (
        <div
          key={i}
          title={`${b.text} — ${(b.confidence * 100).toFixed(0)}%`}
          className="absolute rounded-[1px]"
          style={{
            left: `${(b.x1 / ext.w) * 100}%`,
            top: `${(b.y1 / ext.h) * 100}%`,
            width: `${((b.x2 - b.x1) / ext.w) * 100}%`,
            height: `${((b.y2 - b.y1) / ext.h) * 100}%`,
            backgroundColor: confColor(b.confidence),
            outline: `1px solid ${confColor(b.confidence, 0.7)}`,
          }}
        />
      ))}
    </div>
  )
}

function PageBlock({
  documentId, versionId, page, canCorrect,
}: {
  documentId: string
  versionId: string
  page: OCRPage
  canCorrect: boolean
}) {
  const qc = useQueryClient()
  const boxes = useMemo(() => parseOCRBoxes(page.bounding_boxes), [page.bounding_boxes])
  const baseline = page.corrected_text ?? page.text_content ?? ''
  const [draft, setDraft] = useState(baseline)
  const [editing, setEditing] = useState(false)

  const save = useAppMutation({
    mutationFn: () => correctOCRPage(documentId, versionId, page.page_number, draft),
    onSuccess: () => {
      toast.success(`Saved correction for page ${page.page_number}`)
      setEditing(false)
      qc.invalidateQueries({ queryKey: ['ocr', documentId, versionId] })
    },
    defaultErrorMessage: 'Could not save correction',
  })

  const pct = (page.confidence * 100).toFixed(1)
  return (
    <div className="rounded-lg border border-border p-3" data-testid={`ocr-page-${page.page_number}`}>
      <div className="mb-2 flex items-center gap-2 text-sm">
        <span className="font-medium">Page {page.page_number}</span>
        {page.engine && (
          <span className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">{page.engine}</span>
        )}
        <span
          className="rounded px-1.5 py-0.5 text-xs font-medium"
          style={{ backgroundColor: confColor(page.confidence, 0.25) }}
        >
          {pct}%
        </span>
        {page.corrected_text != null && (
          <span className="inline-flex items-center gap-1 text-xs text-emerald-600">
            <Check className="h-3 w-3" /> corrected
          </span>
        )}
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <ConfidenceHeatmap boxes={boxes} />

        <div className="flex flex-col gap-2">
          {editing ? (
            <>
              <textarea
                className="min-h-[8rem] w-full resize-y rounded border border-border bg-background p-2 font-mono text-xs"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                data-testid={`ocr-correct-input-${page.page_number}`}
              />
              <div className="flex gap-2">
                <Button size="sm" onClick={() => save.mutate(undefined)} disabled={save.isPending}>
                  {save.isPending ? <Spinner className="h-3 w-3" /> : <Check className="h-3 w-3" />}
                  Save
                </Button>
                <Button size="sm" variant="ghost" onClick={() => { setDraft(baseline); setEditing(false) }}>
                  Cancel
                </Button>
              </div>
            </>
          ) : (
            <>
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded border border-border bg-muted/20 p-2 font-mono text-xs">
                {baseline || <span className="text-muted-foreground">— no text —</span>}
              </pre>
              {canCorrect && (
                <Button size="sm" variant="outline" className="self-start" onClick={() => setEditing(true)}
                  data-testid={`ocr-correct-edit-${page.page_number}`}>
                  <Pencil className="h-3 w-3" /> Correct text
                </Button>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

interface Props {
  documentId: string
  versionId: string
  /** Reviewer may edit text (owner/admin/compliance) — gates the correction field. */
  canCorrect?: boolean
  /** Reviewer may re-run OCR — gates the engine selector. */
  canRerun?: boolean
}

export function OcrTextReview({ documentId, versionId, canCorrect = false, canRerun = false }: Props) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['ocr', documentId, versionId],
    queryFn: () => getOCR(documentId, versionId),
  })

  if (isLoading) {
    return <div className="flex items-center gap-2 p-4 text-sm text-muted-foreground"><Spinner className="h-4 w-4" /> Loading recognised text…</div>
  }
  if (isError) {
    return (
      <div className="flex items-start gap-2 rounded border border-destructive/40 bg-destructive/5 p-3 text-sm">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
        <span>{(error as Error)?.message ?? 'Could not load OCR results.'}</span>
      </div>
    )
  }

  const pages = data?.pages ?? []
  return (
    <div className="space-y-3" data-testid="ocr-text-review">
      <div className="flex items-center gap-3 text-sm">
        <span className="text-muted-foreground">{pages.length} page{pages.length === 1 ? '' : 's'}</span>
        {data && data.avg_confidence > 0 && (
          <span
            className="rounded px-1.5 py-0.5 text-xs font-medium"
            style={{ backgroundColor: confColor(data.avg_confidence, 0.25) }}
          >
            avg {(data.avg_confidence * 100).toFixed(1)}%
          </span>
        )}
        <span className="ms-auto inline-flex items-center gap-2 text-xs text-muted-foreground">
          <span className="inline-block h-3 w-6 rounded" style={{ background: confColor(0.15, 0.7) }} /> low
          <span className="inline-block h-3 w-6 rounded" style={{ background: confColor(0.95, 0.7) }} /> high
        </span>
        {canRerun && (
          <RerunOcrButton documentId={documentId} versionId={versionId} canRerun={canRerun} label="Re-run" />
        )}
      </div>

      {pages.length === 0 ? (
        <p className="rounded border border-dashed border-border p-4 text-sm text-muted-foreground">
          No recognised text yet. {canRerun && 'Use “Re-run” to OCR this version (pick Handwriting for ink).'}
        </p>
      ) : (
        pages.map((p) => (
          <PageBlock key={p.id} documentId={documentId} versionId={versionId} page={p} canCorrect={canCorrect} />
        ))
      )}
    </div>
  )
}
