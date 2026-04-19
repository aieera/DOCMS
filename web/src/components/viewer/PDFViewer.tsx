import { useEffect, useState } from 'react'
import { Document, Page, pdfjs } from 'react-pdf'
import { Button } from '@/components/ui/Button'
import { ChevronLeft, ChevronRight, MessageSquare } from 'lucide-react'
import { Spinner } from '@/components/ui/Spinner'
import { annotationsApi, type Annotation } from '@/api/annotations'

pdfjs.GlobalWorkerOptions.workerSrc = `//unpkg.com/pdfjs-dist@${pdfjs.version}/build/pdf.worker.min.mjs`

interface PDFViewerProps {
  url: string
  mimeType?: string
  // §17.3 / D10 — when documentId + versionId are provided, the viewer
  // loads and overlays annotations for that (doc, version). Omit them
  // for preview-only use (share links, OCR bench, etc.).
  documentId?: string
  versionId?: string
}

export function PDFViewer({ url, documentId, versionId }: PDFViewerProps) {
  const [numPages, setNumPages] = useState(0)
  const [page, setPage] = useState(1)
  const [annotations, setAnnotations] = useState<Annotation[]>([])

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

  return (
    <div className="flex flex-col items-center">
      <Document
        file={url}
        onLoadSuccess={({ numPages: n }) => setNumPages(n)}
        loading={<Spinner />}
        error={<p className="text-sm text-red-500">Failed to load PDF</p>}
      >
        <div className="relative">
          <Page
            pageNumber={page}
            width={800}
            renderTextLayer
            renderAnnotationLayer={false}
          />
          {pageAnnotations.length > 0 && (
            <AnnotationOverlay annotations={pageAnnotations} />
          )}
        </div>
      </Document>
      {numPages > 1 && (
        <div className="mt-3 flex items-center gap-2">
          <Button variant="ghost" size="sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <span className="text-sm">{page} / {numPages}</span>
          <Button variant="ghost" size="sm" disabled={page >= numPages} onClick={() => setPage((p) => p + 1)}>
            <ChevronRight className="h-4 w-4" />
          </Button>
          {annotations.length > 0 && (
            <span className="ml-4 inline-flex items-center gap-1 text-xs text-muted-foreground">
              <MessageSquare className="h-3 w-3" aria-hidden />
              {annotations.length} annotation{annotations.length === 1 ? '' : 's'}
            </span>
          )}
        </div>
      )}
    </div>
  )
}

// AnnotationOverlay draws each annotation at its stored coords. The
// data schema per type mirrors the blueprint §17.3:
//   highlight: { x, y, width, height } (0..1 normalized)
//   note:      { x, y, text }
//   stamp:     { x, y, label }
//   drawing:   { path: [[x0,y0], [x1,y1], ...] }
// Kept render-only in this PR; a full drawing UI follows in the
// D10 editing slice.
function AnnotationOverlay({ annotations }: { annotations: Annotation[] }) {
  return (
    <div
      className="pointer-events-none absolute inset-0"
      role="list"
      aria-label="Annotations on current page"
    >
      {annotations.map((a) => {
        const d = a.data as Record<string, number | string>
        const x = typeof d.x === 'number' ? d.x * 100 : 0
        const y = typeof d.y === 'number' ? d.y * 100 : 0
        if (a.type === 'highlight') {
          const w = typeof d.width === 'number' ? d.width * 100 : 10
          const h = typeof d.height === 'number' ? d.height * 100 : 2
          return (
            <div
              key={a.id}
              role="listitem"
              aria-label={`Highlight, page ${a.page_number}`}
              className="absolute rounded-sm bg-yellow-300/40"
              style={{ left: `${x}%`, top: `${y}%`, width: `${w}%`, height: `${h}%` }}
            />
          )
        }
        if (a.type === 'note') {
          return (
            <button
              key={a.id}
              type="button"
              role="listitem"
              aria-label={`Note: ${typeof d.text === 'string' ? d.text : ''}`}
              className="pointer-events-auto absolute -ml-3 -mt-3 h-6 w-6 rounded-full bg-blue-500 text-[10px] font-bold text-white shadow hover:bg-blue-600"
              style={{ left: `${x}%`, top: `${y}%` }}
            >N</button>
          )
        }
        return null
      })}
    </div>
  )
}
