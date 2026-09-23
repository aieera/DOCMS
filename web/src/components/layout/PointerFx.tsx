import { useEffect, useRef, type RefObject } from 'react'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { useCoarsePointer } from '@/hooks/usePointerParallax'

// Each orb chases the pointer at its own rate, so they string out behind
// it and settle at different times. Lower = laggier.
// The layer paints beneath the form card, so the glow never washes over
// input text — which is why it can afford this much presence.
const ORBS = [
  { ease: 0.16, size: 400, tint: 'hsl(var(--primary) / 0.26)' },
  { ease: 0.09, size: 260, tint: 'hsl(var(--primary) / 0.18)' },
]
// Below this movement per frame the orbs have caught up; keep the loop
// alive a few frames longer, then stop it. A decorative effect must not
// hold a requestAnimationFrame loop open on an idle login screen.
const SETTLED_PX = 0.2
const IDLE_FRAMES = 8

/**
 * The pointer effects layer for the auth screens: two trailing orbs, a
 * click ripple, and the `--fx-x`/`--fx-y` custom properties that let the
 * brand pane's light follow the pointer.
 *
 * Nothing here runs through React state. One `pointermove` listener
 * records coordinates into a ref; one animation frame loop writes
 * transforms directly to DOM nodes and the two custom properties onto the
 * surface element. A state update at pointer-move rates would re-render
 * the sign-in form on every frame.
 *
 * The layer is `pointer-events: none` and `aria-hidden`, so it cannot
 * intercept a click or reach a screen reader.
 *
 * Orbs and light-follow are off for touch (a tap is not a hover) and for
 * reduced motion. The ripple survives on touch, where a press ripple is a
 * familiar idiom, but not under reduced motion.
 */
export function PointerFx({ surfaceRef }: { surfaceRef: RefObject<HTMLElement> }) {
  const layerRef = useRef<HTMLDivElement>(null)
  const orbRefs = useRef<(HTMLDivElement | null)[]>([])
  const reduced = usePrefersReducedMotion()
  const coarse = useCoarsePointer()
  const trailing = !reduced && !coarse

  // --- trailing orbs + light position -------------------------------
  useEffect(() => {
    if (!trailing) return
    const surface = surfaceRef.current
    const target = { x: window.innerWidth / 2, y: window.innerHeight / 2 }
    const pos = ORBS.map(() => ({ ...target }))
    let frame: number | null = null
    let idle = 0
    let seen = false

    const tick = () => {
      let moved = 0
      pos.forEach((p, i) => {
        p.x += (target.x - p.x) * ORBS[i].ease
        p.y += (target.y - p.y) * ORBS[i].ease
        moved = Math.max(moved, Math.abs(target.x - p.x), Math.abs(target.y - p.y))
        const node = orbRefs.current[i]
        if (node) {
          node.style.transform = `translate3d(${p.x - ORBS[i].size / 2}px, ${p.y - ORBS[i].size / 2}px, 0)`
          node.style.opacity = seen ? '1' : '0'
        }
      })
      if (surface) {
        surface.style.setProperty('--fx-x', (target.x / window.innerWidth).toFixed(4))
        surface.style.setProperty('--fx-y', (target.y / window.innerHeight).toFixed(4))
      }
      // Stop once the orbs have caught up and stayed caught up.
      idle = moved < SETTLED_PX ? idle + 1 : 0
      if (idle > IDLE_FRAMES) { frame = null; return }
      frame = requestAnimationFrame(tick)
    }

    const onMove = (e: PointerEvent) => {
      target.x = e.clientX
      target.y = e.clientY
      seen = true
      idle = 0
      if (frame === null) frame = requestAnimationFrame(tick)
    }

    const onLeave = () => {
      seen = false
      if (frame === null) frame = requestAnimationFrame(tick)
    }

    window.addEventListener('pointermove', onMove, { passive: true })
    document.addEventListener('pointerleave', onLeave, { passive: true })
    return () => {
      window.removeEventListener('pointermove', onMove)
      document.removeEventListener('pointerleave', onLeave)
      if (frame !== null) cancelAnimationFrame(frame)
      surface?.style.removeProperty('--fx-x')
      surface?.style.removeProperty('--fx-y')
    }
  }, [trailing, surfaceRef])

  // --- click ripple --------------------------------------------------
  useEffect(() => {
    if (reduced) return
    const onDown = (e: PointerEvent) => {
      const layer = layerRef.current
      if (!layer) return
      const ring = document.createElement('span')
      ring.dataset.part = 'ripple'
      ring.className = 'auth-ripple'
      ring.style.left = `${e.clientX}px`
      ring.style.top = `${e.clientY}px`
      // Self-removing: nothing accumulates in the DOM over a long session.
      ring.addEventListener('animationend', () => ring.remove(), { once: true })
      layer.appendChild(ring)
    }
    window.addEventListener('pointerdown', onDown, { passive: true })
    return () => window.removeEventListener('pointerdown', onDown)
  }, [reduced])

  return (
    <div
      ref={layerRef}
      data-testid="pointer-fx"
      aria-hidden="true"
      // Inline rather than a utility class so the guarantee cannot be
      // lost to a CSS purge or an ordering accident.
      style={{ position: 'fixed', inset: 0, pointerEvents: 'none', overflow: 'hidden', zIndex: 0 }}
    >
      {trailing && ORBS.map((orb, i) => (
        <div
          key={orb.size}
          data-part="orb"
          ref={(n) => { orbRefs.current[i] = n }}
          style={{
            position: 'absolute',
            width: orb.size,
            height: orb.size,
            borderRadius: '9999px',
            // A pre-blurred gradient. `filter: blur()` on a moving element
            // re-rasterises every frame; a gradient composites for free.
            background: `radial-gradient(closest-side, ${orb.tint} 0%, transparent 100%)`,
            opacity: 0,
            transition: 'opacity 420ms cubic-bezier(0.4, 0, 0.2, 1)',
            willChange: 'transform',
          }}
        />
      ))}
    </div>
  )
}
