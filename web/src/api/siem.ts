import { api } from './client'

// SIEM forwarding sink admin (services/audit siem package).

export type SinkType = 'syslog' | 'splunk_hec' | 'sentinel_hec'

export interface Sink {
  id: string
  name: string
  type: SinkType
  endpoint: string
  enabled: boolean
  delivered_count: number
  failed_count: number
  last_success_at?: string | null
  last_error?: string
  last_error_at?: string | null
  created_at: string
  updated_at: string
}

export interface SinkInput {
  name: string
  type: SinkType
  endpoint: string
  token: string
  enabled?: boolean
}

export async function listSinks(): Promise<Sink[]> {
  const { data } = await api.get<{ sinks: Sink[] }>('/admin/siem/sinks')
  return data.sinks ?? []
}

export async function createSink(input: SinkInput): Promise<Sink> {
  const { data } = await api.post<Sink>('/admin/siem/sinks', input)
  return data
}

export async function updateSink(id: string, input: Partial<SinkInput>): Promise<Sink> {
  const { data } = await api.put<Sink>(`/admin/siem/sinks/${id}`, input)
  return data
}

export async function deleteSink(id: string): Promise<void> {
  await api.delete(`/admin/siem/sinks/${id}`)
}

export async function testSink(id: string): Promise<{ ok: boolean; error?: string }> {
  const { data } = await api.post<{ ok: boolean; error?: string }>(`/admin/siem/sinks/${id}/test`)
  return data
}
