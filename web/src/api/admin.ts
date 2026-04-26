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

export async function inviteUser(email: string, role: string, displayName: string, workspaceIds?: string[]) {
  const { data } = await api.post('/admin/users/invite', {
    email,
    role,
    display_name: displayName,
    workspace_ids: workspaceIds,
  })
  return data
}

export async function suspendUser(id: string) {
  await api.post(`/admin/users/${id}/suspend`)
}

export async function resetMFA(id: string) {
  await api.post(`/admin/users/${id}/reset-mfa`)
}

export async function getAuditLog(params?: Record<string, string>) {
  // Backend path is /api/v1/audit/events (see services/audit handler).
  // The axios client already prefixes /api/v1.
  const { data } = await api.get('/audit/events', { params })
  return data
}

export interface AuditIntegrityResult {
  ok: boolean
  verified_count: number
  broken_at?: string // event id where the chain breaks, if any
  message?: string
}

export async function verifyAuditIntegrity() {
  const { data } = await api.post<AuditIntegrityResult>('/audit/verify-integrity')
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

// eDiscovery export — streams a signed ZIP bundling docs by id.
// The backend (services/document/internal/handler/ediscovery_handler.go)
// is compliance-officer / admin / owner gated. Filename is set by
// the server's Content-Disposition; we just pull it off the response
// header so the saved file matches what audit logged.
export async function exportEDiscovery(input: {
  case_id: string
  case_name: string
  custodian_email: string
  document_ids: string[]
}): Promise<{ filename: string }> {
  const resp = await api.post('/admin/ediscovery/export', input, { responseType: 'blob' })
  const dispo = (resp.headers['content-disposition'] || resp.headers['Content-Disposition']) as string | undefined
  const match = dispo?.match(/filename="([^"]+)"/)
  const filename = match?.[1] ?? `ediscovery-${input.case_id}.zip`
  const url = URL.createObjectURL(resp.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
  return { filename }
}

export async function getTenantSettings() {
  const { data } = await api.get('/admin/settings')
  return data
}

export async function updateTenantSettings(body: Record<string, unknown>) {
  const { data } = await api.put('/admin/settings', body)
  return data
}
