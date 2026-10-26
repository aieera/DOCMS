import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const css = readFileSync(resolve(__dirname, '../globals.css'), 'utf8')
function block(sel: string): string {
  const start = css.indexOf(sel)
  if (start < 0) throw new Error(`missing block ${sel}`)
  const open = css.indexOf('{', start)
  let depth = 0, i = open
  for (; i < css.length; i++) { if (css[i] === '{') depth++; else if (css[i] === '}') { depth--; if (depth === 0) break } }
  return css.slice(open, i)
}
const light = block(':root')
const dark = block('.dark')

function token(scope: string, name: string): [number, number, number] {
  // Test helper: name is a literal token name from this test file.
  // nosemgrep: detect-non-literal-regexp
  const m = scope.match(new RegExp(`[\\s;{]--${name}:\\s*([^;]+);`))
  if (!m) throw new Error(`token --${name} not found`)
  return toRgb(m[1].trim())
}
// Tokens are plain colour strings: `#rrggbb` or `rgb(r g b / a)`.
function toRgb(value: string): [number, number, number] {
  const hex = value.match(/^#([0-9a-f]{6})$/i)
  if (hex) {
    const n = parseInt(hex[1], 16)
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
  }
  const rgb = value.match(/^rgb\(\s*(\d+)\s+(\d+)\s+(\d+)/)
  if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3])]
  throw new Error(`unparseable colour ${value}`)
}
function lum(rgb: [number, number, number]): number {
  const [r, g, b] = rgb.map((v) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4) })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
function contrast(scope: string, fg: string, bg: string): number {
  const l1 = lum(token(scope, fg)), l2 = lum(token(scope, bg))
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}
const PAIRS: [string, string][] = [
  ['foreground', 'background'], ['foreground', 'card'], ['foreground', 'muted'],
  ['muted-foreground', 'background'], ['muted-foreground', 'muted'], ['muted-foreground', 'card'],
  ['primary-foreground', 'primary'], ['primary', 'background'],
  ['destructive-foreground', 'destructive'], ['success-foreground', 'success'],
  ['info-foreground', 'info'], ['sidebar-foreground', 'sidebar'],
]

for (const [label, scope] of [['light', light], ['dark', dark]] as const) {
  describe(`theme tokens — ${label} AA text contrast`, () => {
    it.each(PAIRS)(`--%s on --%s ≥ 4.5:1 (${label})`, (fg, bg) => {
      expect(contrast(scope, fg, bg)).toBeGreaterThanOrEqual(4.5)
    })
  })
}
describe('theme shape + elevation layer', () => {
  it('radius is 10px', () => { expect(light).toMatch(/--radius:\s*10px/) })
  it('defines the elevation shadow vars in light and dark', () => {
    for (const s of [light, dark]) for (const v of ['--shadow-xs', '--shadow-sm', '--shadow-md', '--shadow-lg']) {
      expect(s).toContain(v)
    }
  })
})

// Regression guard for the tinted-text-on-same-color-tint defect class
// (fixed in the neu-ui final wave): `text-<color>` on a `bg-<color>/NN`
// translucent tint of the SAME color can drop well below WCAG AA because
// the tint composites toward the text color, eroding the effective
// background contrast. The plain PAIRS above only check solid
// token-on-token pairs, so this class was un-gated. These assertions
// composite the tint alpha over --card (the usual ambient surface these
// pills/banners sit on) and lock in that the established fixes — the
// text-warning-strong token, and text-foreground swapped in for
// destructive/info/primary/success small-text-on-same-tint — stay AA.
function alphaComposite(fg: [number, number, number], alphaPct: number, bg: [number, number, number]): [number, number, number] {
  const a = alphaPct / 100
  return [0, 1, 2].map((i) => a * fg[i] + (1 - a) * bg[i]) as [number, number, number]
}
function contrastOnTint(scope: string, fg: string, tint: string, alphaPct: number, ambient = 'card'): number {
  const fgRgb = token(scope, fg)
  const tintRgb = token(scope, tint)
  const ambientRgb = token(scope, ambient)
  const effBg = alphaComposite(tintRgb, alphaPct, ambientRgb)
  const l1 = lum(fgRgb), l2 = lum(effBg)
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}

// [text color token, tint color token, alpha %][] — the known-used
// pairs from the final fix wave.
const WARNING_STRONG_TINTS: [string, number][] = [[ 'warning', 5], ['warning', 10], ['warning', 15]]
const FOREGROUND_ON_TINTS: [string, number][] = [
  ['destructive', 10], ['destructive', 15],
  ['primary', 10], ['primary', 15],
  ['success', 15],
  ['info', 15],
]

for (const [label, scope] of [['light', light], ['dark', dark]] as const) {
  describe(`tinted-text fixes — ${label} AA contrast`, () => {
    it.each(WARNING_STRONG_TINTS)(`text-warning-strong on bg-warning/%i%% ≥ 4.5:1 (${label})`, (tint, alphaPct) => {
      expect(contrastOnTint(scope, 'warning-strong', tint, alphaPct)).toBeGreaterThanOrEqual(4.5)
    })
    it.each(FOREGROUND_ON_TINTS)(`text-foreground on bg-%s/%i%% ≥ 4.5:1 (${label})`, (tint, alphaPct) => {
      expect(contrastOnTint(scope, 'foreground', tint, alphaPct)).toBeGreaterThanOrEqual(4.5)
    })
  })
}
