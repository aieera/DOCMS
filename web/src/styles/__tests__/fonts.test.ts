import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// Read from disk: under vitest a `globals.css?raw` import is stubbed to ''.
const css = readFileSync(resolve(__dirname, '../globals.css'), 'utf8')
const html = readFileSync(resolve(__dirname, '../../../index.html'), 'utf8')
const tailwind = readFileSync(resolve(__dirname, '../../../tailwind.config.js'), 'utf8')

/**
 * Fonts (spec 2026-10-25 §3): Geist + Geist Mono for Latin, IBM Plex Sans
 * Arabic for RTL, all self-hosted through npm packages so the app makes no
 * third-party font request (strict CSP, air-gapped installs).
 *
 * Geist comes from @fontsource/geist and @fontsource/geist-mono: the `geist`
 * package ships no CSS (only Next.js `next/font` modules) and peer-depends on
 * `next`, so it cannot be used from a plain CSS @import in this Vite app.
 */
describe('self-hosted fonts', () => {
  it('index.html does not load fonts from Google', () => {
    expect(html).not.toMatch(/fonts\.googleapis|fonts\.gstatic/)
  })

  it('globals.css has no Google Fonts reference either', () => {
    expect(css).not.toMatch(/fonts\.googleapis|fonts\.gstatic/)
  })

  it('imports the Geist and Geist Mono faces from the npm packages', () => {
    for (const weight of [400, 500, 600, 700]) {
      expect(css).toContain(`@import '@fontsource/geist/${weight}.css'`)
    }
    for (const weight of [400, 500]) {
      expect(css).toContain(`@import '@fontsource/geist-mono/${weight}.css'`)
    }
  })

  it('imports the IBM Plex Sans Arabic 400/500/600 weights', () => {
    for (const weight of [400, 500, 600]) {
      expect(css).toContain(`@import '@fontsource/ibm-plex-sans-arabic/${weight}.css'`)
    }
  })

  it('every imported font stylesheet resolves in node_modules', () => {
    const targets = [...css.matchAll(/@import '(@fontsource\/[^']+)'/g)].map((m) => m[1])
    expect(targets.length).toBe(9)
    for (const target of targets) {
      const file = resolve(__dirname, '../../../node_modules', target)
      expect(existsSync(file), `${target} is not installed`).toBe(true)
    }
  })

  it('declares the font packages as dependencies', () => {
    const pkg = JSON.parse(
      readFileSync(resolve(__dirname, '../../../package.json'), 'utf8'),
    ) as { dependencies: Record<string, string> }
    for (const name of [
      '@fontsource/geist',
      '@fontsource/geist-mono',
      '@fontsource/ibm-plex-sans-arabic',
    ]) {
      expect(pkg.dependencies[name], name).toBeTruthy()
    }
    // `geist` would drag `next` into a Vite app and has no CSS entry.
    expect(pkg.dependencies.geist).toBeUndefined()
  })

  it('places every @import before @tailwind (CSS requires imports first)', () => {
    // Line-anchored: the explanatory comment above also mentions both words.
    const imports = [...css.matchAll(/^@import /gm)].map((m) => m.index ?? -1)
    const firstTailwind = css.search(/^@tailwind /m)
    expect(imports.length).toBe(9)
    expect(firstTailwind).toBeGreaterThan(-1)
    expect(Math.max(...imports)).toBeLessThan(firstTailwind)
  })

  it('defines the font tokens', () => {
    expect(css).toMatch(/--font-sans:\s*'Geist'/)
    expect(css).toMatch(/--font-sans:[^;]*system-ui/)
    expect(css).toMatch(/--font-mono:\s*'Geist Mono'/)
    expect(css).toMatch(/--font-mono:[^;]*ui-monospace/)
  })

  it('applies IBM Plex Sans Arabic for Arabic / RTL, falling back to the sans token', () => {
    expect(css).toMatch(/:lang\(ar\)[^{]*\{[^}]*IBM Plex Sans Arabic/)
    expect(css).toMatch(/:lang\(ar\)[^{]*\{[^}]*IBM Plex Sans Arabic'?,\s*var\(--font-sans\)/)
    // body sets its own font-family, so the RTL rule must target body too.
    expect(css).toMatch(/\[dir=['"]?rtl['"]?\]\s+body/)
  })

  it('keeps code/kbd/pre/samp monospace inside Arabic / RTL content', () => {
    // `:lang(ar)` matches every descendant, so without this the Arabic face
    // (specificity 0,1,0) would beat Tailwind preflight's `code { mono }`
    // (0,0,1) and bare <code>/<kbd> ids and hashes would lose their mono face.
    expect(css).toMatch(
      /:lang\(ar\)\s+:where\(code, kbd, pre, samp\)[^{]*\{[^}]*font-family:\s*var\(--font-mono\)/,
    )
    expect(css).toMatch(
      /\[dir=['"]?rtl['"]?\]\s+:where\(code, kbd, pre, samp\)[^{]*\{[^}]*font-family:\s*var\(--font-mono\)/,
    )
  })

  it('no longer references the old families anywhere', () => {
    for (const old of [/\bInter\b/, /JetBrains/, /Fraunces/]) {
      expect(css).not.toMatch(old)
      expect(html).not.toMatch(old)
      expect(tailwind).not.toMatch(old)
    }
  })

  it('tailwind fontFamily maps sans/mono to the tokens and drops serif', () => {
    expect(tailwind).toMatch(/sans:\s*\[\s*'Geist'/)
    expect(tailwind).toMatch(/mono:\s*\[\s*'Geist Mono'/)
    expect(tailwind).not.toMatch(/\bserif\s*:/)
  })
})

describe('type scale', () => {
  /** Declarations of the first rule whose selector list contains `selector`. */
  function rule(selector: string): string {
    const escaped = selector.replace(/[.\\]/g, '\\$&')
    // Test helper: selector comes from literals in this file.
    // nosemgrep: detect-non-literal-regexp
    const m = css.match(new RegExp(`(?:^|[\\s,}])${escaped}\\s*(?:,[^{]*)?\\{([^}]*)\\}`))
    if (!m) throw new Error(`missing ${selector} rule`)
    return m[1].replace(/\/\*[\s\S]*?\*\//g, '')
  }

  it('.page-title is 24/32, 600, tight tracking, and bare h1 is not restyled', () => {
    const body = rule('.page-title')
    expect(body).toMatch(/font-size:\s*24px/)
    expect(body).toMatch(/line-height:\s*32px/)
    expect(body).toMatch(/font-weight:\s*600/)
    expect(body).toMatch(/letter-spacing:\s*-0\.025em/)
    // other routes own their headings: no bare `h1 {` rule
    expect(css).not.toMatch(/(?:^|[\s,}])h1\s*\{/)
  })

  it('.text-metric is 28/32, 600, tabular numerals', () => {
    const body = rule('.text-metric')
    expect(body).toMatch(/font-size:\s*28px/)
    expect(body).toMatch(/line-height:\s*32px/)
    expect(body).toMatch(/font-weight:\s*600/)
    expect(body).toMatch(/font-variant-numeric:\s*tabular-nums/)
  })

  it('.text-caption is 12/16', () => {
    const body = rule('.text-caption')
    expect(body).toMatch(/font-size:\s*12px/)
    expect(body).toMatch(/line-height:\s*16px/)
  })
})
