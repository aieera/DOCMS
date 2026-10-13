import { describe, it, expect } from 'vitest'
import { lifecycleVariant } from '../lifecycle'

describe('lifecycleVariant', () => {
  // The API sends the raw proto enum. Passing that straight to <Badge
  // variant={…}> matches no variant key, so the badge silently falls back
  // to its default grey and the status colour is lost.
  it('normalises the raw proto enum the API actually sends', () => {
    expect(lifecycleVariant('LIFECYCLE_STATE_DRAFT')).toBe('draft')
    expect(lifecycleVariant('LIFECYCLE_STATE_IN_REVIEW')).toBe('in_review')
    expect(lifecycleVariant('LIFECYCLE_STATE_ACTIVE')).toBe('active')
  })

  it('accepts the short form too, which REST endpoints pre-strip', () => {
    expect(lifecycleVariant('draft')).toBe('draft')
    expect(lifecycleVariant('active')).toBe('active')
  })

  it('is case-insensitive', () => {
    expect(lifecycleVariant('Draft')).toBe('draft')
  })

  it('falls back to a neutral variant for unknown or missing states', () => {
    expect(lifecycleVariant(undefined)).toBe('secondary')
    expect(lifecycleVariant('')).toBe('secondary')
    expect(lifecycleVariant('LIFECYCLE_STATE_UNSPECIFIED')).toBe('secondary')
    expect(lifecycleVariant('something_new')).toBe('secondary')
  })
})
