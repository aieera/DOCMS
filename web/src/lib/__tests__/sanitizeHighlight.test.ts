import { describe, it, expect } from 'vitest'
import { sanitizeHighlight } from '@/lib/sanitizeHighlight'

describe('sanitizeHighlight', () => {
  it('keeps the <mark> wrappers the search service emits', () => {
    expect(sanitizeHighlight('found <mark>contract</mark> here')).toBe(
      'found <mark>contract</mark> here',
    )
  })

  it('strips script tags', () => {
    expect(sanitizeHighlight('<script>alert(1)</script>hi')).toBe('alert(1)hi')
  })

  it('strips event-handler payloads in tags', () => {
    expect(sanitizeHighlight('<img src=x onerror=alert(1)>name.pdf')).toBe('name.pdf')
  })

  it('strips attributes smuggled onto mark itself', () => {
    expect(sanitizeHighlight('<mark onmouseover=alert(1)>x</mark>')).toBe('x</mark>')
  })

  it('handles unterminated tags', () => {
    expect(sanitizeHighlight('evil <img src=x')).toBe('evil ')
  })
})
