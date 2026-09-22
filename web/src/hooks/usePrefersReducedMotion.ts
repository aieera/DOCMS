import { useEffect, useState } from 'react'

const QUERY = '(prefers-reduced-motion: reduce)'

function read(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia(QUERY).matches
}

/**
 * Live `prefers-reduced-motion` state. The global CSS block in
 * globals.css is the backstop for transitions; JS-driven motion
 * (count-up, stroke-dashoffset draws) has to ask explicitly, which
 * is what this is for.
 */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(read)

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(QUERY)
    const onChange = () => setReduced(mq.matches)
    onChange()
    // Safari < 14 only has the deprecated addListener.
    if (typeof mq.addEventListener === 'function') {
      mq.addEventListener('change', onChange)
      return () => mq.removeEventListener('change', onChange)
    }
    mq.addListener(onChange)
    return () => mq.removeListener(onChange)
  }, [])

  return reduced
}
