import { api } from './client'

// Hold-scoped e-discovery export (services/document ediscovery_hold.go).

export interface HoldScope {
  hold_id: string
  query?: string
  doc_count: number
  size_bytes: number
}

export type ExportStatus = 'pending' | 'running' | 'completed' | 'failed'

export interface ExportJob {
  id: string
  hold_id?: string
  query?: string
  format: string
  status: ExportStatus
  doc_count: number
  size_bytes: number
  error?: string
  created_at: string
  updated_at: string
  completed_at?: string
  download_ready: boolean
}

export async function getScope(holdId: string, query: string): Promise<HoldScope> {
  const { data } = await api.get<HoldScope>('/admin/ediscovery/scope', {
    params: { hold_id: holdId, query: query || undefined },
  })
  return data
}

export async function createExportJob(holdId: string, query: string, format = 'edrm'): Promise<ExportJob> {
  const { data } = await api.post<ExportJob>('/admin/ediscovery/jobs', {
    hold_id: holdId, query, format,
  })
  return data
}

export async function getExportJob(id: string): Promise<ExportJob> {
  const { data } = await api.get<ExportJob>(`/admin/ediscovery/jobs/${id}`)
  return data
}

export async function listExportJobs(): Promise<ExportJob[]> {
  const { data } = await api.get<{ jobs: ExportJob[] }>('/admin/ediscovery/jobs')
  return data.jobs ?? []
}

// Fetch the export ZIP (auth header carried by the api client) and trigger a
// browser download.
export async function downloadExportJob(id: string): Promise<void> {
  const resp = await api.get(`/admin/ediscovery/jobs/${id}/download`, { responseType: 'blob' })
  const url = URL.createObjectURL(resp.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `ediscovery-export-${id}.zip`
  a.click()
  URL.revokeObjectURL(url)
}
