import { api } from './client'
import type { SearchResult } from '@/types/api'

export async function search(body: Record<string, unknown>) {
  const { data } = await api.post<SearchResult>('/search', body)
  return data
}

export async function autocomplete(q: string, limit = 10) {
  const { data } = await api.get<{ suggestions: { text: string; source: string }[] }>(
    '/search/autocomplete', { params: { q, limit } },
  )
  return data.suggestions
}

// ADR 0084 — grouped autocomplete. Used by the global CommandPalette.
// Each group is independently capped at the request's `limit`; the
// backend dedupes documents by title before returning.
export interface SuggestResult {
  documents: { text: string; document_id: string; score: number }[]
  tags:      { text: string; count: number }[]
  people:    { text: string; count: number }[]
  recent:    { text: string }[]
}

export async function suggest(q: string, limit = 10): Promise<SuggestResult> {
  const { data } = await api.get<SuggestResult>('/search/suggest', {
    params: { q, limit },
  })
  return data
}

