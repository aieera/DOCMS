import { api } from './client'

// Pre-commit ingestion pipeline (Workstream 3) + native review queue
// (Workstream 4). These are hand-wired document-service routes (see
// INTEGRATION.md); the session cookie + tenant headers the shared `api` client
// already sends satisfy their SessionOrAPIKey + TenantHTTP chain.

export type ReviewReason = 'below_threshold' | 'ambiguous_match' | 'no_external_key' | 'low_ocr_confidence'
export type ReviewStatus = 'pending' | 'resolved' | 'rejected'

export interface ReviewItem {
  id: string
  ingestion_item_id: string
  workspace_id: string
  target_customer_ref: string
  document_class: string
  extracted_external_key: string
  suggested_match_document_id?: string
  confidence: number
  reason: ReviewReason
  status: ReviewStatus
  ocr_text?: string
  created_at: string
}

export interface ReviewList {
  items: ReviewItem[]
  next_cursor?: string
}

export interface IngestionItem {
  id: string
  workspace_id: string
  target_customer_ref: string
  status: string
  extracted_external_key: string
  match_document_id?: string
  confidence: number
  document_class: string
  created_at: string
}

export type ResolveDecision = 'new_version' | 'new_document' | 'reject'

export interface ResolveBody {
  decision: ResolveDecision
  target_document_id?: string
  external_key?: string
  notes?: string
}

export interface ResolveResult {
  status: string
  document_id?: string
  version_id?: string
}

export async function listReviewQueue(params: { status?: ReviewStatus; cursor?: string; limit?: number } = {}) {
  const { data } = await api.get<ReviewList>('/review-queue', {
    params: Object.fromEntries(Object.entries(params).filter(([, v]) => v !== undefined && v !== '')),
  })
  return data
}

export async function getReviewItem(id: string) {
  const { data } = await api.get<ReviewItem>(`/review-queue/${id}`)
  return data
}

export async function resolveReviewItem(id: string, body: ResolveBody) {
  const { data } = await api.post<ResolveResult>(`/review-queue/${id}/resolve`, body)
  return data
}

export async function listIngestionItems(params: { status?: string; limit?: number } = {}) {
  const { data } = await api.get<{ items: IngestionItem[] }>('/ingest/items', {
    params: Object.fromEntries(Object.entries(params).filter(([, v]) => v !== undefined && v !== '')),
  })
  return data
}
