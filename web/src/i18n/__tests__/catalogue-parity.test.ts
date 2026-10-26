import { describe, it, expect } from 'vitest'

import enCommon from '../../../public/locales/en/common.json'
import arCommon from '../../../public/locales/ar/common.json'

// The en and ar `common` catalogues must name the same strings. Plural keys
// differ by design (en ships `_one`/`_other`, ar the six CLDR forms), so they
// are compared by base key with the plural suffix stripped. A key added to
// one catalogue only renders the English fallback (or the raw key) to the
// other language's readers. `npm run i18n:check` points at a script that does
// not exist, so this test is the catalogue check.
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/

function leafKeys(node: unknown, prefix = ''): string[] {
  if (node === null || typeof node !== 'object' || Array.isArray(node)) return [prefix]
  return Object.entries(node as Record<string, unknown>)
    .flatMap(([k, v]) => leafKeys(v, prefix ? `${prefix}.${k}` : k))
}
const baseKeys = (catalogue: unknown) => new Set(leafKeys(catalogue).map((k) => k.replace(PLURAL_SUFFIX, '')))

describe('en/ar common catalogue parity', () => {
  const en = baseKeys(enCommon)
  const ar = baseKeys(arCommon)

  it('has every en key in ar (plural suffixes stripped)', () => {
    expect([...en].filter((k) => !ar.has(k))).toEqual([])
  })

  it('has every ar key in en (plural suffixes stripped)', () => {
    expect([...ar].filter((k) => !en.has(k))).toEqual([])
  })

  it('is not vacuous', () => {
    expect(en.size).toBeGreaterThan(1000)
    expect(baseKeys({ a: { b_one: '', b_other: '' }, c: '' })).toEqual(new Set(['a.b', 'c']))
  })
})
