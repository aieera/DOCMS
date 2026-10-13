import { describe, it, expect } from 'vitest'
import { formatFileSize, formatUsd, notificationTypeLabel } from '@/lib/formatters'

describe('formatFileSize', () => {
  it('formats numeric byte counts', () => {
    expect(formatFileSize(0)).toBe('0\u00A0B')
    expect(formatFileSize(15907)).toBe('\u206615.5\u00A0KB\u2069')
    expect(formatFileSize(5_500_000)).toBe('\u20665.2\u00A0MB\u2069')
  })

  it('coerces numeric strings (API serialises int64 as string)', () => {
    expect(formatFileSize('15907')).toBe('\u206615.5\u00A0KB\u2069')
    expect(formatFileSize('0')).toBe('0\u00A0B')
  })

  it('em-dashes null/undefined/garbage', () => {
    expect(formatFileSize(null)).toBe('—')
    expect(formatFileSize(undefined)).toBe('—')
    expect(formatFileSize('not-a-number' as unknown as string)).toBe('—')
    expect(formatFileSize(-5)).toBe('—')
  })
})

describe('notificationTypeLabel', () => {
  it('never renders a raw dms.{domain}.{action} event code', () => {
    expect(notificationTypeLabel('document.uploaded')).toBe('Document uploaded')
    expect(notificationTypeLabel('document.shared')).toBe('Document shared')
    expect(notificationTypeLabel('signature.requested')).toBe('Signature requested')
  })

  it('uses the explicit label where the derived one reads badly', () => {
    expect(notificationTypeLabel('document.version_uploaded')).toBe('New version uploaded')
    expect(notificationTypeLabel('comment.mention')).toBe('Mentioned in a comment')
  })

  it('collapses digest rows to one label', () => {
    expect(notificationTypeLabel('digest.document')).toBe('Digest')
  })

  it('still humanises an unknown domain rather than leaking the code', () => {
    expect(notificationTypeLabel('quota.exceeded')).toBe('Quota exceeded')
    expect(notificationTypeLabel(undefined)).toBe('Notification')
  })
})

describe('formatUsd', () => {
  it('scales precision to magnitude instead of padding zeros', () => {
    // The bug: sub-cent spend rendered as "$0.00230".
    expect(formatUsd(0.0023)).toBe('$0.0023')
    expect(formatUsd(0.002)).toBe('$0.002')
    expect(formatUsd(0.0234)).toBe('$0.023')
    expect(formatUsd(12.5)).toBe('$12.50')
  })

  it('does not claim a spend of exactly zero for dust', () => {
    expect(formatUsd(0.00001)).toBe('< $0.0001')
    expect(formatUsd(0)).toBe('—')
    expect(formatUsd(null)).toBe('—')
  })
})
