import { describe, it, expect, beforeEach } from 'vitest'
import { getRecentSearches, recordRecentSearch, removeRecentSearch } from '@/lib/recentSearches'

beforeEach(() => localStorage.clear())

describe('recentSearches', () => {
  it('records queries most-recent first, deduplicated', () => {
    recordRecentSearch('alpha')
    recordRecentSearch('beta')
    recordRecentSearch('alpha')
    expect(getRecentSearches()).toEqual(['alpha', 'beta'])
  })

  it('ignores empty/whitespace queries and trims', () => {
    recordRecentSearch('  ')
    recordRecentSearch(' gamma ')
    expect(getRecentSearches()).toEqual(['gamma'])
  })

  it('caps the list at 8', () => {
    for (let i = 0; i < 12; i++) recordRecentSearch(`q${i}`)
    const list = getRecentSearches()
    expect(list).toHaveLength(8)
    expect(list[0]).toBe('q11')
  })

  it('removes a single entry', () => {
    recordRecentSearch('alpha')
    recordRecentSearch('beta')
    removeRecentSearch('alpha')
    expect(getRecentSearches()).toEqual(['beta'])
  })

  it('survives corrupted storage', () => {
    localStorage.setItem('sedoc-recent-searches', '{not json')
    expect(getRecentSearches()).toEqual([])
  })
})
