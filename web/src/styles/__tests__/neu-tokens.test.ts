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
  const m = scope.match(new RegExp(`--${name}:\\s*([\\d.]+)\\s+([\\d.]+)%\\s+([\\d.]+)%`))
  if (!m) throw new Error(`token --${name} not found`)
  return [Number(m[1]), Number(m[2]), Number(m[3])]
}
function hslToRgb([h, s, l]: [number, number, number]): [number, number, number] {
  const S = s / 100, L = l / 100
  const c = (1 - Math.abs(2 * L - 1)) * S
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1))
  const m = L - c / 2
  const [r, g, b] = h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x] : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x]
  return [(r + m) * 255, (g + m) * 255, (b + m) * 255]
}
function lum(rgb: [number, number, number]): number {
  const [r, g, b] = rgb.map((v) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4) })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
function contrast(scope: string, fg: string, bg: string): number {
  const l1 = lum(hslToRgb(token(scope, fg))), l2 = lum(hslToRgb(token(scope, bg)))
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
  describe(`neumorphic tokens — ${label} AA text contrast`, () => {
    it.each(PAIRS)(`--%s on --%s ≥ 4.5:1 (${label})`, (fg, bg) => {
      expect(contrast(scope, fg, bg)).toBeGreaterThanOrEqual(4.5)
    })
  })
}
describe('neumorphic shape + shadow layer', () => {
  it('radius is 1.25rem', () => { expect(light).toMatch(/--radius:\s*1\.25rem/) })
  it('defines the neumorphic shadow vars in light and dark', () => {
    for (const s of [light, dark]) for (const v of ['--nm-shadow', '--nm-shadow-inset', '--nm-light', '--nm-dark']) {
      expect(s).toContain(v)
    }
  })
})
