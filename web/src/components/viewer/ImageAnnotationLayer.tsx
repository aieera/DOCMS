// ADR 0067 — image annotation layer.
//
// Persists a Fabric.js-shaped envelope `{version, objects: [...]}`
// per the ADR. v1 ships with a small SVG-backed overlay that
// produces the same wire shape so a future swap to real Fabric.js
// is data-compatible. Each tool draws one shape on click-drag and
// posts a new annotation row.
//
// Coordinate space: shapes are stored in the image's NATURAL pixel
// space, and the overlay <svg> carries a matching viewBox with
// preserveAspectRatio="none". This keeps annotations pinned to the
// same image feature regardless of the rendered size — previously the
// coordinates were absolute draw-time pixels in a viewBox-less SVG, so
// any responsive resize (the image is `max-w-full`) slid every shape
// off its target. Outlines use non-scaling-stroke so they stay crisp
// at any zoom; note text scales with the render so it tracks the box.
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { annotationsApi, type Annotation, type ImageShapeData } from '@/api/annotations'
import { readErrorMessage } from '@/api/client'
import { useAuthBlob } from '@/lib/useAuthBlob'
import { AnnotationToolbar, type ImageMode } from './AnnotationToolbar'

interface Props {
  documentId: string
  versionId: string
  imageUrl: string
  canCreate: boolean
}

interface Shape {
  type: 'rect' | 'ellipse' | 'arrow' | 'note'
  x: number; y: number; w: number; h: number
  body?: string
}

export function ImageAnnotationLayer({ documentId, versionId, imageUrl, canCreate }: Props) {
  const [mode, setMode] = useState<ImageMode | null>(null)
  const [visible, setVisible] = useState(true)
  const [annotations, setAnnotations] = useState<Annotation[]>([])
  // Natural image dimensions define the annotation coordinate space.
  const [natural, setNatural] = useState<{ w: number; h: number } | null>(null)
  // Rendered surface width, tracked in state (updated by the
  // ResizeObserver + on image load) so the natural→rendered scale used
  // for note font sizing recomputes on reflow without reading the ref
  // during render.
  const [renderedW, setRenderedW] = useState(0)
  // <img> can't send X-Tenant-ID — fetch via axios into a blob URL so
  // the bytes load even before the backend's cookie-only fallback ships.
  const blobUrl = useAuthBlob(imageUrl)
  const [drag, setDrag] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null)
  const surfaceRef = useRef<HTMLDivElement>(null)

  // Load on mount + on doc/version change. Clear the previous document's
  // annotations FIRST so they don't render over the newly-selected one
  // during the async fetch (cross-document ghost overlay).
  useEffect(() => {
    let cancelled = false
    setAnnotations([])
    setDrag(null)
    annotationsApi.list(documentId, versionId).then((rows) => {
      if (!cancelled) setAnnotations(rows.filter((a) => a.type === 'image_shape'))
    }).catch(() => { /* non-fatal — empty overlay */ })
    return () => { cancelled = true }
  }, [documentId, versionId])

  // Track surface resizes so note text stays legible after a reflow.
  useEffect(() => {
    const el = surfaceRef.current
    if (!el) return
    setRenderedW(el.clientWidth)
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => setRenderedW(el.clientWidth))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // Convert a client (viewport) point to natural-image coordinates.
  const toNatural = (clientX: number, clientY: number) => {
    const rect = surfaceRef.current!.getBoundingClientRect()
    const sx = natural && rect.width > 0 ? natural.w / rect.width : 1
    const sy = natural && rect.height > 0 ? natural.h / rect.height : 1
    return { x: (clientX - rect.left) * sx, y: (clientY - rect.top) * sy }
  }

  const persist = async (shape: Shape) => {
    const data: ImageShapeData = {
      version: '5.3.0', // matches Fabric.js's toJSON envelope version
      objects: [shape],
    }
    try {
      const created = await annotationsApi.create(documentId, versionId, {
        page: 1, type: 'image_shape', data: data as unknown as Record<string, unknown>,
      })
      setAnnotations((a) => [...a, created])
    } catch (e: unknown) {
      toast.error(readErrorMessage(e) ?? 'Failed to save annotation')
    }
  }

  const onMouseDown = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!mode || !canCreate) return
    const p = toNatural(e.clientX, e.clientY)
    setDrag({ x0: p.x, y0: p.y, x1: p.x, y1: p.y })
  }
  const onMouseMove = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!drag) return
    const p = toNatural(e.clientX, e.clientY)
    setDrag({ ...drag, x1: p.x, y1: p.y })
  }
  const onMouseUp = () => {
    if (!drag || !mode) { setDrag(null); return }
    const x = Math.min(drag.x0, drag.x1)
    const y = Math.min(drag.y0, drag.y1)
    const w = Math.abs(drag.x1 - drag.x0)
    const h = Math.abs(drag.y1 - drag.y0)
    // Ignore stray clicks. The 4px threshold is in natural units scaled
    // to the current render so it feels the same at any zoom.
    const rect = surfaceRef.current?.getBoundingClientRect()
    const minMove = natural && rect && rect.width > 0 ? 4 * (natural.w / rect.width) : 4
    if (w < minMove && h < minMove) { setDrag(null); return }
    let body: string | undefined
    if (mode === 'note') {
      body = window.prompt('Comment') ?? undefined
      if (!body) { setDrag(null); return }
    }
    void persist({ type: mode, x, y, w, h, body })
    setDrag(null)
  }

  // Natural units per rendered pixel — used to keep note text ~constant
  // on screen regardless of the viewBox scale.
  const unit = natural && renderedW > 0 ? natural.w / renderedW : 1

  return (
    <div className="space-y-2">
      <AnnotationToolbar
        kind="image" mode={mode} onModeChange={(m) => setMode(m as ImageMode | null)}
        visible={visible} onToggleVisible={setVisible} canCreate={canCreate}
      />
      <div
        ref={surfaceRef}
        onMouseDown={onMouseDown}
        onMouseMove={onMouseMove}
        onMouseUp={onMouseUp}
        className={`relative inline-block ${mode ? 'cursor-crosshair' : 'cursor-default'}`}
        data-testid="image-annotation-surface"
      >
        {blobUrl && (
          <img
            src={blobUrl}
            alt=""
            className="block max-w-full select-none"
            onLoad={(e) => {
              setNatural({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })
              if (surfaceRef.current) setRenderedW(surfaceRef.current.clientWidth)
            }}
          />
        )}
        {visible && (
          <svg
            className="absolute inset-0 h-full w-full pointer-events-none"
            viewBox={natural ? `0 0 ${natural.w} ${natural.h}` : undefined}
            preserveAspectRatio="none"
            data-testid="image-annotation-overlay"
          >
            {annotations.flatMap((a) =>
              ((a.data as unknown as ImageShapeData).objects ?? []).map((o, i) =>
                renderShape(o as Shape, `${a.id}-${i}`, unit),
              ),
            )}
            {drag && mode && drag.x0 !== drag.x1 && (
              <ShapePreview shape={{
                type: mode, x: Math.min(drag.x0, drag.x1), y: Math.min(drag.y0, drag.y1),
                w: Math.abs(drag.x1 - drag.x0), h: Math.abs(drag.y1 - drag.y0),
              }} unit={unit} />
            )}
          </svg>
        )}
      </div>
    </div>
  )
}

function renderShape(s: Shape, key: string, unit = 1) {
  const stroke = '#2563eb'
  switch (s.type) {
    case 'rect':
      return <rect key={key} x={s.x} y={s.y} width={s.w} height={s.h} fill="none" stroke={stroke} strokeWidth={2} vectorEffect="non-scaling-stroke" />
    case 'ellipse':
      return <ellipse key={key} cx={s.x + s.w / 2} cy={s.y + s.h / 2} rx={s.w / 2} ry={s.h / 2} fill="none" stroke={stroke} strokeWidth={2} vectorEffect="non-scaling-stroke" />
    case 'arrow':
      return <line key={key} x1={s.x} y1={s.y} x2={s.x + s.w} y2={s.y + s.h} stroke={stroke} strokeWidth={2} markerEnd="url(#arrow)" vectorEffect="non-scaling-stroke" />
    case 'note':
      return (
        <g key={key}>
          <rect x={s.x} y={s.y} width={s.w} height={s.h} fill="rgba(254,243,199,0.85)" stroke="#f59e0b" strokeWidth={1} vectorEffect="non-scaling-stroke" />
          {s.body && <text x={s.x + 4 * unit} y={s.y + 14 * unit} fontSize={11 * unit} fill="#78350f">{s.body}</text>}
        </g>
      )
    default:
      return null
  }
}

function ShapePreview({ shape, unit = 1 }: { shape: Shape; unit?: number }) {
  return renderShape(shape, 'preview', unit)
}
