// Zero-trust view-only share — ADR 0098 §18 F2.
import { api } from './client'

export interface ZTCreateRequest {
  document_id: string
  version_id: string
  recipient_email: string
  expires_in_hours: number
  max_views: number
}

export interface ZTCreateResponse {
  token_id: string
  share_url: string
  expires_at: string
}

export interface ZTManifest {
  token_id: string
  document_title: string
  mime_type: string
  page_count: number
  watermark_text: string
  sender_name: string
  expires_at: string
  revoked: boolean
  max_views: number
  view_count: number
}

export interface ZTTelemetryEvent {
  event_type: 'page_view' | 'scroll' | 'focus_blur' | 'devtools_open'
  page_number?: number
  dwell_ms?: number
  created_at: string
  user_agent?: string
}

export async function createZTShare(req: ZTCreateRequest) {
  const { data } = await api.post<ZTCreateResponse>('/admin/share-links/zt', req)
  return data
}

export async function revokeZTShare(tokenId: string) {
  await api.post(`/admin/share-links/zt/${tokenId}/revoke`)
}

export async function getZTTelemetry(tokenId: string, limit = 100) {
  const { data } = await api.get<ZTTelemetryEvent[]>(
    `/admin/share-links/zt/${tokenId}/telemetry?limit=${limit}`,
  )
  return data
}

// Public manifest fetch — used by the recipient viewer. No auth header.
export async function fetchZTManifest(tokenId: string): Promise<ZTManifest> {
  // We can't use `api` (it sends the cookie + headers); use fetch directly.
  const res = await fetch(`/api/v1/zt/${tokenId}/manifest`)
  if (!res.ok) throw new Error(`manifest ${res.status}`)
  return res.json()
}

export async function postZTTelemetry(
  tokenId: string,
  body: { event_type: string; page_number?: number; dwell_ms?: number },
) {
  await fetch(`/api/v1/zt/${tokenId}/telemetry`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    // best-effort; don't fail the viewer if telemetry can't reach the server
    keepalive: true,
  }).catch(() => {})
}
