// Tiny localStorage-backed list of the user's recent search queries,
// surfaced in the topbar search dropdown (most-recent first, deduped,
// capped). Purely a UX nicety — server state lives in saved searches.

const KEY = 'sedoc-recent-searches'
const MAX = 8

export function getRecentSearches(): string[] {
  try {
    const raw = localStorage.getItem(KEY)
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

export function recordRecentSearch(query: string): void {
  const q = query.trim()
  if (!q) return
  const next = [q, ...getRecentSearches().filter((x) => x !== q)].slice(0, MAX)
  try {
    localStorage.setItem(KEY, JSON.stringify(next))
  } catch {
    // storage full/blocked — recents are best-effort
  }
}

export function removeRecentSearch(query: string): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(getRecentSearches().filter((x) => x !== query)))
  } catch {
    // best-effort
  }
}
