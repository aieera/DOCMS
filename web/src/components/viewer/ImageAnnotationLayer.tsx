// ADR 0067 — image annotation layer.
//
// Persists a Fabric.js-shaped envelope `{version, objects: [...]}`
// per the ADR. v1 ships with a small SVG-backed overlay that
// produces the same wire shape so a future swap to real Fabric.js
// is data-compatible. Each tool draws one shape on click-drag and
// posts a new annotation row.
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { annotationsApi, type Annotation, type ImageShapeData } from '@/api/annotations'
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
  const [drag, setDrag] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null)
  const surfaceRef = useRef<HTMLDivElement>(null)

  // Load on mount + on doc/version change. Was useMemo; useEffect
  // is the right hook for fire-and-forget side effects (useMemo
  // doesn't guarantee re-run semantics in strict mode + is wrong
  // intent).
  useEffect(() => {
    let cancelled = false
    annotationsApi.list(documentId, versionId).then((rows) => {
      if (!cancelled) setAnnotations(rows.filter((a) => a.type === 'image_shape'))
    }).catch(() => { /* non-fatal — empty overlay */ })
    return () => { cancelled = true }
  }, [documentId, versionId])

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
    } catch (e: any) {
      toast.error(e?.response?.data?.error ?? 'Failed to save annotation')
    }
  }

  const onMouseDown = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!mode || !canCreate) return
    const rect = surfaceRef.current!.getBoundingClientRect()
    setDrag({ x0: e.clientX - rect.left, y0: e.clientY - rect.top, x1: e.clientX - rect.left, y1: e.clientY - rect.top })
  }
  const onMouseMove = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!drag) return
    const rect = surfaceRef.current!.getBoundingClientRect()
    setDrag({ ...drag, x1: e.clientX - rect.left, y1: e.clientY - rect.top })
  }
  const onMouseUp = () => {
    if (!drag || !mode) { setDrag(null); return }
    const x = Math.min(drag.x0, drag.x1)
    const y = Math.min(drag.y0, drag.y1)
    const w = Math.abs(drag.x1 - drag.x0)
    const h = Math.abs(drag.y1 - drag.y0)
    if (w < 4 && h < 4) { setDrag(null); return } // ignore stray clicks
    let body: string | undefined
    if (mode === 'note') {
      body = window.prompt('Comment') ?? undefined
      if (!body) { setDrag(null); return }
    }
    void persist({ type: mode, x, y, w, h, body })
    setDrag(null)
  }

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
        <img src={imageUrl} alt="" className="block max-w-full select-none" />
        {visible && (
          <svg className="absolute inset-0 h-full w-full pointer-events-none" data-testid="image-annotation-overlay">
            {annotations.flatMap((a) =>
              ((a.data as unknown as ImageShapeData).objects ?? []).map((o, i) =>
                renderShape(o as Shape, `${a.id}-${i}`),
              ),
            )}
            {drag && mode && drag.x0 !== drag.x1 && (
              <ShapePreview shape={{
                type: mode, x: Math.min(drag.x0, drag.x1), y: Math.min(drag.y0, drag.y1),
                w: Math.abs(drag.x1 - drag.x0), h: Math.abs(drag.y1 - drag.y0),
              }} />
            )}
          </svg>
        )}
      </div>
    </div>
  )
}

function renderShape(s: Shape, key: string) {
  const stroke = '#2563eb'
  switch (s.type) {
    case 'rect':
      return <rect key={key} x={s.x} y={s.y} width={s.w} height={s.h} fill="none" stroke={stroke} strokeWidth={2} />
    case 'ellipse':
      return <ellipse key={key} cx={s.x + s.w / 2} cy={s.y + s.h / 2} rx={s.w / 2} ry={s.h / 2} fill="none" stroke={stroke} strokeWidth={2} />
    case 'arrow':
      return <line key={key} x1={s.x} y1={s.y} x2={s.x + s.w} y2={s.y + s.h} stroke={stroke} strokeWidth={2} markerEnd="url(#arrow)" />
    case 'note':
      return (
        <g key={key}>
          <rect x={s.x} y={s.y} width={s.w} height={s.h} fill="rgba(254,243,199,0.85)" stroke="#f59e0b" strokeWidth={1} />
          {s.body && <text x={s.x + 4} y={s.y + 14} fontSize={11} fill="#78350f">{s.body}</text>}
        </g>
      )
    default:
      return null
  }
}

function ShapePreview({ shape }: { shape: Shape }) {
  return renderShape(shape, 'preview')
}
