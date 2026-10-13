import { useId } from 'react'

import { cn } from '@/lib/cn'
import type { Point } from './metrics'

const W = 120
const H = 34

/**
 * A 120x34 inline-SVG trend line. Inline rather than recharts because a
 * sparkline has no axes, no tooltip and no legend — mounting a chart
 * library four times in the KPI strip would cost far more than it earns.
 *
 * Decorative by default: the tile's aria-label already carries the
 * figure, and the full series is available in the activity chart's data
 * table, so announcing it again here would be noise.
 */
export function Sparkline({ points, className }: { points: Point[]; className?: string }) {
  const gradientId = useId()
  if (points.length < 2) return null

  const values = points.map((p) => p.value)
  const min = Math.min(...values)
  const max = Math.max(...values)
  // A flat series has zero range; dividing by it yields NaN coordinates
  // and an invisible line, so pin it to the vertical middle instead.
  const range = max - min || 1
  const step = W / (points.length - 1)

  const coords = points.map((p, i) => {
    const x = i * step
    const y = max === min ? H / 2 : H - ((p.value - min) / range) * (H - 4) - 2
    return `${x.toFixed(2)},${y.toFixed(2)}`
  })

  const line = coords.join(' ')
  const area = `0,${H} ${line} ${W},${H}`

  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      className={cn('h-8 w-full', className)}
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="currentColor" stopOpacity="0.25" />
          <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <polygon points={area} fill={`url(#${gradientId})`} />
      <polyline
        points={line}
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="dash-draw"
        style={{ '--dash-len': 400 } as React.CSSProperties}
      />
    </svg>
  )
}
