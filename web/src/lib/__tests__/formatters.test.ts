import { describe, it, expect } from 'vitest'
import { formatFileSize } from '@/lib/formatters'

describe('formatFileSize', () => {
  it('formats numeric byte counts', () => {
    expect(formatFileSize(0)).toBe('0 B')
    expect(formatFileSize(15907)).toBe('15.5 KB')
    expect(formatFileSize(5_500_000)).toBe('5.2 MB')
  })

  it('coerces numeric strings (API serialises int64 as string)', () => {
    expect(formatFileSize('15907')).toBe('15.5 KB')
    expect(formatFileSize('0')).toBe('0 B')
  })

  it('em-dashes null/undefined/garbage', () => {
    expect(formatFileSize(null)).toBe('—')
    expect(formatFileSize(undefined)).toBe('—')
    expect(formatFileSize('not-a-number' as unknown as string)).toBe('—')
    expect(formatFileSize(-5)).toBe('—')
  })
})
