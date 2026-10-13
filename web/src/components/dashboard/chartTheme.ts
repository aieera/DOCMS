import { useEffect, useState } from 'react'

export interface ChartTheme {
  grid: string
  axis: string
  /** Ordered categorical ramp, derived from the single accent. */
  series: string[]
  area: string
}

// `--primary` etc. are stored as bare HSL channels ("223 75% 47%") so
// Tailwind can compose them with an alpha. Recharts needs a real color
// string, so wrap them back up here.
function channel(styles: CSSStyleDeclaration, name: string, fallback: string): string {
  const raw = styles.getPropertyValue(name).trim()
  return raw ? `hsl(${raw})` : fallback
}

function read(): ChartTheme {
  if (typeof window === 'undefined') {
    return { grid: '#c4c9d4', axis: '#465063', series: ['#1e50d2'], area: '#1e50d2' }
  }
  const s = getComputedStyle(document.documentElement)
  const primary = s.getPropertyValue('--primary').trim() || '223 75% 47%'
  const [h, sat] = primary.split(/\s+/)
  // One accent, stepped in lightness — the single-accent rule from the
  // neumorphic phase. Lightness stays inside a band that keeps every
  // step distinguishable against both canvases.
  const ramp = [30, 42, 54, 66, 78].map((l) => `hsl(${h} ${sat} ${l}%)`)
  return {
    grid: channel(s, '--border', '#c4c9d4'),
    axis: channel(s, '--muted-foreground', '#465063'),
    series: [channel(s, '--primary', '#1e50d2'), ...ramp],
    area: channel(s, '--primary', '#1e50d2'),
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
