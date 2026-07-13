import { describe, it, expect } from 'vitest'
import { dimensionLabel } from '@/lib/reportFormat'

// Guards the "blank bar" report bug: a document with no document_class
// grouped into its own bucket must get a visible label, not ''.
describe('dimensionLabel', () => {
  it('labels null / undefined / empty as (uncategorized)', () => {
    expect(dimensionLabel(null)).toBe('(uncategorized)')
    expect(dimensionLabel(undefined)).toBe('(uncategorized)')
    expect(dimensionLabel('')).toBe('(uncategorized)')
    expect(dimensionLabel('   ')).toBe('(uncategorized)')
  })

  it('passes real values through unchanged', () => {
    expect(dimensionLabel('invoice')).toBe('invoice')
    expect(dimensionLabel('contract')).toBe('contract')
    expect(dimensionLabel(0)).toBe('0')
    expect(dimensionLabel(42)).toBe('42')
    expect(dimensionLabel(false)).toBe('false')
  })
})
