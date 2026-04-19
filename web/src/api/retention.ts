import { api } from './client'

export interface RetentionPolicy {
  id: string
  name: string
  description?: string
  document_class_filter?: string
  tag_filter?: string[]
  workspace_filter?: string
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
  retain_days: number
  then_action: 'archive' | 'dispose'
  archive_days?: number
  is_active?: boolean
}

export type UpdatePolicyInput = Partial<CreatePolicyInput>

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
