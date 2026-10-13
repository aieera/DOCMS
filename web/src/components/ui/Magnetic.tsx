import { useEffect, useRef, type ReactNode } from 'react'

import { usePrefersReducedMotion } from '@/hooks/usePrefersReducedMotion'
import { useCoarsePointer } from '@/hooks/usePointerParallax'

/**
 * Leans its child a few pixels toward the pointer once the pointer is
 * within `radius`, then releases it.
 *
 * Two deliberate safeguards:
 *  - It releases on `pointerdown`. A magnetic control that is still
 *    chasing the cursor when the click lands can move out from under it,
 *    which is the classic way this effect breaks a form.
 *  - The pull is capped at `max` px, so the control never drifts far
 *    enough to overlap a neighbour or to matter for a click target.
 *
 * Writes the transform straight to the node: at pointer-move rates a
 * state update here would re-render the whole form on every frame.
 * Off entirely for touch and for reduced motion.
 */
export function Magnetic({
  children,
  radius = 140,
  max = 6,
  className,
}: {
  children: ReactNode
  radius?: number
  max?: number
  className?: string
}) {
  const ref = useRef<HTMLSpanElement>(null)
  const frame = useRef<number | null>(null)
  const released = useRef(false)
  const reduced = usePrefersReducedMotion()
  const coarse = useCoarsePointer()
  const enabled = !reduced && !coarse

  useEffect(() => {
    const node = ref.current
    if (!node) return
    if (!enabled) {
      node.style.transform = 'translate3d(0px, 0px, 0)'
      return
    }

    const apply = (x: number, y: number) => {
      node.style.transform = `translate3d(${x.toFixed(2)}px, ${y.toFixed(2)}px, 0)`
    }
    apply(0, 0)

    const onMove = (e: PointerEvent) => {
      if (released.current) return
      const rect = node.getBoundingClientRect()
      if (!rect.width || !rect.height) return
      const cx = rect.left + rect.width / 2
      const cy = rect.top + rect.height / 2
      const dx = e.clientX - cx
      const dy = e.clientY - cy
      const dist = Math.hypot(dx, dy)
      const pull = dist > radius ? 0 : 1 - dist / radius
      if (frame.current !== null) cancelAnimationFrame(frame.current)
      frame.current = requestAnimationFrame(() => {
        frame.current = null
        apply((dx / radius) * max * pull, (dy / radius) * max * pull)
      })
    }

    // Release before the click resolves, and stay released until the
    // pointer lifts, so the control is stationary for the whole press.
    const onDown = () => {
      released.current = true
      if (frame.current !== null) { cancelAnimationFrame(frame.current); frame.current = null }
      apply(0, 0)
    }
    const onUp = () => { released.current = false }

    window.addEventListener('pointermove', onMove, { passive: true })
    window.addEventListener('pointerdown', onDown, { passive: true })
    window.addEventListener('pointerup', onUp, { passive: true })
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerdown', onDown)
      window.removeEventListener('pointerup', onUp)
      if (frame.current !== null) cancelAnimationFrame(frame.current)
      frame.current = null
      released.current = false
    }
  }, [enabled, radius, max])

  return (
    <span
      ref={ref}
      className={className}
      style={{ display: 'block', willChange: enabled ? 'transform' : undefined, transition: 'transform 260ms cubic-bezier(0.4, 0, 0.2, 1)' }}
    >
      {children}
    </span>
  )
}
