import { api } from './client'

// Wave 15.1 — acknowledgement campaigns (policy attestations).

export type AcknowledgementStatus = 'draft' | 'active' | 'closed' | 'archived'

export interface Campaign {
  id: string
  tenant_id: string
  document_id: string
  version_id?: string
  title: string
  body_md?: string
  due_at: string
  status: AcknowledgementStatus
  created_by: string
  created_at: string
  updated_at: string
  closed_at?: string
}

export interface Assignment {
  id: string
  campaign_id: string
  assignee_user_id: string
  assigned_at: string
  reminded_count: number
  reminded_at?: string
  acknowledged_at?: string
  reminded_at_: string | null
  escalated_at?: string
}

export interface Report {
  campaign_id: string
  total: number
  acknowledged: number
  overdue: number
  escalated: number
  acknowledgement_rate: number
  due_at: string
}

export async function listCampaigns(status?: AcknowledgementStatus) {
  const { data } = await api.get<{ campaigns: Campaign[] }>(
    '/acknowledgement/campaigns',
    { params: status ? { status } : undefined },
  )
  return data.campaigns ?? []
}

export async function createCampaign(input: {
  document_id: string
  title: string
  body_md?: string
  due_at: string
  recipient_policy: { users?: string[]; groups?: string[]; roles?: string[] }
  activate?: boolean
}) {
  const { data } = await api.post<Campaign>('/acknowledgement/campaigns', input)
  return data
}

export async function closeCampaign(id: string) {
  await api.post(`/acknowledgement/campaigns/${id}/close`)
}

export async function getReport(id: string) {
  const { data } = await api.get<Report>(`/acknowledgement/campaigns/${id}/report`)
  return data
}

export async function getMyPending() {
  const { data } = await api.get<{ pending: Assignment[] }>('/acknowledgement/my')
  return data.pending ?? []
}

export async function acknowledge(assignmentId: string, comment?: string) {
  const { data } = await api.post<Assignment>(
    `/acknowledgement/assignments/${assignmentId}/ack`,
    comment ? { comment } : {},
  )
  return data
}
