import { useCallback, useEffect, useImperativeHandle, useRef, useState, forwardRef } from 'react'
import { Eraser, Check } from 'lucide-react'
import { Button } from '@/components/ui/shadcn/button'
import { cn } from '@/lib/cn'

// SignaturePad — touch-first signature capture (ADR 0073).
//
// Records the user's stroke via Pointer Events (works for finger,
// stylus, and mouse with one code path). Emits an SVG `<path d=...>`
// string on submit so the captured signature stays vector-perfect
// at any size and stores in ~2-5 KB of text rather than a base64
// raster blob.
//
// Sizing is touch-first: h-[40vh] on a phone so the user has enough
// room to draw with a finger; clamps to ~h-64 on lg+ so a desktop
// reviewer doesn't get a huge empty box. The canvas internally
// scales to the device pixel ratio so iOS / high-DPI Android stays
// crisp.

export interface SignaturePadHandle {
  clear: () => void
  isEmpty: () => boolean
  toSVGPath: () => string
}

interface SignaturePadProps {
  onSubmit?: (svgPath: string) => void
  onChange?: (isEmpty: boolean) => void
  submitLabel?: string
  className?: string
  disabled?: boolean
}

interface Stroke {
  points: { x: number; y: number }[]
}

export const SignaturePad = forwardRef<SignaturePadHandle, SignaturePadProps>(function SignaturePad(
  { onSubmit, onChange, submitLabel = 'Apply signature', className, disabled },
  ref,
) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const strokesRef = useRef<Stroke[]>([])
  const currentRef = useRef<Stroke | null>(null)
  const [empty, setEmpty] = useState(true)

  const fitCanvas = useCallback(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const dpr = window.devicePixelRatio || 1
    const rect = canvas.getBoundingClientRect()
    canvas.width = rect.width * dpr
    canvas.height = rect.height * dpr
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.scale(dpr, dpr)
    ctx.lineCap = 'round'
    ctx.lineJoin = 'round'
    ctx.lineWidth = 2.4
    ctx.strokeStyle = 'currentColor'
    redraw()
  }, [])

  const redraw = useCallback(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    const rect = canvas.getBoundingClientRect()
    ctx.clearRect(0, 0, rect.width, rect.height)
    for (const s of strokesRef.current) drawStroke(ctx, s)
    if (currentRef.current) drawStroke(ctx, currentRef.current)
  }, [])

  useEffect(() => {
    fitCanvas()
    const onResize = () => fitCanvas()
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [fitCanvas])

  const updateEmpty = useCallback(() => {
    const next = strokesRef.current.length === 0
    setEmpty(next)
    onChange?.(next)
  }, [onChange])

  const clear = useCallback(() => {
    strokesRef.current = []
    currentRef.current = null
    redraw()
    updateEmpty()
  }, [redraw, updateEmpty])

  const toSVGPath = useCallback(() => strokesToSVGPath(strokesRef.current), [])

  useImperativeHandle(ref, () => ({ clear, isEmpty: () => empty, toSVGPath }), [clear, empty, toSVGPath])

  const pointFromEvent = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const canvas = canvasRef.current!
    const rect = canvas.getBoundingClientRect()
    return { x: e.clientX - rect.left, y: e.clientY - rect.top }
  }

  const onPointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (disabled) return
    canvasRef.current?.setPointerCapture(e.pointerId)
    const pt = pointFromEvent(e)
    currentRef.current = { points: [pt] }
    redraw()
  }
  const onPointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (!currentRef.current) return
    currentRef.current.points.push(pointFromEvent(e))
    redraw()
  }
  const onPointerUp = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (!currentRef.current) return
    canvasRef.current?.releasePointerCapture(e.pointerId)
    if (currentRef.current.points.length > 1) {
      strokesRef.current.push(currentRef.current)
      updateEmpty()
    }
    currentRef.current = null
    redraw()
  }

  const handleSubmit = () => {
    if (empty || disabled) return
    onSubmit?.(toSVGPath())
  }

  return (
    <div className={cn('flex flex-col gap-3', className)}>
      <div className="relative h-[40vh] min-h-[180px] sm:h-64 rounded-md border border-border bg-card text-foreground">
        <canvas
          ref={canvasRef}
          // touch-action:none — without this, mobile browsers pan/zoom
          // the page on the gesture instead of routing it as pointer
          // events. Pure CSS, no preventDefault juggling needed.
          className="h-full w-full touch-none rounded-md"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={onPointerUp}
          aria-label="Signature canvas"
          data-testid="signature-pad-canvas"
        />
        {empty && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center text-xs text-muted-foreground">
            Sign here with your finger or stylus
          </div>
        )}
      </div>
      <div className="flex items-center justify-between gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={clear}
          disabled={disabled || empty}
          data-testid="signature-pad-clear"
        >
          <Eraser className="me-1 h-4 w-4" /> Clear
        </Button>
        <Button
          type="button"
          onClick={handleSubmit}
          disabled={disabled || empty}
          data-testid="signature-pad-submit"
        >
          <Check className="me-1 h-4 w-4" /> {submitLabel}
        </Button>
      </div>
    </div>
  )
})

function drawStroke(ctx: CanvasRenderingContext2D, s: Stroke) {
  if (s.points.length < 2) return
  ctx.beginPath()
  ctx.moveTo(s.points[0].x, s.points[0].y)
  for (let i = 1; i < s.points.length; i++) {
    ctx.lineTo(s.points[i].x, s.points[i].y)
  }
  ctx.stroke()
}

// strokesToSVGPath compresses the recorded strokes into a single
// `d` attribute. Each stroke becomes an `M x,y L x,y L x,y` segment.
// Coordinates are rounded to 1 decimal — the canvas resolution is
// already smaller than a pixel of perceptible difference.
export function strokesToSVGPath(strokes: Stroke[]): string {
  const parts: string[] = []
  for (const s of strokes) {
    if (s.points.length === 0) continue
    const [first, ...rest] = s.points
    parts.push(`M${round(first.x)},${round(first.y)}`)
    for (const p of rest) parts.push(`L${round(p.x)},${round(p.y)}`)
  }
  return parts.join(' ')
}

function round(n: number): string {
  return (Math.round(n * 10) / 10).toString()
}

// detectDeviceKind classifies the current device for the ADR 0073
// device_kind evidence column. Coarse heuristic:
//   - pointer:coarse + viewport ≤ 768 → phone
//   - pointer:coarse + larger        → tablet
//   - everything else                → desktop
export function detectDeviceKind(): 'phone' | 'tablet' | 'desktop' {
  if (typeof window === 'undefined') return 'desktop'
  const coarse = window.matchMedia?.('(pointer: coarse)').matches
  if (!coarse) return 'desktop'
  return window.innerWidth <= 768 ? 'phone' : 'tablet'
}
