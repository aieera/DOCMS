import { useMemo, useState } from 'react'
import { Document, Page, pdfjs } from 'react-pdf'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/shadcn/button'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import type { OCRPage } from '@/api/ocr'
import type { Entity } from '@/api/ner'
import { ENTITY_COLOR } from '@/components/intelligence/EntitiesPanel'

pdfjs.GlobalWorkerOptions.workerSrc = `//unpkg.com/pdfjs-dist@${pdfjs.version}/build/pdf.worker.min.mjs`

// Surya emits boxes in pixel coordinates of the rasterized page (150 DPI).
// We don't persist the raster's width/height yet, so we infer them from
// max(x2)/max(y2) across the page's boxes — accurate within ~1-2% on
// pages where text reaches the margins, which is nearly always true for
// scanned documents. When persisted dims land in `bounding_boxes`, this
// falls back through gracefully.
type SuryaBox = { x1: number; y1: number; x2: number; y2: number; text: string; confidence: number }

// Per-page word-box payload the OCR worker writes (ADR 0078
// follow-up). Empty array when the engine doesn't produce them.
type WordBox = { start: number; end: number; x0: number; y0: number; x1: number; y1: number }
type WordBoxPayload = { page_width: number; page_height: number; words: WordBox[] }

interface Props {
  url: string
  pages: OCRPage[]
  /** Doc-wide entities (offsets into "\n\n".join(pages)). When provided,
   * matching word boxes are highlighted with the entity color and tooltip. */
  entities?: Entity[]
  onSelectLine?: (page: number, text: string) => void
  onSelectEntity?: (e: Entity) => void
}

export function PDFLayoutViewer({ url, pages, entities, onSelectLine, onSelectEntity }: Props) {
  const [numPages, setNumPages] = useState(0)
  const [page, setPage] = useState(1)
  const [renderWidth, setRenderWidth] = useState(800)
  const [showEntities, setShowEntities] = useState(true)
  const [showLines, setShowLines] = useState(true)

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

  // entityRectsByPage maps page_number → array of rectangles to draw,
  // each carrying its source entity for tooltip + click. Computed once
  // per render of `pages` + `entities`. Skipped when entities is
  // undefined (caller doesn't want overlays).
  const entityRectsByPage = useMemo(() => {
    const m = new Map<number, EntityRect[]>()
    if (!entities || entities.length === 0) return m
    // Walk page text lengths to map each page's [pageStart, pageEnd]
    // range in the joined "\n\n" text the worker emitted to NER.
    let cursor = 0
    for (const p of pages) {
      const len = p.text_content?.length ?? 0
      const pageStart = cursor
      const pageEnd = pageStart + len
      cursor = pageEnd + 2 // "\n\n" separator
      const wb = parseWordBoxes(p.word_boxes)
      if (!wb || wb.words.length === 0) continue
      const rects: EntityRect[] = []
      for (const e of entities) {
        if (e.start_offset < pageStart || e.end_offset > pageEnd) continue
        // Re-base entity offsets to page-local for the word-box index.
        const localStart = e.start_offset - pageStart
        const localEnd = e.end_offset - pageStart
        const r = entityToRect(wb, localStart, localEnd)
        if (r) rects.push({ ...r, entity: e })
      }
      if (rects.length) {
        m.set(p.page_number, rects)
      }
    }
    return m
  }, [pages, entities])

  const currentEntities = entityRectsByPage.get(page) ?? []
  const hasEntities = entityRectsByPage.size > 0

  return (
    <div className="flex flex-col items-center">
      <Document
        file={url}
        onLoadSuccess={({ numPages: n }) => setNumPages(n)}
        loading={<Spinner />}
        error={
          <div className="rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm">
            <p className="font-medium text-destructive">PDF unavailable — cannot render layout.</p>
            <p className="mt-1 text-xs text-muted-foreground">
              The document bytes couldn't be fetched from storage. Check that the storage service is healthy and the version exists.
            </p>
          </div>
        }
      >
        <div className="relative inline-block">
          <Page
            pageNumber={page}
            width={renderWidth}
            renderTextLayer={false}
            renderAnnotationLayer={false}
            onRenderSuccess={(p) => setRenderWidth(p.width)}
          />
          {showLines && current && current.w > 0 && current.h > 0 && (
            <BoxOverlay
              boxes={current.boxes}
              srcW={current.w}
              srcH={current.h}
              onSelect={onSelectLine ? (b) => onSelectLine(page, b.text) : undefined}
            />
          )}
          {showEntities && currentEntities.length > 0 && (
            <EntityOverlay
              rects={currentEntities}
              onSelect={onSelectEntity}
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
        <label className="flex items-center gap-1">
          <input
            type="checkbox"
            checked={showLines}
            onChange={(e) => setShowLines(e.target.checked)}
          />
          Lines
        </label>
        {hasEntities && (
          <label className="flex items-center gap-1">
            <input
              type="checkbox"
              checked={showEntities}
              onChange={(e) => setShowEntities(e.target.checked)}
            />
            Entities
            <span>· {currentEntities.length} on this page</span>
          </label>
        )}
      </div>
    </div>
  )
}

interface EntityRect {
  // Percentages of the page so the overlay scales with the rendered
  // <Page> regardless of CSS / device pixel ratio.
  leftPct: number
  topPct: number
  widthPct: number
  heightPct: number
  entity: Entity
}

function EntityOverlay({
  rects,
  onSelect,
}: {
  rects: EntityRect[]
  onSelect?: (e: Entity) => void
}) {
  return (
    <div className="pointer-events-none absolute inset-0">
      {rects.map((r, i) => {
        const cls = ENTITY_COLOR[r.entity.entity_type] ?? 'bg-zinc-100/40 border-zinc-300/70'
        return (
          <button
            key={i}
            type="button"
            title={`${r.entity.entity_type}: ${r.entity.entity_value}\n${(r.entity.confidence * 100).toFixed(0)}% · ${r.entity.source}`}
            onClick={onSelect ? () => onSelect(r.entity) : undefined}
            className={`pointer-events-auto absolute rounded-sm border-2 ${cls} opacity-60 hover:opacity-100`}
            style={{
              left: `${r.leftPct}%`,
              top: `${r.topPct}%`,
              width: `${r.widthPct}%`,
              height: `${r.heightPct}%`,
            }}
          />
        )
      })}
    </div>
  )
}

// parseWordBoxes accepts the wrapper shape the worker writes
// ({page_width, page_height, words:[...]}) and the legacy empty-array
// shape. Returns null when there's nothing usable.
function parseWordBoxes(raw: unknown): WordBoxPayload | null {
  if (!raw) return null
  if (Array.isArray(raw)) return null // legacy empty / Surya path
  if (typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const pw = num(r.page_width)
  const ph = num(r.page_height)
  if (pw === null || ph === null || pw <= 0 || ph <= 0) return null
  const words: WordBox[] = []
  if (Array.isArray(r.words)) {
    for (const w of r.words) {
      if (!w || typeof w !== 'object') continue
      const o = w as Record<string, unknown>
      const start = num(o.start)
      const end = num(o.end)
      const x0 = num(o.x0)
      const y0 = num(o.y0)
      const x1 = num(o.x1)
      const y1 = num(o.y1)
      if (
        start === null || end === null ||
        x0 === null || y0 === null || x1 === null || y1 === null
      ) continue
      words.push({ start, end, x0, y0, x1, y1 })
    }
  }
  return { page_width: pw, page_height: ph, words }
}

// entityToRect finds the union bounding box of all word boxes that
// intersect [start, end). Multi-line spans become one tall rectangle;
// short single-word spans become a tight word-sized rectangle. Returns
// null when no word in the page overlaps the span.
function entityToRect(
  wb: WordBoxPayload,
  start: number,
  end: number,
): { leftPct: number; topPct: number; widthPct: number; heightPct: number } | null {
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
  let touched = false
  for (const w of wb.words) {
    if (w.end <= start || w.start >= end) continue // disjoint
    touched = true
    if (w.x0 < minX) minX = w.x0
    if (w.y0 < minY) minY = w.y0
    if (w.x1 > maxX) maxX = w.x1
    if (w.y1 > maxY) maxY = w.y1
  }
  if (!touched) return null
  const W = wb.page_width
  const H = wb.page_height
  return {
    leftPct: (minX / W) * 100,
    topPct: (minY / H) * 100,
    widthPct: ((maxX - minX) / W) * 100,
    heightPct: ((maxY - minY) / H) * 100,
  }
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
