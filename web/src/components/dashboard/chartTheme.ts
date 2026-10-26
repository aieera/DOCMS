import { useEffect, useState } from 'react'

export interface ChartTheme {
  grid: string
  axis: string
  /** Ordered categorical ramp, derived from the single accent. */
  series: string[]
  area: string
}

// Tokens are plain colour strings (hex / rgb()), so they pass straight
// through to Recharts.
function token(styles: CSSStyleDeclaration, name: string, fallback: string): string {
  return styles.getPropertyValue(name).trim() || fallback
}

/** `#rgb` / `#rrggbb` → [hue°, saturation%]; null for any other form. */
function hueSat(hex: string): [number, number] | null {
  const m = hex.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i)
  if (!m) return null
  const full = m[1].length === 3 ? m[1].replace(/./g, (c) => c + c) : m[1]
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(full.slice(i, i + 2), 16) / 255)
  const max = Math.max(r, g, b), min = Math.min(r, g, b), d = max - min
  if (d === 0) return [0, 0]
  const l = (max + min) / 2
  const s = d / (1 - Math.abs(2 * l - 1))
  const h = max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4
  return [Math.round(((h * 60) + 360) % 360), Math.round(s * 100)]
}

function read(): ChartTheme {
  if (typeof window === 'undefined') {
    return { grid: '#e5e5e5', axis: '#737373', series: ['#171717'], area: '#171717' }
  }
  const s = getComputedStyle(document.documentElement)
  const primary = token(s, '--primary', '#171717')
  const [h, sat] = hueSat(primary) ?? [0, 0]
  // One accent, stepped in lightness — the single-accent rule from the
  // neumorphic phase. Lightness stays inside a band that keeps every
  // step distinguishable against both canvases.
  const ramp = [30, 42, 54, 66, 78].map((l) => `hsl(${h} ${sat}% ${l}%)`)
  return {
    grid: token(s, '--border', '#e5e5e5'),
    axis: token(s, '--muted-foreground', '#737373'),
    series: [primary, ...ramp],
    area: primary,
  }
}

/**
 * Chart colors that follow the active theme. ComplianceDashboard.tsx
 * hardcodes hex values and therefore does not theme; this is the
 * replacement pattern for anything new.
 */
export function useChartTheme(): ChartTheme {
  const [theme, setTheme] = useState<ChartTheme>(read)

  useEffect(() => {
    setTheme(read())
    const target = document.documentElement
    const observer = new MutationObserver(() => setTheme(read()))
    observer.observe(target, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])

  return theme
}
