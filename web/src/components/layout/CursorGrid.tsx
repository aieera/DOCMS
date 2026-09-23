import { useEffect, useRef } from 'react'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { useCoarsePointer } from '@/hooks/usePointerParallax'

/**
 * Adapted from React Bits' "CursorGrid" (github.com/DavidHDev/react-bits,
 * MIT + Commons Clause).
 *
 * A lattice covering the whole page whose cells light as the pointer
 * passes and fade behind it; a click sends a ring outward, waking cells as
 * it crosses them. It is the page backdrop, so the form card floats above
 * it and text contrast is untouched.
 *
 * Changes from the original, all forced by it being a backdrop:
 *  - Listens on `window`, not the container. The layer is
 *    `pointer-events: none` so it can never intercept a click, which also
 *    means container-level pointer events would never fire.
 *  - Takes its colour from the theme's `--primary` token instead of a hex
 *    prop, resolved through the canvas so any CSS colour form works.
 *  - Off entirely for touch and for reduced motion.
 *
 * The original's idle behaviour is kept and matters: once every cell has
 * faded the loop stops rather than spinning forever on an idle page.
 */
type Falloff = 'linear' | 'smooth' | 'sharp'

const FALLOFF: Record<Falloff, (t: number) => number> = {
  linear: (t) => t,
  smooth: (t) => t * t * (3 - 2 * t),
  sharp: (t) => t * t * t,
}

/** Resolves any CSS colour (including `hsl(var(--x))` output) to RGB. */
function toRgb(ctx: CanvasRenderingContext2D, color: string): [number, number, number] {
  ctx.fillStyle = '#000'
  ctx.fillStyle = color
  const v = ctx.fillStyle as string
  if (v.startsWith('#')) {
    const h = v.slice(1)
    const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h
    const n = parseInt(full.slice(0, 6), 16)
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
  }
  const m = v.match(/\d+/g)
  return m ? [Number(m[0]), Number(m[1]), Number(m[2])] : [30, 80, 210]
}

function accent(): string {
  if (typeof window === 'undefined') return '#1e50d2'
  const raw = getComputedStyle(document.documentElement).getPropertyValue('--primary').trim()
  return raw ? `hsl(${raw})` : '#1e50d2'
}

export function CursorGrid({
  cellSize = 64,
  radius = 190,
  falloff = 'smooth' as Falloff,
  holdTime = 320,
  fadeDuration = 900,
  lineWidth = 1.2,
  maxOpacity = 0.55,
  gridOpacity = 0.05,
  clickPulse = true,
  pulseSpeed = 620,
}: {
  cellSize?: number
  radius?: number
  falloff?: Falloff
  holdTime?: number
  fadeDuration?: number
  lineWidth?: number
  maxOpacity?: number
  gridOpacity?: number
  clickPulse?: boolean
  pulseSpeed?: number
} = {}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const reduced = usePrefersReducedMotion()
  const coarse = useCoarsePointer()
  const enabled = !reduced && !coarse

  // Read through a ref so tuning props never tear down the loop.
  const opts = useRef({ cellSize, radius, falloff, holdTime, fadeDuration, lineWidth, maxOpacity, gridOpacity, clickPulse, pulseSpeed })
  opts.current = { cellSize, radius, falloff, holdTime, fadeDuration, lineWidth, maxOpacity, gridOpacity, clickPulse, pulseSpeed }

  useEffect(() => {
    if (!enabled) return
    const container = containerRef.current
    const canvas = canvasRef.current
    if (!container || !canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    let cols = 0, rows = 0, offX = 0, offY = 0, w = 0, h = 0
    let alphas = new Float32Array(0)
    let touched = new Float64Array(0)
    const pulses: { x: number; y: number; t0: number }[] = []
    let raf = 0
    let running = false
    let last = 0
    let rgb: [number, number, number] = toRgb(ctx, accent())

    const rebuild = () => {
      const p = opts.current
      w = container.offsetWidth
      h = container.offsetHeight
      canvas.width = Math.max(1, Math.round(w * dpr))
      canvas.height = Math.max(1, Math.round(h * dpr))
      canvas.style.width = `${w}px`
      canvas.style.height = `${h}px`
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      cols = Math.ceil(w / p.cellSize) + 1
      rows = Math.ceil(h / p.cellSize) + 1
      offX = (w - cols * p.cellSize) / 2
      offY = (h - rows * p.cellSize) / 2
      alphas = new Float32Array(cols * rows)
      touched = new Float64Array(cols * rows)
      rgb = toRgb(ctx, accent())
    }

    const centre = (i: number): [number, number] => {
      const p = opts.current
      return [
        offX + (i % cols) * p.cellSize + p.cellSize / 2,
        offY + Math.floor(i / cols) * p.cellSize + p.cellSize / 2,
      ]
    }

    const energize = (x: number, y: number) => {
      const p = opts.current
      const r = Math.max(p.radius, 1)
      const ease = FALLOFF[p.falloff] ?? FALLOFF.linear
      const now = performance.now()
      const minCol = Math.max(0, Math.floor((x - r - offX) / p.cellSize))
      const maxCol = Math.min(cols - 1, Math.floor((x + r - offX) / p.cellSize))
      const minRow = Math.max(0, Math.floor((y - r - offY) / p.cellSize))
      const maxRow = Math.min(rows - 1, Math.floor((y + r - offY) / p.cellSize))
      for (let cRow = minRow; cRow <= maxRow; cRow++) {
        for (let cCol = minCol; cCol <= maxCol; cCol++) {
          const i = cRow * cols + cCol
          const [cx, cy] = centre(i)
          const dist = Math.hypot(cx - x, cy - y)
          if (dist > r) continue
          const level = ease(1 - dist / r) * p.maxOpacity
          if (level > alphas[i]) { alphas[i] = level; touched[i] = now }
          else if (level > 0) { touched[i] = now }
        }
      }
    }

    const draw = (now: number) => {
      const p = opts.current
      const dt = Math.min(now - last, 50)
      last = now
      ctx.clearRect(0, 0, w, h)
      const [cr, cg, cb] = rgb

      if (p.gridOpacity > 0) {
        ctx.strokeStyle = `rgba(${cr}, ${cg}, ${cb}, ${p.gridOpacity})`
        ctx.lineWidth = 1
        ctx.beginPath()
        for (let c = 0; c <= cols; c++) {
          const x = Math.round(offX + c * p.cellSize) + 0.5
          ctx.moveTo(x, 0); ctx.lineTo(x, h)
        }
        for (let r2 = 0; r2 <= rows; r2++) {
          const y = Math.round(offY + r2 * p.cellSize) + 0.5
          ctx.moveTo(0, y); ctx.lineTo(w, y)
        }
        ctx.stroke()
      }

      for (let pi = pulses.length - 1; pi >= 0; pi--) {
        const pulse = pulses[pi]
        const ringR = ((now - pulse.t0) / 1000) * p.pulseSpeed
        if (ringR > Math.hypot(w, h)) { pulses.splice(pi, 1); continue }
        const band = p.cellSize
        const minCol = Math.max(0, Math.floor((pulse.x - ringR - band - offX) / p.cellSize))
        const maxCol = Math.min(cols - 1, Math.floor((pulse.x + ringR + band - offX) / p.cellSize))
        const minRow = Math.max(0, Math.floor((pulse.y - ringR - band - offY) / p.cellSize))
        const maxRow = Math.min(rows - 1, Math.floor((pulse.y + ringR + band - offY) / p.cellSize))
        for (let cRow = minRow; cRow <= maxRow; cRow++) {
          for (let cCol = minCol; cCol <= maxCol; cCol++) {
            const i = cRow * cols + cCol
            const [cx, cy] = centre(i)
            if (Math.abs(Math.hypot(cx - pulse.x, cy - pulse.y) - ringR) < band / 2 && p.maxOpacity > alphas[i]) {
              alphas[i] = p.maxOpacity
              touched[i] = now
            }
          }
        }
      }

      let visible = pulses.length > 0
      const fadeStep = dt / Math.max(p.fadeDuration, 16)
      const half = p.cellSize / 2

      for (let i = 0; i < alphas.length; i++) {
        let a = alphas[i]
        if (a <= 0) continue
        if (now - touched[i] > p.holdTime) {
          a = Math.max(0, a - fadeStep)
          alphas[i] = a
          if (a <= 0) continue
        }
        visible = true
        const [cx, cy] = centre(i)
        const gradient = ctx.createRadialGradient(cx, cy, half * 0.1, cx, cy, p.cellSize)
        gradient.addColorStop(0, `rgba(${cr}, ${cg}, ${cb}, ${a})`)
        gradient.addColorStop(1, `rgba(${cr}, ${cg}, ${cb}, 0)`)
        ctx.beginPath()
        ctx.rect(cx - half + 0.5, cy - half + 0.5, p.cellSize - 1, p.cellSize - 1)
        ctx.strokeStyle = gradient
        ctx.lineWidth = p.lineWidth
        ctx.stroke()
      }

      // Stop once nothing is lit: a backdrop must not hold an animation
      // frame loop open on an idle sign-in page.
      if (visible) {
        raf = requestAnimationFrame(draw)
      } else {
        running = false
        if (opts.current.gridOpacity <= 0) ctx.clearRect(0, 0, w, h)
      }
    }

    const wake = () => {
      if (running) return
      running = true
      last = performance.now()
      raf = requestAnimationFrame(draw)
    }

    const local = (e: PointerEvent): [number, number] => {
      const rect = canvas.getBoundingClientRect()
      return [e.clientX - rect.left, e.clientY - rect.top]
    }
    const onMove = (e: PointerEvent) => { const [x, y] = local(e); energize(x, y); wake() }
    const onDown = (e: PointerEvent) => {
      if (!opts.current.clickPulse) return
      const [x, y] = local(e)
      pulses.push({ x, y, t0: performance.now() })
      wake()
    }

    const ro = new ResizeObserver(() => { rebuild(); wake() })
    ro.observe(container)
    rebuild()
    wake()

    window.addEventListener('pointermove', onMove, { passive: true })
    window.addEventListener('pointerdown', onDown, { passive: true })

    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerdown', onDown)
    }
  }, [enabled, cellSize])

  if (!enabled) return null

  return (
    <div
      ref={containerRef}
      data-testid="cursor-grid"
      aria-hidden="true"
      style={{ position: 'fixed', inset: 0, overflow: 'hidden', pointerEvents: 'none', zIndex: 0 }}
    >
      <canvas ref={canvasRef} style={{ display: 'block', width: '100%', height: '100%' }} />
    </div>
  )
}
