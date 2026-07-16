// Clause library — ADR 0104 §18 F11.
import { api } from './client'

export interface Clause {
  id: string
  name: string
  body_text: string
  jurisdiction: string
  tags: string[]
  version: number
  approved_by?: string | null
  approved_at?: string | null
  created_by: string
  created_at: string
  updated_at: string
  search_rank?: number | null
}

export interface ListClausesParams {
  q?: string
  jurisdiction?: string
  tag?: string
  limit?: number
}

export async function listClauses(params: ListClausesParams = {}) {
  const qs = new URLSearchParams()
  if (params.q)            qs.set('q', params.q)
  if (params.jurisdiction) qs.set('jurisdiction', params.jurisdiction)
  if (params.tag)          qs.set('tag', params.tag)
  if (params.limit)        qs.set('limit', String(params.limit))
  const query = qs.toString()
  const { data } = await api.get<{ clauses: Clause[]; total: number }>(
    `/clauses${query ? `?${query}` : ''}`,
  )
  return data
}

export async function getClause(id: string) {
  const { data } = await api.get<Clause>(`/clauses/${id}`)
  return data
}

export interface CreateClauseInput {
  name: string
  body_text: string
  jurisdiction?: string
  tags?: string[]
}

export async function createClause(input: CreateClauseInput) {
  const { data } = await api.post<{ id: string }>('/clauses', input)
  return data
}

// Approval state changes ONLY via approveClause/revokeClauseApproval
// (admin/owner-gated dedicated endpoints) — PATCH deliberately has no
// "approved" field.
export interface PatchClauseInput {
  name?: string
  body_text?: string
  jurisdiction?: string
  tags?: string[]
}

export async function patchClause(id: string, input: PatchClauseInput) {
  const { data } = await api.patch<Clause>(`/clauses/${id}`, input)
  return data
}

export async function deleteClause(id: string) {
  await api.delete(`/clauses/${id}`)
}

// ── ADR 0104 Phases 2/4 + approval ──────────────────────────────────

export interface ClauseMatch {
  clause_id: string
  clause_name: string
  jurisdiction: string
  approved: boolean
  similarity: number
  chunk_index: number
  matched_text: string
  detected_at: string
}

export interface ClauseVariation {
  normalized_hash: string
  occurrences: number
  sample_text: string
  min_similarity: number
  max_similarity: number
  document_ids: string[]
}

export interface ClauseVariations {
  variations: ClauseVariation[]
  total_documents: number
}

export async function getDocumentClauseMatches(documentId: string): Promise<ClauseMatch[]> {
  const { data } = await api.get<{ matches: ClauseMatch[] }>(
    `/documents/${documentId}/clause-matches`,
  )
  return data?.matches ?? []
}

export async function getClauseVariations(clauseId: string): Promise<ClauseVariations> {
  const { data } = await api.get<ClauseVariations>(`/clauses/${clauseId}/variations`)
  return data
}

export async function approveClause(clauseId: string): Promise<void> {
  await api.post(`/clauses/${clauseId}/approve`)
}

export async function revokeClauseApproval(clauseId: string): Promise<void> {
  await api.delete(`/clauses/${clauseId}/approve`)
}
