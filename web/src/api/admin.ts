import { api } from './client'
import type { User, PaginatedResponse } from '@/types/api'

export async function getUsers(params?: Record<string, string>): Promise<PaginatedResponse<User>> {
  // Backend returns {users, next_cursor}; the rest of the app expects
  // PaginatedResponse<User> ({items, total_count, page_token}). Map here
  // so the table component doesn't need to know about the shape.
  const { data } = await api.get<{ users: User[]; next_cursor?: string }>('/admin/users', { params })
  return {
    items: data.users ?? [],
    total_count: data.users?.length ?? 0,
    page_token: data.next_cursor,
  }
}

export interface InviteUserResponse {
  user: User
  invite_token: string
  tenant_slug: string
}

export async function inviteUser(
  email: string,
  role: string,
  displayName: string,
  workspaceIds?: string[],
): Promise<InviteUserResponse> {
  const { data } = await api.post<InviteUserResponse>('/admin/users/invite', {
    email,
    role,
    display_name: displayName,
    workspace_ids: workspaceIds,
  })
  return data
}

// createUser sidesteps the invite/email round-trip by setting the password
// directly. Useful for dev/demo bootstrap when SMTP isn't wired up.
export async function createUser(
  email: string,
  password: string,
  displayName: string,
  role: string,
): Promise<{ user: User }> {
  const { data } = await api.post<{ user: User }>('/admin/users', {
    email,
    password,
    display_name: displayName,
    role,
  })
  return data
}

export async function suspendUser(id: string) {
  await api.post(`/admin/users/${id}/suspend`)
}

export async function resetMFA(id: string) {
  await api.post(`/admin/users/${id}/reset-mfa`)
}

export async function changeUserRole(id: string, role: string) {
  await api.patch(`/admin/users/${id}/role`, { role })
}

export async function getAuditLog(params?: Record<string, string>) {
  // Backend path is /api/v1/audit/events (see services/audit handler).
  // The axios client already prefixes /api/v1.
  const { data } = await api.get('/audit/events', { params })
  return data
}

export async function exportAuditCSV(params?: Record<string, string>) {
  const resp = await api.get('/audit/export', { params, responseType: 'blob' })
  const url = URL.createObjectURL(resp.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'audit_log.csv'
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

export async function getTenantSettings() {
  const { data } = await api.get('/admin/settings')
  return data
}

export async function updateTenantSettings(body: Record<string, unknown>) {
  const { data } = await api.put('/admin/settings', body)
  return data
}
