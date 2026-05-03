import { api } from './client'

export type RouteMatchSource = 'rule' | 'history' | 'similarity'
export type RouteSuggestionStatus = 'pending' | 'accepted' | 'dismissed'

export interface RouteSuggestion {
  id: string
  document_id: string
  suggested_folder_id: string
  suggested_workspace_id?: string
  folder_path: string
  match_source: RouteMatchSource
  match_detail: Record<string, unknown>
  confidence: number
  status: RouteSuggestionStatus
  created_at: string
}

export interface RoutingRule {
  id: string
  name: string
  description: string
  category_key: string
  target_folder_id: string
  target_workspace_id?: string
  priority: number
  enabled: boolean
  created_by: string
  created_at: string
  updated_at: string
}

export interface SmartRoutingConfig {
  enabled: boolean
  auto_move_threshold: number
  suggest_threshold: number
  max_suggestions: number
  learn_from_history: boolean
  use_similarity: boolean
}

export interface FilingAnalytics {
  total_filings: number
  top_categories: { category_key: string; count: number }[]
  top_folder_by_category: { category_key: string; folder_id: string; count: number }[]
  suggestion_acceptance: {
    total_suggested: number
    total_accepted: number
    total_dismissed: number
    acceptance_rate: number
  }
}

export async function listRouteSuggestions(documentId: string) {
  const { data } = await api.get<{ suggestions: RouteSuggestion[] }>(
    `/documents/${documentId}/route-suggestions`,
  )
  return data
}

export async function acceptRouteSuggestion(documentId: string, suggestionId: string) {
  const { data } = await api.post<{
    suggestion: RouteSuggestion
    document_id: string
    folder_id: string
    workspace_id: string
  }>(`/documents/${documentId}/route-suggestions/${suggestionId}/accept`)
  return data
}

export async function dismissRouteSuggestion(documentId: string, suggestionId: string) {
  await api.post(`/documents/${documentId}/route-suggestions/${suggestionId}/dismiss`)
}

export async function listRoutingRules() {
  const { data } = await api.get<{ rules: RoutingRule[] }>('/admin/routing-rules')
  return data.rules
}

export async function createRoutingRule(input: {
  name: string
  description?: string
  category_key: string
  target_folder_id: string
  target_workspace_id?: string
  priority?: number
  enabled?: boolean
}) {
  const { data } = await api.post<RoutingRule>('/admin/routing-rules', input)
  return data
}

export async function updateRoutingRule(
  id: string,
  patch: { name?: string; description?: string; priority?: number; enabled?: boolean },
) {
  const { data } = await api.put<RoutingRule>(`/admin/routing-rules/${id}`, patch)
  return data
}

export async function deleteRoutingRule(id: string) {
  await api.delete(`/admin/routing-rules/${id}`)
}

export async function getSmartRoutingConfig() {
  const { data } = await api.get<SmartRoutingConfig>('/admin/smart-routing-config')
  return data
}

export async function updateSmartRoutingConfig(patch: Partial<SmartRoutingConfig>) {
  const { data } = await api.put<SmartRoutingConfig>('/admin/smart-routing-config', patch)
  return data
}

export async function getFilingAnalytics() {
  const { data } = await api.get<FilingAnalytics>('/admin/filing-analytics')
  return data
}
