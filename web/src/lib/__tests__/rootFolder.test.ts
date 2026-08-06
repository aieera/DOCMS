import { describe, it, expect } from 'vitest'
import { findRootFolder } from '@/lib/rootFolder'

const f = (id: string, name: string, extra: object = {}) => ({ id, name, ...extra })

describe('findRootFolder', () => {
  it('prefers the auto-created "Root" folder', () => {
    const out = findRootFolder([f('a', 'Quotes'), f('b', 'Root'), f('c', 'Shared Documents')])
    expect(out?.id).toBe('b')
  })

  it('falls back to the seeded "Shared Documents"', () => {
    const out = findRootFolder([f('a', 'Quotes'), f('c', 'Shared Documents')])
    expect(out?.id).toBe('c')
  })

  it('falls back to the oldest parentless folder — never an arbitrary sibling', () => {
    const out = findRootFolder([
      f('a', 'Quotes', { created_at: '2026-08-02T00:00:00Z' }),
      f('b', 'Archive', { created_at: '2026-08-01T00:00:00Z' }),
    ])
    expect(out?.id).toBe('b')
  })

  it('ignores folders that have a parent', () => {
    const out = findRootFolder([
      f('child', 'Root', { parent_folder_id: 'x' }),
      f('top', 'Quotes'),
    ])
    expect(out?.id).toBe('top')
  })

  it('returns undefined for an empty list', () => {
    expect(findRootFolder([])).toBeUndefined()
  })
})
