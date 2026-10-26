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

/**
 * Lifecycle states keyed to a colour, so a state is the same colour in every
 * tenant and in every place it appears (ring, legend, anything else). Indexing
 * the palette by slice position repeats hues once there are more than five
 * states, and moves a state's colour whenever another state's count overtakes
 * it. Five states take the five chart tokens; the two rarest end states take
 * neutrals so they read as "set aside" rather than competing for attention.
 */
export const LIFECYCLE_COLORS: Readonly<Record<string, string>> = {
  draft: 'var(--chart-4)',
  in_review: 'var(--chart-5)',
  active: 'var(--chart-1)',
  superseded: 'var(--chart-3)',
  retained: 'var(--chart-2)',
  archived: 'var(--muted-foreground)',
  disposed: 'var(--ring)',
}

/** A state the catalogue does not know yet. */
const UNKNOWN_STATE_COLOR = 'var(--border)'

export function lifecycleColor(key: string): string {
  // hasOwnProperty, not `??`: a key like "constructor" would find Object.prototype's.
  return Object.prototype.hasOwnProperty.call(LIFECYCLE_COLORS, key) ? LIFECYCLE_COLORS[key] : UNKNOWN_STATE_COLOR
}

/** The neutral segment that folds every slice past the palette into "Other". */
export const OTHER_SEGMENT_COLOR = 'var(--muted-foreground)'
