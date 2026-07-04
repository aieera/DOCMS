import { api } from './client'

// Dynamic viewer watermark (§ viewer-watermark). Backed by the document
// service /api/v1/documents/{doc}/versions/{ver}/wm/* endpoints for the
// per-version rendered images, and /api/v1/admin/watermark/* for the
// tenant admin config + per-classification overrides.
//
// The wm{Page,Thumbnail,Download}Url helpers return raw /api/v1 URLs
// (NOT axios calls): they're consumed by <img src> and window.open,
// which ship the HttpOnly session cookie on same-origin requests just
// like axios does with withCredentials. The `api` instance's baseURL
// is '/api/v1', so these mirror that prefix by hand.

const API_BASE = '/api/v1'

export type WatermarkStatusValue = 'ready' | 'processing' | 'failed' | 'none'

export interface WatermarkStatus {
  status: WatermarkStatusValue
  page_count: number | null
  watermark_enabled: boolean
}

// Classifications the admin can scope an override to. Mirrors the
// document security_classification levels plus the PHI pseudo-level.
export type WatermarkClassification =
  | 'unclassified'
  | 'internal'
  | 'confidential'
  | 'restricted'
  | 'phi'

export const WATERMARK_CLASSIFICATIONS: WatermarkClassification[] = [
  'unclassified',
  'internal',
  'confidential',
  'restricted',
  'phi',
]

export interface WatermarkConfig {
  enabled: boolean
  template: string
  opacity: number // 0-100
  rotation_deg: number
  tile: boolean
  font_size: number
  color: string // hex, e.g. #888888
}

export interface WatermarkOverride {
  id: string
  classification: WatermarkClassification
  enabled: boolean
  opacity: number | null
  tile: boolean | null
  force: boolean
  description: string
  created_at: string
}

export interface CreateWatermarkOverrideInput {
  classification: WatermarkClassification
  enabled: boolean
  opacity?: number | null
  tile?: boolean | null
  force: boolean
  description: string
}

// Tokens the admin can embed in `template`. Rendered server-side per
// viewer; listed in the admin UI helper + used by the live preview.
export const WATERMARK_TOKENS = [
  '{email}',
  '{timestamp}',
  '{ip}',
  '{tenant}',
  '{classification}',
  '{user_id}',
] as const

// ---- Per-version rendered surfaces (cookie-authenticated, not axios) ----

export function wmPageUrl(documentId: string, versionId: string, page: number): string {
  return `${API_BASE}/documents/${documentId}/versions/${versionId}/wm/pages/${page}`
}

export function wmThumbnailUrl(documentId: string, versionId: string): string {
  return `${API_BASE}/documents/${documentId}/versions/${versionId}/wm/thumbnail`
}

export function wmDownloadUrl(documentId: string, versionId: string): string {
  return `${API_BASE}/documents/${documentId}/versions/${versionId}/wm/download`
}

export async function getWatermarkStatus(
  documentId: string,
  versionId: string,
): Promise<WatermarkStatus> {
  const { data } = await api.get<WatermarkStatus>(
    `/documents/${documentId}/versions/${versionId}/wm/status`,
  )
  return data
}

// ---- Admin config + overrides -------------------------------------------

export async function getWatermarkConfig(): Promise<WatermarkConfig> {
  const { data } = await api.get<WatermarkConfig>('/admin/watermark/config')
  return data
}

export async function setWatermarkConfig(cfg: WatermarkConfig): Promise<WatermarkConfig> {
  const { data } = await api.put<WatermarkConfig>('/admin/watermark/config', cfg)
  return data
}

export async function listWatermarkOverrides(): Promise<WatermarkOverride[]> {
  const { data } = await api.get<{ overrides: WatermarkOverride[] }>('/admin/watermark/overrides')
  return data.overrides ?? []
}

export async function createWatermarkOverride(
  body: CreateWatermarkOverrideInput,
): Promise<WatermarkOverride> {
  const { data } = await api.post<WatermarkOverride>('/admin/watermark/overrides', body)
  return data
}

export async function deleteWatermarkOverride(id: string): Promise<void> {
  await api.delete(`/admin/watermark/overrides/${id}`)
}
