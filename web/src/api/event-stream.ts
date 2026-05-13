import { api } from './client'

export interface EventStreamToken {
  id: string
  label: string
  bearer_token?: string         // present only at issuance
  nats_creds_file?: string      // present only at issuance
  nats_account_id?: string
  created_at: string
  expires_at?: string
  last_used_at?: string
  revoked: boolean
}

export async function listEventStreamTokens(): Promise<EventStreamToken[]> {
  const { data } = await api.get<EventStreamToken[]>('/admin/event-stream/tokens')
  return data ?? []
}

export async function issueEventStreamToken(label: string): Promise<EventStreamToken> {
  const { data } = await api.post<EventStreamToken>('/admin/event-stream/tokens', { label })
  return data
}

export async function revokeEventStreamToken(id: string): Promise<void> {
  await api.delete(`/admin/event-stream/tokens/${id}`)
}
