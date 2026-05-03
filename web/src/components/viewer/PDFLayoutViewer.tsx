import { useMemo, useState } from 'react'
import { Document, Page, pdfjs } from 'react-pdf'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/Button'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import type { OCRPage } from '@/api/ocr'

pdfjs.GlobalWorkerOptions.workerSrc = `//unpkg.com/pdfjs-dist@${pdfjs.version}/build/pdf.worker.min.mjs`

// Surya emits boxes in pixel coordinates of the rasterized page (150 DPI).
// We don't persist the raster's width/height yet, so we infer them from
// max(x2)/max(y2) across the page's boxes — accurate within ~1-2% on
// pages where text reaches the margins, which is nearly always true for
// scanned documents. When persisted dims land in `bounding_boxes`, this
// falls back through gracefully.
type SuryaBox = { x1: number; y1: number; x2: number; y2: number; text: string; confidence: number }

interface Props {
  url: string
  pages: OCRPage[]
  onSelectLine?: (page: number, text: string) => void
}

export function PDFLayoutViewer({ url, pages, onSelectLine }: Props) {
  const [numPages, setNumPages] = useState(0)
  const [page, setPage] = useState(1)
  const [renderWidth, setRenderWidth] = useState(800)

  const boxesByPage = useMemo(() => {
    const m = new Map<number, { boxes: SuryaBox[]; w: number; h: number }>()
    for (const p of pages) {
      const raw = p.bounding_boxes
      const boxes = normalizeBoxes(raw)
      if (boxes.length === 0) continue
      let w = 0
      let h = 0
      // Persisted shape may be {width, height, lines:[...]}; honor it.
      if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
        const obj = raw as { width?: number; height?: number }
        if (typeof obj.width === 'number') w = obj.width
        if (typeof obj.height === 'number') h = obj.height
      }
      if (w === 0 || h === 0) {
        for (const b of boxes) {
          if (b.x2 > w) w = b.x2
          if (b.y2 > h) h = b.y2
        }
      }
      m.set(p.page_number, { boxes, w, h })
    }
    return m
  }, [pages])

  const current = boxesByPage.get(page)

  return (
    <div className="flex flex-col items-center">
      <Document
        file={url}
        onLoadSuccess={({ numPages: n }) => setNumPages(n)}
        loading={<Spinner />}
        error={<p className="text-sm text-red-500">Failed to load PDF</p>}
      >
        <div className="relative inline-block">
          <Page
            pageNumber={page}
            width={renderWidth}
            renderTextLayer={false}
            renderAnnotationLayer={false}
            onRenderSuccess={(p) => setRenderWidth(p.width)}
          />
          {current && current.w > 0 && current.h > 0 && (
            <BoxOverlay
              boxes={current.boxes}
              srcW={current.w}
              srcH={current.h}
              onSelect={onSelectLine ? (b) => onSelectLine(page, b.text) : undefined}
            />
          )}
        </div>
      </Document>
      <div className="mt-3 flex items-center gap-3 text-xs text-[var(--color-text-secondary)]">
        {numPages > 1 && (
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
              <ChevronLeft className="h-4 w-4" />
            </Button>
            <span>{page} / {numPages}</span>
            <Button variant="ghost" size="sm" disabled={page >= numPages} onClick={() => setPage((p) => p + 1)}>
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        )}
        {current ? (
          <span>{current.boxes.length} line{current.boxes.length === 1 ? '' : 's'} detected</span>
        ) : (
          <span>No layout boxes on this page (text-PDF fast path or empty page)</span>
        )}
        <span className="ml-2 inline-flex items-center gap-2">
          <LegendSwatch className="bg-emerald-400/40" /> ≥95%
          <LegendSwatch className="bg-amber-400/40" /> 80–95%
          <LegendSwatch className="bg-red-400/40" /> &lt;80%
        </span>
      </div>
    </div>
  )
}

function BoxOverlay({
  boxes,
  srcW,
  srcH,
  onSelect,
}: {
  boxes: SuryaBox[]
  srcW: number
  srcH: number
  onSelect?: (b: SuryaBox) => void
}) {
  return (
    <div className="pointer-events-none absolute inset-0">
      {boxes.map((b, i) => {
        const left = (b.x1 / srcW) * 100
        const top = (b.y1 / srcH) * 100
        const width = ((b.x2 - b.x1) / srcW) * 100
        const height = ((b.y2 - b.y1) / srcH) * 100
        const conf = b.confidence ?? 0
        const color =
          conf >= 0.95 ? 'bg-emerald-400/30 border-emerald-500/70'
          : conf >= 0.8 ? 'bg-amber-400/30 border-amber-500/70'
          : 'bg-red-400/30 border-red-500/70'
        return (
          <button
            key={i}
            type="button"
            title={`${b.text}\n${(conf * 100).toFixed(1)}% confidence`}
            onClick={onSelect ? () => onSelect(b) : undefined}
            className={`pointer-events-auto absolute border ${color} hover:ring-2 hover:ring-[var(--color-primary)]`}
            style={{ left: `${left}%`, top: `${top}%`, width: `${width}%`, height: `${height}%` }}
          />
        )
      })}
    </div>
  )
}

function LegendSwatch({ className }: { className: string }) {
  return <span className={`inline-block h-3 w-3 rounded-sm border border-[var(--color-border)] ${className}`} />
}

// normalizeBoxes accepts either the raw array shape currently persisted
// ([{x1,y1,x2,y2,text,confidence}, ...]) or a future {lines:[...]} wrapper.
function normalizeBoxes(raw: unknown): SuryaBox[] {
  if (!raw) return []
  const arr =
    Array.isArray(raw) ? raw
    : typeof raw === 'object' && Array.isArray((raw as { lines?: unknown[] }).lines) ? (raw as { lines: unknown[] }).lines
    : []
  const out: SuryaBox[] = []
  for (const item of arr) {
    if (!item || typeof item !== 'object') continue
    const b = item as Record<string, unknown>
    const x1 = num(b.x1)
    const y1 = num(b.y1)
    const x2 = num(b.x2)
    const y2 = num(b.y2)
    if (x1 === null || y1 === null || x2 === null || y2 === null) continue
    out.push({
      x1, y1, x2, y2,
      text: typeof b.text === 'string' ? b.text : '',
      confidence: typeof b.confidence === 'number' ? b.confidence : 0,
    })
  }
  return out
}

function num(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}
