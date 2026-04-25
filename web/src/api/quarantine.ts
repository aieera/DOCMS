// Admin quarantine review API. Backed by services/storage's
// quarantine_events table + upload_sessions. Permission gate is
// `quarantine.review` (admin/owner only).

import { api } from './client'

export type QuarantineStatus = 'new' | 'reviewed' | 'released' | 'deleted'
export type QuarantineReason = 'virus' | 'blocked_mime' | 'mime_mismatch'

export interface QuarantineItem {
  id: string
  upload_id: string
  tenant_id: string
  filename: string
  uploader_id: string
  uploader_name: string
  reason: QuarantineReason
  signature: string // virus name OR detected MIME
  declared_mime: string
  detected_mime: string
  size_bytes: number
  created_at: string // ISO
  status: QuarantineStatus
  reviewed_by?: string
  reviewed_at?: string
}

interface QuarantineListResponse {
  items: QuarantineItem[]
  total_count: number
  page_token?: string
}

export async function listQuarantine(params?: Record<string, string>): Promise<QuarantineListResponse> {
  const { data } = await api.get<QuarantineListResponse>('/admin/quarantine', { params })
  return { items: data.items ?? [], total_count: data.total_count ?? 0, page_token: data.page_token }
}

export async function releaseQuarantine(id: string, reason: string) {
  const { data } = await api.post(`/admin/quarantine/${id}/release`, { reason })
  return data
}

export async function deleteQuarantine(id: string, reason: string) {
  const { data } = await api.post(`/admin/quarantine/${id}/delete`, { reason })
  return data
}

export async function markQuarantineReviewed(id: string) {
  const { data } = await api.post(`/admin/quarantine/${id}/reviewed`)
  return data
}
