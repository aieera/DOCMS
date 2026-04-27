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

// Wave 17 — §9.3 closure surfaces (custodian inbox + chain audit).

export interface Custodian {
  id: string
  hold_id: string
  user_id: string
  notified_at?: string
  acknowledged_at?: string
  created_at: string
}

export interface MyHoldRow {
  hold_id: string
  hold_name: string
  matter_reference?: string
  is_active: boolean
  notified_at?: string
  acknowledged_at?: string
}

export interface HoldEvent {
  id: string
  hold_id: string
  sequence: number
  event_type: string
  actor_id?: string
  payload: Record<string, unknown>
  prev_hash: string
  self_hash: string
  occurred_at: string
}

export interface VerifyChainResult {
  ok: boolean
  event_count: number
  broken_at?: number
  message?: string
}

export async function listMyHolds(): Promise<MyHoldRow[]> {
  const { data } = await api.get<{ holds: MyHoldRow[] }>('/compliance/holds/my')
  return data.holds ?? []
}

export async function listCustodians(holdID: string): Promise<Custodian[]> {
  const { data } = await api.get<{ custodians: Custodian[] }>(`/compliance/holds/${holdID}/custodians`)
  return data.custodians ?? []
}

export async function addCustodians(holdID: string, userIDs: string[]): Promise<Custodian[]> {
  const { data } = await api.post<{ custodians: Custodian[] }>(
    `/compliance/holds/${holdID}/custodians`,
    { user_ids: userIDs },
  )
  return data.custodians ?? []
}

export async function acknowledgeCustodian(holdID: string, userID: string): Promise<Custodian> {
  const { data } = await api.post<Custodian>(
    `/compliance/holds/${holdID}/custodians/${userID}/acknowledge`,
  )
  return data
}

export async function listHoldEvents(holdID: string): Promise<HoldEvent[]> {
  const { data } = await api.get<{ events: HoldEvent[] }>(`/compliance/holds/${holdID}/events`)
  return data.events ?? []
}

export async function verifyHoldChain(holdID: string): Promise<VerifyChainResult> {
  const { data } = await api.get<VerifyChainResult>(`/compliance/holds/${holdID}/verify-chain`)
  return data
}
