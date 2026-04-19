import { api } from './client'

export interface ResidencyRow {
  region: string
  doc_count: number
  blob_bytes: number
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
