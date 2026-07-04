import { api } from './client'

// SCIM admin (services/auth scim_admin.go).

export interface ScimInfo {
  slug: string
  configured: boolean
  base_path: string
}

export interface ScimLogEntry {
  id: string
  action: 'provisioned' | 'updated' | 'deprovisioned' | 'deleted'
  resource_type: 'user' | 'group'
  external_id?: string
  user_id?: string
  detail?: string
  created_at: string
}

export async function getScimInfo(): Promise<ScimInfo> {
  const { data } = await api.get<ScimInfo>('/admin/scim/info')
  return data
}

// Returns the plaintext token ONCE — it is never retrievable again.
export async function rotateScimToken(): Promise<string> {
  const { data } = await api.post<{ token?: string; error?: string }>('/admin/scim/token/rotate')
  if (!data.token) throw new Error(data.error ?? 'rotation failed')
  return data.token
}

export async function getScimLog(): Promise<ScimLogEntry[]> {
  const { data } = await api.get<{ entries: ScimLogEntry[] }>('/admin/scim/log')
  return data.entries ?? []
}
