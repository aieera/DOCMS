import { describe, it, expect } from 'vitest'
import { endOfLocalDayISO, isoToLocalDateInput } from '@/lib/grantExpiry'

// L-12: a grant "expiring 2026-10-12" was sent as 2026-10-12T00:00:00Z —
// UTC midnight, i.e. the START of the chosen day (and the previous
// evening west of UTC). Expiry now means the end of the chosen local day.
describe('grant expiry (L-12)', () => {
  it('maps a picked date to the end of that local day', () => {
    const iso = endOfLocalDayISO('2026-10-12')
    expect(iso).toBe(new Date(2026, 9, 12, 23, 59, 59, 999).toISOString())
    const d = new Date(iso)
    expect([d.getFullYear(), d.getMonth(), d.getDate()]).toEqual([2026, 9, 12])
  })

  it('round-trips back to the same date input value', () => {
    expect(isoToLocalDateInput(endOfLocalDayISO('2026-03-29'))).toBe('2026-03-29')
    expect(isoToLocalDateInput('')).toBe('')
  })

  it('returns empty for a cleared or malformed date', () => {
    expect(endOfLocalDayISO('')).toBe('')
    expect(endOfLocalDayISO('not-a-date')).toBe('')
  })
})
