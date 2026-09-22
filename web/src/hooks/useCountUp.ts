import { useEffect, useRef, useState } from 'react'
import { usePrefersReducedMotion } from './usePrefersReducedMotion'

// Matches the CSS easing used across the dashboard, cubic-bezier(.4,0,.2,1),
// closely enough for a numeral: fast out, gentle settle.
function easeOut(t: number): number {
  return 1 - Math.pow(1 - t, 3)
}

/**
 * Counts a numeral up to `value` on mount and whenever `value` changes.
 * Returns `value` immediately when the viewer asked for reduced motion,
 * so the final state is always reachable without waiting.
 */
export function useCountUp(value: number, durationMs = 600): number {
  const reduced = usePrefersReducedMotion()
  const [display, setDisplay] = useState(() => (reduced ? value : 0))
  const frameRef = useRef<number | null>(null)

  useEffect(() => {
    if (reduced || value === 0 || !Number.isFinite(value)) {
      setDisplay(value)
      return
    }
    const from = 0
    const start = performance.now()
    const tick = (now: number) => {
      const elapsed = now - start
      if (elapsed >= durationMs) {
        setDisplay(value)
        frameRef.current = null
        return
      }
      setDisplay(Math.round(from + (value - from) * easeOut(elapsed / durationMs)))
      frameRef.current = requestAnimationFrame(tick)
    }
    setDisplay(from)
    frameRef.current = requestAnimationFrame(tick)
    return () => {
      if (frameRef.current !== null) cancelAnimationFrame(frameRef.current)
      frameRef.current = null
    }
  }, [value, durationMs, reduced])

  return display
}
