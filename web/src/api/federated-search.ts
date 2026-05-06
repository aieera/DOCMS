import { api } from './client'

// ADR 0069 — platform-admin federated (cross-tenant) search.
//
// Used ONLY by the support-search admin page. Customer-facing
// pages MUST NOT import this module.

export interface FederatedHit {
  document_id: string
  title: string
  workspace_id: string
  size_bytes: number
  mime_type: string
  created_at: string
  lifecycle_state: string
  score: number
}

export interface FederatedSearchResult {
  results_by_tenant: Record<string, FederatedHit[]>
  total_hits: number
  tenants_with_hits: number
  audit_id: string
  latency_ms: number
}

export interface FederatedSearchInput {
  query: string
  reason: string
  max_per_tenant?: number
  page_size?: number
  // Optional filter knobs — same shape as the regular /search body.
  filters?: {
    tags?: string[]
    document_class?: string[]
    lifecycle_state?: string[]
    mime_type?: string[]
    workspace_id?: string
    created_by?: string
  }
}

export async function runFederatedSearch(input: FederatedSearchInput) {
  const { data } = await api.post<FederatedSearchResult>(
    '/platform/search/federated',
    input,
  )
  return data
}

// Audit-row read-back for the admin's own recent queries.
export interface FederatedAuditRecord {
  id: string
  reason: string
  query_payload: { query: string; filters?: Record<string, unknown> }
  results_summary: { total_hits?: number; tenants_with_hits?: number }
  latency_ms: number
  outcome: 'success' | 'denied_perm' | 'denied_quota' | 'error'
  error_kind?: string
  created_at: string
}

export async function listMyFederatedAudit(limit = 20) {
  const { data } = await api.get<FederatedAuditRecord[]>(
    '/platform/search/federated/audit',
    { params: { limit } },
  )
  return data
}
