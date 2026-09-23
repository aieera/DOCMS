import { useEffect, useRef, type RefObject } from 'react'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { useCoarsePointer } from '@/hooks/usePointerParallax'

/**
 * Pointer plumbing for the auth screens: publishes `--fx-x`/`--fx-y` on the
 * surface so the brand pane's light can follow the pointer in CSS, and
 * draws a soft ring on each click.
 *
 * The trailing orbs that used to live here were replaced by the
 * AntigravityField particle system — a blurred blob chasing the cursor
 * read as smeared rather than deliberate.
 *
 * Nothing here runs through React state: a state update at pointer-move
 * rates would re-render the sign-in form on every frame. Updates are
 * coalesced into one animation frame and written straight to the DOM.
 *
 * The layer is `pointer-events: none` and `aria-hidden`, so it can neither
 * intercept a click nor reach a screen reader. Tracking is off for touch
 * and for reduced motion; the ripple survives on touch, where a press
 * ripple is a familiar idiom, but not under reduced motion.
 */
export function PointerFx({ surfaceRef }: { surfaceRef: RefObject<HTMLElement> }) {
  const layerRef = useRef<HTMLDivElement>(null)
  const reduced = usePrefersReducedMotion()
  const coarse = useCoarsePointer()
  const tracking = !reduced && !coarse

  // --- publish the pointer for the CSS light -------------------------
  useEffect(() => {
    if (!tracking) return
    const surface = surfaceRef.current
    if (!surface) return
    let frame: number | null = null
    let next = { x: 0.5, y: 0.5 }

    const flush = () => {
      frame = null
      surface.style.setProperty('--fx-x', next.x.toFixed(4))
      surface.style.setProperty('--fx-y', next.y.toFixed(4))
    }

    const onMove = (e: PointerEvent) => {
      next = { x: e.clientX / window.innerWidth, y: e.clientY / window.innerHeight }
      if (frame === null) frame = requestAnimationFrame(flush)
    }

    window.addEventListener('pointermove', onMove, { passive: true })
    return () => {
      window.removeEventListener('pointermove', onMove)
      if (frame !== null) cancelAnimationFrame(frame)
      surface.style.removeProperty('--fx-x')
      surface.style.removeProperty('--fx-y')
    }
  }, [tracking, surfaceRef])

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
    />
  )
}
