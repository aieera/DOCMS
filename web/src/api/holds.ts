import { api } from './client'

export interface LegalHold {
  id: string
  tenant_id: string
  name: string
  description?: string
  matter_reference?: string
  applied_by?: string
  applied_at: string
  released_by?: string
  released_at?: string
  is_active: boolean
  document_ids?: string[]
}

export async function listHolds(opts: { status?: 'active' | 'released' } = {}): Promise<LegalHold[]> {
  const { data } = await api.get<LegalHold[]>('/compliance/holds', { params: opts })
  return data ?? []
}

export async function getHold(id: string): Promise<LegalHold> {
  const { data } = await api.get<LegalHold>(`/compliance/holds/${id}`)
  return data
}

export async function createHold(input: {
  name: string
  description?: string
  matter_reference?: string
  document_ids: string[]
}): Promise<LegalHold> {
  const { data } = await api.post<LegalHold>('/compliance/holds', input)
  return data
}

export async function releaseHold(id: string, input: { reason: string; approver_id: string }): Promise<LegalHold> {
  const { data } = await api.post<LegalHold>(`/compliance/holds/${id}/release`, input)
  return data
}
