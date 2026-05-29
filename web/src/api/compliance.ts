import { api } from './client'

export interface ComplianceOverview {
  docs_by_state: { name: string; count: number }[]
  storage_by_region: { name: string; gb: number }[]
  encryption_coverage: number
  encrypted_blobs: number
  total_blobs: number
}

export async function getComplianceOverview(): Promise<ComplianceOverview> {
  const { data } = await api.get<ComplianceOverview>('/admin/compliance/overview')
  return data
}
