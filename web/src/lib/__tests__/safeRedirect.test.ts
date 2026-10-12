import { describe, it, expect } from 'vitest'
import { safeRedirectPath } from '@/lib/safeRedirect'

// L-12: login must honour the ?redirect= the IRM viewer / auth guard
// sends, but only to a same-origin relative path — never an open
// redirect to another site.
describe('safeRedirectPath (L-12)', () => {
  it('keeps same-origin relative paths with query and hash', () => {
    expect(safeRedirectPath('/protected/c-1?lt=abc')).toBe('/protected/c-1?lt=abc')
    expect(safeRedirectPath('/sign/r-1/s-1?token=deadbeef#x')).toBe('/sign/r-1/s-1?token=deadbeef#x')
  })

  it('rejects absolute, protocol-relative and scheme URLs', () => {
    for (const bad of [
      'https://evil.example/', '//evil.example/x', '/\\evil.example', '\\\\evil.example',
      'javascript:alert(1)', 'http:/evil', ' /x', '/x\n', 'relative/path', '',
    ]) {
      expect(safeRedirectPath(bad), bad).toBeUndefined()
    }
  })

  it('rejects non-strings, oversize values and login loops', () => {
    expect(safeRedirectPath(42)).toBeUndefined()
    expect(safeRedirectPath(undefined)).toBeUndefined()
    expect(safeRedirectPath('/' + 'a'.repeat(5000))).toBeUndefined()
    expect(safeRedirectPath('/login?redirect=/x')).toBeUndefined()
  })
})
