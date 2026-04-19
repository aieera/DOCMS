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
