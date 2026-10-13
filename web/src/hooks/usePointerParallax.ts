import { useCallback, useEffect, useRef, useState } from 'react'

import { usePrefersReducedMotion } from './usePrefersReducedMotion'

const COARSE = '(pointer: coarse)'

/** True on touch-first devices, where "pointer move" is a tap, not a hover. */
export function useCoarsePointer(): boolean {
  const read = () =>
    typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia(COARSE).matches
  const [coarse, setCoarse] = useState(read)

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(COARSE)
    const onChange = () => setCoarse(mq.matches)
    onChange()
    if (typeof mq.addEventListener === 'function') {
      mq.addEventListener('change', onChange)
      return () => mq.removeEventListener('change', onChange)
    }
    mq.addListener(onChange)
    return () => mq.removeListener(onChange)
  }, [])

  return coarse
}

export interface Parallax {
  /** False when the tilt must not engage at all. */
  enabled: boolean
  /** Spread onto the tilting element. */
  handlers: {
    onPointerMove: (e: React.PointerEvent<HTMLElement>) => void
    onPointerLeave: () => void
  }
  transform: string
}

/**
 * Tilts an element a few degrees toward the pointer.
 *
 * Disabled outright for coarse pointers and for viewers who asked for
 * reduced motion — on a touch device the tilt would fire on every tap and
 * fight the scroll, and a parallax that ignores the motion preference is
 * exactly the kind of effect that preference exists to stop.
 *
 * Updates are coalesced into one animation frame, and only `transform`
 * changes, so the tilt stays on the compositor.
 */
export function usePointerParallax(maxDeg = 5): Parallax {
  const reduced = usePrefersReducedMotion()
  const coarse = useCoarsePointer()
  const enabled = !reduced && !coarse

  const [tilt, setTilt] = useState({ x: 0, y: 0 })
  const frame = useRef<number | null>(null)

  useEffect(() => {
    if (!enabled) setTilt({ x: 0, y: 0 })
  }, [enabled])

  useEffect(() => () => {
    if (frame.current !== null) cancelAnimationFrame(frame.current)
  }, [])

  const onPointerMove = useCallback((e: React.PointerEvent<HTMLElement>) => {
    if (!enabled) return
    const rect = e.currentTarget.getBoundingClientRect()
    if (!rect.width || !rect.height) return
    // -0.5 .. 0.5 from the element's centre.
    const px = (e.clientX - rect.left) / rect.width - 0.5
    const py = (e.clientY - rect.top) / rect.height - 0.5
    const next = { x: -py * maxDeg * 2, y: px * maxDeg * 2 }
    if (frame.current !== null) cancelAnimationFrame(frame.current)
    frame.current = requestAnimationFrame(() => {
      frame.current = null
      setTilt(next)
    })
  }, [enabled, maxDeg])

  const onPointerLeave = useCallback(() => {
    if (frame.current !== null) {
      cancelAnimationFrame(frame.current)
      frame.current = null
    }
    setTilt({ x: 0, y: 0 })
  }, [])

  return {
    enabled,
    handlers: { onPointerMove, onPointerLeave },
    transform: `rotateX(${tilt.x.toFixed(2)}deg) rotateY(${tilt.y.toFixed(2)}deg)`,
  }
}
