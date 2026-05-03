import { api } from './client'
import type { AIAnswer } from '@/types/api'

export async function askQuestion(question: string, scope?: string, scopeId?: string) {
  const { data } = await api.post<AIAnswer>('/intelligence/ask', { question, scope, scope_id: scopeId })
  return data
}

export async function summarizeDocument(documentId: string, text: string, length = 'medium') {
  const { data } = await api.post('/intelligence/summarize', { document_id: documentId, text, length })
  return data
}

export async function detectRedactions(documentId: string, versionId: string, text: string) {
  const { data } = await api.post('/intelligence/redact/detect', { document_id: documentId, version_id: versionId, text })
  return data
}

// ----- Auto-tagging (ADR 0052) ------------------------------------------------

export type TagSource = 'ner' | 'classification' | 'llm' | 'pattern'
export type SuggestionStatus = 'pending' | 'accepted' | 'rejected' | 'auto_applied'

export interface TagSuggestion {
  id: string
  document_id: string
  version_id: string
  tag_name: string
  source: TagSource
  source_detail: Record<string, unknown>
  confidence: number
  status: SuggestionStatus
  reviewed_by?: string
  reviewed_at?: string
  created_at: string
}

export interface AutoTagConfig {
  enabled: boolean
  auto_apply_threshold: number
  suggest_threshold: number
  max_tags_per_document: number
  blocked_tags: string[]
  source_weights: Record<string, number>
}

export interface ReviewAction {
  suggestion_id: string
  action: 'accept' | 'reject'
}

export async function listTagSuggestions(documentId: string) {
  const { data } = await api.get<{ suggestions: TagSuggestion[]; config?: AutoTagConfig }>(
    `/documents/${documentId}/tag-suggestions`,
  )
  return data
}

export async function reviewTagSuggestions(documentId: string, actions: ReviewAction[]) {
  const { data } = await api.post<{
    accepted_count: number
    rejected_count: number
    accepted_tags: string[]
    rejected_tags: string[]
  }>(`/documents/${documentId}/tag-suggestions/review`, { actions })
  return data
}

export async function listPendingTagSuggestions(params: {
  min_confidence?: number
  max_confidence?: number
  limit?: number
  offset?: number
} = {}) {
  const { data } = await api.get<{
    suggestions: TagSuggestion[]
    total: number
    limit: number
    offset: number
  }>('/admin/tag-suggestions', {
    params: Object.fromEntries(
      Object.entries(params).filter(([, v]) => v !== undefined),
    ),
  })
  return data
}

export async function getAutoTagConfig() {
  const { data } = await api.get<AutoTagConfig>('/admin/auto-tag-config')
  return data
}

export async function updateAutoTagConfig(patch: Partial<AutoTagConfig>) {
  const { data } = await api.put<AutoTagConfig>('/admin/auto-tag-config', patch)
  return data
}
