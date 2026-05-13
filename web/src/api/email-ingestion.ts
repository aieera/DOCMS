import { api } from './client'

export type EmailSource = 'microsoft' | 'gmail' | 'imap'

export interface EmailConfig {
  id: string
  tenant_id: string
  source: EmailSource
  label: string
  active: boolean
  oauth_provider?: string
  imap_host?: string
  imap_port?: number
  imap_use_tls: boolean
  imap_username?: string
  target_workspace_id?: string
  target_folder_id?: string
  poll_interval_seconds: number
  last_run_at?: string
  last_success_at?: string
  last_error?: string
  messages_ingested: number
  created_at: string
}

export interface CreateEmailConfigInput {
  source: EmailSource
  label: string
  oauth_provider?: string
  imap_host?: string
  imap_port?: number
  imap_use_tls?: boolean
  imap_username?: string
  imap_password?: string
  target_workspace_id?: string
  target_folder_id?: string
  poll_interval_seconds?: number
}

export interface PatchEmailConfigInput {
  active?: boolean
  label?: string
  target_workspace_id?: string
  target_folder_id?: string
  poll_interval_seconds?: number
}

export interface EmailConfigStats {
  config_id: string
  messages_total: number
  messages_pending: number
  messages_failed: number
  last_run_at?: string
  last_success_at?: string
  last_error?: string
  next_run_at?: string
}

export async function listEmailConfigs(): Promise<EmailConfig[]> {
  const { data } = await api.get<EmailConfig[]>('/admin/email-configs')
  return data ?? []
}

export async function createEmailConfig(input: CreateEmailConfigInput): Promise<EmailConfig> {
  const { data } = await api.post<EmailConfig>('/admin/email-configs', input)
  return data
}

export async function patchEmailConfig(id: string, input: PatchEmailConfigInput): Promise<EmailConfig> {
  const { data } = await api.patch<EmailConfig>(`/admin/email-configs/${id}`, input)
  return data
}

export async function deleteEmailConfig(id: string): Promise<void> {
  await api.delete(`/admin/email-configs/${id}`)
}

export async function runEmailConfig(id: string): Promise<{ ingested: number }> {
  const { data } = await api.post<{ ingested: number }>(`/admin/email-configs/${id}/run`)
  return data
}

export async function getEmailConfigStats(id: string): Promise<EmailConfigStats> {
  const { data } = await api.get<EmailConfigStats>(`/admin/email-configs/${id}/stats`)
  return data
}
