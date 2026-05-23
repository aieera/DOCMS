import { api } from './client'

export interface RetentionPolicy {
  id: string
  name: string
  description?: string
  document_class_filter?: string
  tag_filter?: string[]
  workspace_filter?: string
  // Phase 5 — folder_filter scope. The backend matches the picked
  // folder AND all of its ltree descendants when this is set.
  folder_filter?: string
  retain_days: number
  then_action: 'archive' | 'dispose'
  archive_days?: number
  is_active: boolean
  created_by?: string
  created_at: string
  updated_at: string
}

export interface CreatePolicyInput {
  name: string
  description?: string
  document_class_filter?: string
  tag_filter?: string[]
  workspace_filter?: string
  folder_filter?: string
  retain_days: number
  then_action: 'archive' | 'dispose'
  archive_days?: number
  is_active?: boolean
}

export type UpdatePolicyInput = Partial<CreatePolicyInput>

// Phase 5 — preview a DRAFT policy. The backend returns the count +
// a sample of documents the policy would currently affect; the same
// SQL backs the sweep (one source of truth — see retention.go
// buildRetentionMatchSQL).
export interface RetentionPreviewSample {
  id: string
  title: string
  workspace_id: string
  folder_id: string
  document_class: string
  lifecycle_state: string
  created_at: string
}

export interface RetentionPreviewResult {
  count: number
  sample: RetentionPreviewSample[]
}

export async function listRetentionPolicies(): Promise<RetentionPolicy[]> {
  const { data } = await api.get<RetentionPolicy[]>('/admin/retention-policies')
  return data ?? []
}

export async function getRetentionPolicy(id: string): Promise<RetentionPolicy> {
  const { data } = await api.get<RetentionPolicy>(`/admin/retention-policies/${id}`)
  return data
}

export async function createRetentionPolicy(input: CreatePolicyInput): Promise<{ id: string; name: string }> {
  const { data } = await api.post<{ id: string; name: string }>('/admin/retention-policies', input)
  return data
}

export async function updateRetentionPolicy(id: string, input: UpdatePolicyInput): Promise<void> {
  await api.patch(`/admin/retention-policies/${id}`, input)
}

export async function deleteRetentionPolicy(id: string): Promise<void> {
  await api.delete(`/admin/retention-policies/${id}`)
}

export async function previewRetentionPolicy(draft: CreatePolicyInput): Promise<RetentionPreviewResult> {
  const { data } = await api.post<RetentionPreviewResult>('/admin/retention-policies/preview', draft)
  return data
}
