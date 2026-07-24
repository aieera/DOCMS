import { describe, it, expect } from 'vitest'
import { dimensionLabel, formatCell } from '@/lib/reportFormat'

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

// Table-cell rendering: dimension cells go through dimensionLabel (blank
// buckets stay visible), numeric measure cells get thousands separators.
describe('formatCell', () => {
  it('labels empty dimension cells as (uncategorized)', () => {
    expect(formatCell('', true)).toBe('(uncategorized)')
    expect(formatCell(null, true)).toBe('(uncategorized)')
    expect(formatCell('invoice', true)).toBe('invoice')
  })

  it('formats numeric measure cells with separators', () => {
    expect(formatCell(1421234, false)).toBe('1,421,234')
    expect(formatCell(0, false)).toBe('0')
  })

  it('passes non-numeric measure cells through', () => {
    expect(formatCell('n/a', false)).toBe('n/a')
    expect(formatCell(null, false)).toBe('')
  })
})
