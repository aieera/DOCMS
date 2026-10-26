export interface ChartTheme {
  grid: string
  axis: string
  /** Ordered categorical ramp: the design system's `--chart-1…5` tokens. */
  series: string[]
  /** The lead series colour (`--chart-1`). */
  area: string
}

/**
 * Every colour is a `var()` reference, not a resolved value. Recharts passes
 * `fill` / `stroke` straight through to SVG, and the browser resolves the
 * variable at paint time — so a chart follows the light/dark theme the moment
 * the `.dark` class flips, with no `getComputedStyle` read and no observer.
 *
 * The one thing a `var()` cannot do is be parsed in JS: anything that needs
 * the concrete colour (appending an alpha channel, deriving a tint) has to use
 * `color-mix()` in CSS instead.
 */
const CHART_THEME: ChartTheme = {
  grid: 'var(--border)',
  axis: 'var(--muted-foreground)',
  series: ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)'],
  area: 'var(--chart-1)',
}

/**
 * Chart colors that follow the active theme. ComplianceDashboard.tsx
 * hardcodes hex values and therefore does not theme; this is the
 * replacement pattern for anything new.
 */
export function useChartTheme(): ChartTheme {
  return CHART_THEME
}
