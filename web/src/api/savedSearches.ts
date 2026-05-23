import { api } from './client'

export interface SavedSearchSubscriber {
  user_id: string
  channels: string[]
  subscribed_by: string
  subscribed_at: string
}

export type TreeVisibility = 'private' | 'workspace' | 'public'

export interface SavedSearch {
  id: string
  name: string
  query: string
  filters?: Record<string, unknown>
  notify?: boolean
  notify_interval_minutes?: number
  alert_frequency_cron?: string
  workflow_id?: string
  subscribers?: SavedSearchSubscriber[]
  subscriber_count?: number
  created_at: string
  last_run_at?: string | null
  // ADR 0100 — smart folder fields. Defaults: is_smart_folder=false,
  // tree_visibility='private'. Set via promoteSmartFolder().
  is_smart_folder?: boolean
  tree_visibility?: TreeVisibility
  workspace_id?: string | null
  icon?: string
  smart_folder_at?: string | null
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
  workspace_id?: string
}) {
  const { data } = await api.post<SavedSearch>('/saved-searches', input)
  return data
}

export async function deleteSavedSearch(id: string) {
  await api.delete(`/saved-searches/${id}`)
}

// ADR 0085 — PATCH any subset of fields. Pointer-style on the
// backend so an unset field stays unchanged. Used to convert a
// saved search into an alert (notify: true), edit the query, or
// change frequency.
export type SavedSearchPatch = Partial<{
  name: string
  query: string
  filters: Record<string, unknown>
  notify: boolean
  notify_interval_minutes: number
  alert_frequency_cron: string
}>

export async function updateSavedSearch(id: string, patch: SavedSearchPatch) {
  const { data } = await api.patch<SavedSearch>(`/saved-searches/${id}`, patch)
  return data
}

export async function subscribeSavedSearch(
  id: string,
  body: { user_id?: string; channels: string[] },
) {
  await api.post(`/saved-searches/${id}/subscribe`, body)
}

export async function unsubscribeSavedSearch(id: string, userId: string) {
  await api.delete(`/saved-searches/${id}/subscribe/${userId}`)
}

// ADR 0100 — smart folders. listSmartFolders returns ALL smart
// folders the caller can see (own private + workspace + public),
// not just their own.
export async function listSmartFolders() {
  const { data } = await api.get<SavedSearch[]>('/saved-searches/smart-folders')
  return Array.isArray(data) ? data : []
}

export async function promoteSmartFolder(
  id: string,
  input: {
    tree_visibility: TreeVisibility
    workspace_id?: string | null
    icon?: string
  },
) {
  const { data } = await api.post<SavedSearch>(`/saved-searches/${id}/promote`, input)
  return data
}

export async function demoteSmartFolder(id: string) {
  await api.post(`/saved-searches/${id}/demote`)
}
