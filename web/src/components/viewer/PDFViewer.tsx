import { useEffect, useRef, useState } from 'react'
import { Document, Page, pdfjs } from 'react-pdf'
import { Button } from '@/components/ui/shadcn/button'
import { MessageSquare } from 'lucide-react'
import { Spinner } from '@/components/ui/Spinner'
import { annotationsApi, type Annotation, type PDFMarkupData } from '@/api/annotations'
import { AnnotationToolbar, type PDFMode } from './AnnotationToolbar'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

pdfjs.GlobalWorkerOptions.workerSrc = `//unpkg.com/pdfjs-dist@${pdfjs.version}/build/pdf.worker.min.mjs`

interface PDFViewerProps {
  url: string
  mimeType?: string
  // §17.3 / D10 + ADR 0067 — when documentId + versionId are
  // provided, the viewer loads existing annotations + lets the user
  // create new ones via the toolbar. Omit them for preview-only use
  // (share links, OCR bench, etc.).
  documentId?: string
  versionId?: string
  // ADR 0067: false hides write tools but keeps the visibility
  // toggle so a read-only viewer can still hide the overlay.
  canCreate?: boolean
}

export function PDFViewer({ url, documentId, versionId, canCreate = true }: PDFViewerProps) {
  const [numPages, setNumPages] = useState(0)
  const [page, setPage] = useState(1)
  const [annotations, setAnnotations] = useState<Annotation[]>([])
  const [mode, setMode] = useState<PDFMode | null>(null)
  const [visible, setVisible] = useState(true)
  const [drag, setDrag] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null)
  const pageRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!documentId || !versionId) return
    let cancelled = false
    annotationsApi.list(documentId, versionId).then((items) => {
      if (!cancelled) setAnnotations(items)
    }).catch(() => {
      // Non-fatal: missing annotations shouldn't block the viewer.
    })
    return () => { cancelled = true }
  }, [documentId, versionId])

  const pageAnnotations = annotations.filter((a) => a.page_number === page)

  // ADR 0067 — click-to-create on the page surface.
  // For drag-shape tools (highlight/underline/strikethrough/drawing)
  // we capture the bounding rect normalized 0..1; for `note` we drop
  // a single point on click and prompt for body text.
  const onMouseDown = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!mode || !canCreate || !documentId || !versionId) return
    const rect = pageRef.current?.getBoundingClientRect()
    if (!rect) return
    const x = (e.clientX - rect.left) / rect.width
    const y = (e.clientY - rect.top) / rect.height
    if (mode === 'note') {
      void persistNote(x, y)
      return
    }
    setDrag({ x0: x, y0: y, x1: x, y1: y })
  }
  const onMouseMove = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!drag) return
    const rect = pageRef.current?.getBoundingClientRect()
    if (!rect) return
    setDrag({ ...drag, x1: (e.clientX - rect.left) / rect.width, y1: (e.clientY - rect.top) / rect.height })
  }
  const onMouseUp = () => {
    if (!drag || !mode) { setDrag(null); return }
    const x = Math.min(drag.x0, drag.x1)
    const y = Math.min(drag.y0, drag.y1)
    const w = Math.abs(drag.x1 - drag.x0)
    const h = Math.abs(drag.y1 - drag.y0)
    if (w < 0.005 && h < 0.005) { setDrag(null); return } // ignore stray clicks
    void persistRect(mode, x, y, w, h)
    setDrag(null)
  }

  const persistNote = async (x: number, y: number) => {
    if (!documentId || !versionId) return
    const body = window.prompt('Comment')
    if (!body) return
    const data: PDFMarkupData = { kind: 'note', page, rects: [{ x, y, w: 0, h: 0 }], body }
    try {
      const created = await annotationsApi.create(documentId, versionId, {
        page, type: 'pdf_markup', data: data as unknown as Record<string, unknown>,
      })
      setAnnotations((a) => [...a, created])
    } catch { /* persistence failure surfaced via the global error toast in api/client */ }
  }
  const persistRect = async (kind: PDFMode, x: number, y: number, w: number, h: number) => {
    if (!documentId || !versionId || kind === 'note') return
    const data: PDFMarkupData = { kind, page, rects: [{ x, y, w, h }] }
    try {
      const created = await annotationsApi.create(documentId, versionId, {
        page, type: 'pdf_markup', data: data as unknown as Record<string, unknown>,
      })
      setAnnotations((a) => [...a, created])
    } catch { /* same: client interceptor toasts */ }
  }

  return (
    <div className="flex flex-col items-center">
      {documentId && versionId && (
        <div className="mb-2 self-stretch flex items-center justify-between">
          <AnnotationToolbar
            kind="pdf"
            mode={mode}
            onModeChange={(m) => setMode(m as PDFMode | null)}
            visible={visible}
            onToggleVisible={setVisible}
            canCreate={canCreate}
          />
          {annotations.length > 0 && (
            <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
              <MessageSquare className="h-3 w-3" aria-hidden />
              {annotations.length} annotation{annotations.length === 1 ? '' : 's'}
            </span>
          )}
        </div>
      )}
      <Document
        file={url}
        onLoadSuccess={({ numPages: n }) => setNumPages(n)}
        loading={<Spinner />}
        error={
          <div className="rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm">
            <p className="font-medium text-destructive">Unable to load document.</p>
            <p className="mt-1 text-xs text-muted-foreground">
              The PDF bytes couldn't be fetched. The storage service may be unavailable or this version may have been deleted.
            </p>
          </div>
        }
      >
        <div
          ref={pageRef}
          onMouseDown={onMouseDown}
          onMouseMove={onMouseMove}
          onMouseUp={onMouseUp}
          className={`relative ${mode ? 'cursor-crosshair' : 'cursor-default'}`}
          data-testid="pdf-page-surface"
        >
          <Page
            pageNumber={page}
            width={800}
            renderTextLayer
            renderAnnotationLayer={false}
          />
          {visible && pageAnnotations.length > 0 && (
            <AnnotationOverlay annotations={pageAnnotations} />
          )}
          {visible && drag && mode && mode !== 'note' && (
            <DragPreview
              kind={mode}
              x={Math.min(drag.x0, drag.x1)}
              y={Math.min(drag.y0, drag.y1)}
              w={Math.abs(drag.x1 - drag.x0)}
              h={Math.abs(drag.y1 - drag.y0)}
            />
          )}
        </div>
      </Document>
      {numPages > 1 && (
        <div className="mt-3 flex items-center gap-2">
          <Button variant="ghost" size="sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            <DirectionalIcon name="ChevronLeft" className="h-4 w-4" />
          </Button>
          <span className="text-sm">{page} / {numPages}</span>
          <Button variant="ghost" size="sm" disabled={page >= numPages} onClick={() => setPage((p) => p + 1)}>
            <DirectionalIcon name="ChevronRight" className="h-4 w-4" />
          </Button>
        </div>
      )}
    </div>
  )
}

// AnnotationOverlay draws each annotation at its stored coords.
// Two payload shapes are supported:
//   - Legacy primitives (highlight/note/stamp/drawing) where the
//     row's `type` carries the kind and `data` has flat
//     {x,y,width,height,text} fields.
//   - ADR 0067 `pdf_markup` rows where `type` is the category and
//     `data.kind` picks the renderer; coordinates in `data.rects[]`.
function AnnotationOverlay({ annotations }: { annotations: Annotation[] }) {
  return (
    <div
      className="pointer-events-none absolute inset-0"
      role="list"
      aria-label="Annotations on current page"
      data-testid="pdf-annotation-overlay"
    >
      {annotations.map((a) => renderAnnotation(a)).filter(Boolean)}
    </div>
  )
}

function renderAnnotation(a: Annotation): JSX.Element | null {
  let kind: string = a.type
  let rect: { x: number; y: number; w: number; h: number } | null = null
  let body: string | undefined
  if (a.type === 'pdf_markup') {
    const d = a.data as unknown as PDFMarkupData
    kind = d.kind
    rect = d.rects?.[0] ?? null
    body = d.body
  } else {
    const d = a.data as Record<string, number | string>
    rect = {
      x: typeof d.x === 'number' ? d.x : 0,
      y: typeof d.y === 'number' ? d.y : 0,
      w: typeof d.width === 'number' ? d.width : 0,
      h: typeof d.height === 'number' ? d.height : 0,
    }
    if (typeof d.text === 'string') body = d.text
  }
  if (!rect) return null
  const style: React.CSSProperties = {
    left: `${rect.x * 100}%`,
    top: `${rect.y * 100}%`,
    width: `${rect.w * 100}%`,
    height: `${rect.h * 100}%`,
  }
  switch (kind) {
    case 'highlight':
      return <div key={a.id} role="listitem" data-testid={`pdf-annotation-${a.id}`} className="absolute rounded-sm bg-yellow-300/40" style={style} />
    case 'underline':
      return <div key={a.id} role="listitem" data-testid={`pdf-annotation-${a.id}`} className="absolute border-b-2 border-blue-500" style={style} />
    case 'strikethrough':
      return (
        <div
          key={a.id}
          role="listitem"
          data-testid={`pdf-annotation-${a.id}`}
          className="absolute"
          style={{
            ...style,
            borderTop: '2px solid #dc2626',
            top: `calc(${rect.y * 100}% + ${rect.h * 50}%)`,
          }}
        />
      )
    case 'drawing':
      return <div key={a.id} role="listitem" data-testid={`pdf-annotation-${a.id}`} className="absolute rounded border-2 border-emerald-500" style={style} />
    case 'note':
    case 'stamp':
      return (
        <button
          key={a.id}
          type="button"
          role="listitem"
          aria-label={`Note: ${body ?? ''}`}
          data-testid={`pdf-annotation-${a.id}`}
          className="pointer-events-auto absolute -ms-3 -mt-3 h-6 w-6 rounded-full bg-blue-500 text-[10px] font-bold text-white shadow hover:bg-blue-600"
          style={{ left: `${rect.x * 100}%`, top: `${rect.y * 100}%` }}
          title={body}
        >N</button>
      )
    default:
      return null
  }
}

function DragPreview({ kind, x, y, w, h }: { kind: PDFMode; x: number; y: number; w: number; h: number }) {
  const style: React.CSSProperties = {
    left: `${x * 100}%`,
    top: `${y * 100}%`,
    width: `${w * 100}%`,
    height: `${h * 100}%`,
  }
  const cls =
    kind === 'highlight' ? 'bg-yellow-300/40 rounded-sm' :
    kind === 'underline' ? 'border-b-2 border-blue-500' :
    kind === 'strikethrough' ? 'border-t-2 border-red-500' :
    'rounded border-2 border-emerald-500 border-dashed'
  return <div className={`pointer-events-none absolute ${cls}`} style={style} />
}
