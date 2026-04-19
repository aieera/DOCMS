import { api } from './client'

export interface AdminShareLink {
  id: string
  document_id: string
  document_title?: string
  password_protected: boolean
  expires_at?: string
  accessed_at?: string
  max_views: number
  view_count: number
  permissions: string[]
  is_active: boolean
  created_by?: string
  created_at: string
}

export async function listAdminShareLinks(
  opts: { status?: 'active' | 'all' } = {},
): Promise<AdminShareLink[]> {
  const { data } = await api.get<AdminShareLink[]>('/admin/share-links', {
    params: { status: opts.status ?? 'active' },
  })
  return data ?? []
}

export async function revokeAdminShareLink(id: string): Promise<void> {
  await api.post(`/admin/share-links/${id}/revoke`)
}

export async function revokeAllShareLinksForDocument(
  documentId: string,
): Promise<{ revoked: number }> {
  const { data } = await api.post<{ revoked: number }>(
    `/admin/documents/${documentId}/share-links/revoke-all`,
  )
  return data
}
