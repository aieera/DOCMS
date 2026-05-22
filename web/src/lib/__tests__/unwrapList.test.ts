import { describe, expect, it } from 'vitest'
import { unwrapList, UnknownListShapeError } from '@/lib/unwrapList'

interface Item { id: string }

describe('unwrapList — Wave 5 pattern 3', () => {
  it('returns the array unchanged for a bare-array response', () => {
    const items: Item[] = [{ id: 'a' }, { id: 'b' }]
    expect(unwrapList<Item>(items, 'items')).toEqual(items)
  })

  it('returns the wrapped array for { key: [...] } envelopes', () => {
    const items: Item[] = [{ id: 'a' }]
    expect(unwrapList<Item>({ items }, 'items')).toEqual(items)
  })

  it('returns an empty array when the wrapped key is an empty array', () => {
    // Distinct from "key missing" — an empty array is a healthy
    // zero-item response and must NOT throw.
    expect(unwrapList<Item>({ items: [] }, 'items')).toEqual([])
    expect(unwrapList<Item>([], 'items')).toEqual([])
  })

  it('throws UnknownListShapeError on null', () => {
    expect(() => unwrapList<Item>(null, 'items')).toThrow(UnknownListShapeError)
  })

  it('throws UnknownListShapeError on undefined', () => {
    expect(() => unwrapList<Item>(undefined, 'items')).toThrow(UnknownListShapeError)
  })

  it('throws UnknownListShapeError on a primitive (string)', () => {
    expect(() => unwrapList<Item>('not a list', 'items')).toThrow(UnknownListShapeError)
  })

  it('throws UnknownListShapeError on a number', () => {
    expect(() => unwrapList<Item>(42, 'items')).toThrow(UnknownListShapeError)
  })

  it('throws when the key is missing from an object', () => {
    // The audit's failure mode: backend returns { error: '...' } and
    // the old code silently returned []. Now it raises so react-query
    // routes the failure to isError instead of pretending zero items.
    expect(() => unwrapList<Item>({ error: 'database down' }, 'items')).toThrow(UnknownListShapeError)
  })

  it('throws when the key is present but not an array', () => {
    // Defends against a partial backend regression where the wrapper
    // key is reserved but the value is malformed.
    expect(() => unwrapList<Item>({ items: null }, 'items')).toThrow(UnknownListShapeError)
    expect(() => unwrapList<Item>({ items: 'oops' }, 'items')).toThrow(UnknownListShapeError)
    expect(() => unwrapList<Item>({ items: {} }, 'items')).toThrow(UnknownListShapeError)
  })

  it('uses a different key when caller asks for one', () => {
    expect(unwrapList<Item>({ versions: [{ id: '1' }] }, 'versions')).toEqual([{ id: '1' }])
    expect(() => unwrapList<Item>({ versions: [{ id: '1' }] }, 'items')).toThrow(UnknownListShapeError)
  })

  it('error message names the expected key and describes the shape received', () => {
    // Maintainers reading the toast or console need to see what
    // arrived instead of a bare "[object Object]". Pin the message
    // contract so logging downstream stays useful.
    try {
      unwrapList<Item>({ error: 'auth failed', code: 'UNAUTH' }, 'items')
      throw new Error('should have thrown')
    } catch (e) {
      expect(e).toBeInstanceOf(UnknownListShapeError)
      const msg = (e as Error).message
      expect(msg).toContain("'items'")
      expect(msg).toContain('error')
      expect(msg).toContain('code')
    }
  })

  it('error preserves the key and received payload on the instance', () => {
    // Lets a higher-level error boundary or logger pull the raw
    // payload back out for telemetry without re-parsing the message.
    try {
      unwrapList<Item>({ wrong: [] }, 'items')
    } catch (e) {
      const err = e as UnknownListShapeError
      expect(err.key).toBe('items')
      expect(err.received).toEqual({ wrong: [] })
    }
  })
})
