import { api } from './client'

export interface ResidencyRow {
  region: string
  doc_count: number
  blob_bytes: number
  // Populated by the document service's residency-stats query (Wave 16):
  // documents whose current content_blob storage_region differs from the
  // doc's region_pin. Surfaces the data_residency_compliance SLI in the
  // /admin/compliance widget without a separate endpoint.
  off_region_count?: number
}

export interface TenantResidencyPolicy {
  allowed_regions: string[]
  default_region_pin: string
}

export async function getTenantResidencyPolicy(): Promise<TenantResidencyPolicy> {
  const { data } = await api.get<TenantResidencyPolicy>('/tenant/residency-policy')
  return data
}

export async function putTenantResidencyPolicy(p: TenantResidencyPolicy) {
  const { data } = await api.put<TenantResidencyPolicy>('/tenant/residency-policy', p)
  return data
}

export interface WorkspaceRegion {
  workspace_id: string
  workspace_name: string
  region_pin: string
}

export async function listWorkspaceRegions(): Promise<WorkspaceRegion[]> {
  const { data } = await api.get<WorkspaceRegion[]>('/tenant/workspace-regions')
  return data ?? []
}

export interface ResidencyMigration {
  id: string
  source_region: string
  target_region: string
  status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled'
  total_docs: number
  moved_docs: number
  failed_docs: number
  created_at: string
  completed_at?: string
  workflow_run_id?: string
  error_summary?: string
  items?: Record<string, number>
}

export async function getResidencyStats(): Promise<ResidencyRow[]> {
  const { data } = await api.get<ResidencyRow[]>('/residency/stats')
  return data ?? []
}

export async function listResidencyMigrations(): Promise<ResidencyMigration[]> {
  const { data } = await api.get<ResidencyMigration[]>('/residency/migrations')
  return data ?? []
}

export async function createResidencyMigration(input: {
  source_region: string
  target_region: string
  filter_workspace?: string
  filter_document_class?: string
}): Promise<{ id: string; status: string }> {
  const { data } = await api.post<{ id: string; status: string }>('/residency/migrations', input)
  return data
}
