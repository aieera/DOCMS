import { describe, it, expect } from 'vitest'
import { sanitizeRelPath, isJunkPath, dirSegments } from '../paths'

describe('sanitizeRelPath', () => {
  it('normalizes separators and strips leading slash', () => {
    expect(sanitizeRelPath('\\a\\b\\c.txt')).toBe('a/b/c.txt')
    expect(sanitizeRelPath('/a/b.txt')).toBe('a/b.txt')
  })
  it('rejects traversal, absolute, and drive paths (zip-slip)', () => {
    expect(sanitizeRelPath('../etc/passwd')).toBeNull()
    expect(sanitizeRelPath('a/../../b')).toBeNull()
    expect(sanitizeRelPath('C:\\win\\x')).toBeNull()
  })
  it('collapses . segments and empty segments', () => {
    expect(sanitizeRelPath('a/./b//c.txt')).toBe('a/b/c.txt')
  })
  it('returns null for empty or all-junk input', () => {
    expect(sanitizeRelPath('')).toBeNull()
    expect(sanitizeRelPath('/')).toBeNull()
  })
})

describe('isJunkPath', () => {
  it('flags OS junk', () => {
    expect(isJunkPath('__MACOSX/x')).toBe(true)
    expect(isJunkPath('a/.DS_Store')).toBe(true)
    expect(isJunkPath('Thumbs.db')).toBe(true)
    expect(isJunkPath('a/real.pdf')).toBe(false)
  })
})

describe('dirSegments', () => {
  it('returns parent dirs only', () => {
    expect(dirSegments('a/b/c.txt')).toEqual(['a', 'b'])
    expect(dirSegments('root.txt')).toEqual([])
  })
})
