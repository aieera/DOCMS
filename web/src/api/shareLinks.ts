import { api } from './client'
import type { Document } from '@/types/api'

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

// ShareLinkAccessResult mirrors proto AccessShareLinkResponse:
//   - password_required=true when the link is password-protected and the
//     caller didn't supply the right password. `document` is then nil.
//   - On success, `document` + `download_url` are populated. The download
//     URL is the public /api/v1/shared/{token}/download alias and is the
//     only credential needed to fetch the bytes (no session cookie).
export interface ShareLinkAccessResult {
  document: Document | null
  download_url: string
  password_required: boolean
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

// accessShareLink hits the public POST /api/v1/shared/{token} endpoint.
// Pass no password for the initial "peek" — the server returns
// {password_required: true, document: null} if a password is set,
// otherwise the full document + download URL. Pass a password to
// verify; a wrong password still returns 200 with password_required=true
// (server-side: backend does not leak existence-vs-wrong-password).
export async function accessShareLink(
  token: string,
  password?: string,
): Promise<ShareLinkAccessResult> {
  const { data } = await api.post<ShareLinkAccessResult>(
    `/shared/${token}`,
    password ? { password } : {},
  )
  return data
}
