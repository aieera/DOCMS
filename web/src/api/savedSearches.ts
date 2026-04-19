import { api } from './client'

export interface SavedSearch {
  id: string
  name: string
  query: string
  filters?: Record<string, unknown>
  notify?: boolean
  notify_interval_minutes?: number
  created_at: string
  last_run_at?: string | null
}

export async function listSavedSearches() {
  const { data } = await api.get<SavedSearch[] | { saved_searches: SavedSearch[] }>('/saved-searches')
  return Array.isArray(data) ? data : (data.saved_searches ?? [])
}

export async function createSavedSearch(input: {
  name: string
  query: string
  filters?: Record<string, unknown>
  notify?: boolean
  notify_interval_minutes?: number
}) {
  const { data } = await api.post<SavedSearch>('/saved-searches', input)
  return data
}

export async function deleteSavedSearch(id: string) {
  await api.delete(`/saved-searches/${id}`)
}
