import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// Read from disk: under vitest a `globals.css?raw` import is stubbed to ''.
const css = readFileSync(resolve(__dirname, '../globals.css'), 'utf8')

/**
 * Design tokens (spec 2026-10-25 §3): flat neutral palette, light + dark.
 * Tokens are plain colour strings (hex / rgb()), consumed as `var(--x)`,
 * not HSL channel triples.
 */
function tokens(selector: ':root' | '.dark'): Record<string, string> {
  const escaped = selector.replace('.', '\\.')
  // Test helper: the selector is one of two literals above.
  // nosemgrep: detect-non-literal-regexp
  const m = css.match(new RegExp(`(?:^|[\\s,}])${escaped}\\s*\\{([^}]*)\\}`))
  if (!m) throw new Error(`missing ${selector} block`)
  const out: Record<string, string> = {}
  for (const decl of m[1].replace(/\/\*[\s\S]*?\*\//g, '').split(';')) {
    const i = decl.indexOf(':')
    if (i < 0) continue
    const name = decl.slice(0, i).trim()
    if (name.startsWith('--')) out[name] = decl.slice(i + 1).trim()
  }
  return out
}

const light = tokens(':root')
const dark = tokens('.dark')

describe('design tokens', () => {
  it('uses the flat neutral values from the spec', () => {
    expect(light['--background']).toBe('#ffffff')
    expect(light['--muted-foreground']).toBe('#6f6f6f')
    expect(light['--radius']).toBe('10px')
    expect(dark['--background']).toBe('#0a0a0a')
    expect(dark['--border']).toBe('rgb(255 255 255 / .10)')
    expect(dark['--chart-1']).toBe('#1447e6')
  })

  it('drops the neumorphic layer and the HSL-channel form', () => {
    expect(Object.keys(light)).not.toContain('--nm-dist')
    expect(css).not.toMatch(/--nm-/)
    expect(css).not.toMatch(/hsl\(var\(--/)
  })

  // Every row of the spec §3 table, light then dark.
  const TABLE: [string, string, string][] = [
    ['--background', '#ffffff', '#0a0a0a'],
    ['--foreground', '#0a0a0a', '#fafafa'],
    ['--card', '#ffffff', '#171717'],
    ['--card-foreground', '#0a0a0a', '#fafafa'],
    ['--popover', '#ffffff', '#171717'],
    ['--popover-foreground', '#0a0a0a', '#fafafa'],
    ['--primary', '#171717', '#e5e5e5'],
    ['--primary-foreground', '#fafafa', '#171717'],
    ['--secondary', '#f5f5f5', '#262626'],
    ['--secondary-foreground', '#171717', '#fafafa'],
    ['--muted', '#f5f5f5', '#262626'],
    ['--muted-foreground', '#6f6f6f', '#a1a1a1'],
    ['--accent', '#f5f5f5', '#262626'],
    ['--accent-foreground', '#171717', '#fafafa'],
    // Light darkened from the mock's #e7000b (same hue) so error text passes
    // AA on its own 10% tint; dark lightened so error text passes AA on the
    // dark card, with dark text on red (--destructive-foreground below).
    ['--destructive', '#d6000a', '#ff6467'],
    ['--destructive-foreground', '#ffffff', '#0a0a0a'],
    ['--border', '#e5e5e5', 'rgb(255 255 255 / .10)'],
    ['--input', '#e5e5e5', 'rgb(255 255 255 / .15)'],
    // Light darkened from the mock's #a1a1a1 (2.58:1) so the focus ring
    // meets the 3:1 of WCAG 1.4.11 on white.
    ['--ring', '#737373', '#737373'],
    ['--outline-bg', '#ffffff', 'rgb(255 255 255 / .045)'],
    ['--chart-1', '#f54900', '#1447e6'],
    ['--chart-2', '#009689', '#00bc7d'],
    ['--chart-3', '#104e64', '#fe9a00'],
    ['--chart-4', '#ffb900', '#ad46ff'],
    ['--chart-5', '#fe9a00', '#ff2056'],
    ['--sidebar', '#fafafa', '#171717'],
    ['--sidebar-foreground', '#0a0a0a', '#fafafa'],
    ['--sidebar-accent', '#f5f5f5', '#262626'],
    ['--sidebar-accent-foreground', '#171717', '#fafafa'],
    ['--sidebar-border', '#e5e5e5', 'rgb(255 255 255 / .10)'],
  ]
  it.each(TABLE)('%s is %s light / %s dark', (name, l, d) => {
    expect(light[name]).toBe(l)
    expect(dark[name]).toBe(d)
  })

  it('defines the elevation scale in both themes', () => {
    expect(light['--shadow-xs']).toBe('0 1px 2px 0 rgb(0 0 0 / .05)')
    expect(light['--shadow-sm']).toBe('0 1px 3px 0 rgb(0 0 0 / .1), 0 1px 2px -1px rgb(0 0 0 / .1)')
    for (const s of ['--shadow-xs', '--shadow-sm', '--shadow-md', '--shadow-lg']) {
      expect(light[s]).toMatch(/rgb\(0 0 0/)
      expect(dark[s]).toMatch(/rgb\(0 0 0/)
    }
  })

  it('defines the radius scale (6 / 8 / 10 / 14 px)', () => {
    expect(light['--radius-sm']).toBe('6px')
    expect(light['--radius-md']).toBe('8px')
    expect(light['--radius-lg']).toBe('10px')
    expect(light['--radius-xl']).toBe('14px')
  })

  it('keeps the status pairs other routes use, as hex', () => {
    expect(light['--success']).toBe('#1f6b4c')
    expect(light['--success-foreground']).toBe('#ffffff')
    expect(light['--warning']).toBe('#f59e0b')
    expect(light['--warning-foreground']).toBe('#1c1917')
    expect(light['--warning-strong']).toBe('#92400e')
    expect(light['--info']).toBe('#1447e6')
    expect(light['--info-foreground']).toBe('#ffffff')
    for (const s of ['--success', '--success-foreground', '--warning', '--warning-foreground', '--warning-strong', '--info', '--info-foreground']) {
      expect(dark[s]).toMatch(/^#[0-9a-f]{6}$/)
    }
  })

  it('points the legacy --color-* aliases at the tokens', () => {
    expect(light['--color-bg']).toBe('var(--background)')
    expect(light['--color-primary']).toBe('var(--primary)')
    expect(light['--color-border']).toBe('var(--border)')
  })
})

// The mobile browser chrome is tinted from <meta name="theme-color">; it was
// the neumorphic canvas (#e3e7ee) in both themes.
describe('index.html theme-color', () => {
  const html = readFileSync(resolve(__dirname, '../../../index.html'), 'utf8')
  const metas = [...html.matchAll(/<meta\s+name="theme-color"[^>]*>/g)].map((m) => m[0])

  it('has one theme-color per colour scheme, matching --background', () => {
    expect(metas).toHaveLength(2)
    expect(metas).toContainEqual(expect.stringContaining(`content="${light['--background']}"`))
    expect(metas).toContainEqual(expect.stringContaining(`content="${dark['--background']}"`))
    const lightMeta = metas.find((m) => m.includes(`content="${light['--background']}"`)) as string
    const darkMeta = metas.find((m) => m.includes(`content="${dark['--background']}"`)) as string
    expect(lightMeta).toContain('media="(prefers-color-scheme: light)"')
    expect(darkMeta).toContain('media="(prefers-color-scheme: dark)"')
    expect(html).not.toContain('#e3e7ee')
  })
})
