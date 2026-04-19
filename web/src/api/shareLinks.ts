import { api } from './client'

export interface ShareLink {
  id: string
  document_id: string
  token?: string
  password_protected?: boolean
  expires_at?: string
  max_views?: number
  view_count?: number
  permissions?: string[]
  is_active?: boolean
}

export interface CreateShareLinkInput {
  password?: string
  expires_in_hours?: number
  max_views?: number
  permissions?: string[]
}

export async function createShareLink(documentId: string, input: CreateShareLinkInput = {}) {
  const { data } = await api.post<ShareLink>(
    `/documents/${documentId}/share-links`,
    input,
  )
  return data
}

export async function listShareLinks(documentId: string) {
  const { data } = await api.get<{ share_links: ShareLink[] } | ShareLink[]>(
    `/documents/${documentId}/share-links`,
  )
  return Array.isArray(data) ? data : (data.share_links ?? [])
}

export async function deleteShareLink(linkId: string) {
  await api.delete(`/share-links/${linkId}`)
}

export async function accessShareLink(token: string, password?: string) {
  const { data } = await api.post(`/shared/${token}`, { password })
  return data
}
