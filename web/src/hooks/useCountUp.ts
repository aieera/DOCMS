import { useEffect, useRef, useState } from 'react'
import { usePrefersReducedMotion } from './usePrefersReducedMotion'

// Matches the CSS easing used across the dashboard, cubic-bezier(.4,0,.2,1),
// closely enough for a numeral: fast out, gentle settle.
function easeOut(t: number): number {
  return 1 - Math.pow(1 - t, 3)
}

/**
 * Counts a numeral up to `value` on mount (from 0) and, whenever `value`
 * changes, from the number currently on screen to the new one. Returns
 * `value` immediately when the viewer asked for reduced motion, so the
 * final state is always reachable without waiting.
 */
export function useCountUp(value: number, durationMs = 600): number {
  const reduced = usePrefersReducedMotion()
  const [display, setDisplay] = useState(() => (reduced ? value : 0))
  const frameRef = useRef<number | null>(null)
  // I5: the number on screen right now. A refetch that moves 3 to 4 must
  // animate 3 → 4, not restart at 0 — a transient false 0 on every poll.
  // Kept in a ref so an interrupted animation resumes from where it was.
  const shownRef = useRef(reduced ? value : 0)

  useEffect(() => {
    const from = shownRef.current
    if (reduced || from === value || !Number.isFinite(value) || !Number.isFinite(from)) {
      shownRef.current = value
      setDisplay(value)
      return
    }
    const start = performance.now()
    const tick = (now: number) => {
      const elapsed = now - start
      if (elapsed >= durationMs) {
        shownRef.current = value
        setDisplay(value)
        frameRef.current = null
        return
      }
      const next = Math.round(from + (value - from) * easeOut(elapsed / durationMs))
      shownRef.current = next
      setDisplay(next)
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
