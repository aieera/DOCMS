// IRM protected-container export — "Protect & share".
//
// Seals a document into a protected container (encrypted payload in S3
// + a signed policy header) and issues a per-recipient license. Opening
// requires an online license check (validates + logs + can be revoked).
// A revoked license blocks the next open. This is access-control +
// audit + revocation, NOT copy-prevention — it does not block
// screenshots.
import { api } from './client'

// The three actions a license can grant. View is always implied for a
// successful check; Print / Download are opt-in per export.
export type AllowedAction = 'view' | 'print' | 'download'

export type RecipientType = 'user' | 'email'

export interface ExportRecipient {
  type: RecipientType
  ref: string
}

export interface ExportProtectedRequest {
  document_id: string
  version_id?: string
  recipients: ExportRecipient[]
  /** ISO 8601 timestamp. */
  expires_at: string
  allowed_actions: AllowedAction[]
}

export interface IssuedLicense {
  license_id: string
  recipient_type: RecipientType
  recipient_ref: string
  /** e.g. `/protected/{container_id}?lt={license_token}` */
  open_url: string
}

export interface ExportProtectedResponse {
  container_id: string
  document_title: string
  licenses: IssuedLicense[]
}

export interface CheckLicenseRequest {
  container_id: string
  license_token: string
}

export interface CheckLicenseResponse {
  ok: true
  allowed_actions: AllowedAction[]
  expires_at: string
  document_title: string
  mime: string
  sender_email: string
}

export interface IrmContainerSummary {
  id: string
  document_id: string
  document_title: string
  created_at: string
  expires_at: string
  recipient_count: number
  revoked: boolean
}

export interface IrmContainerLicense {
  license_id: string
  recipient_type: RecipientType
  recipient_ref: string
  expires_at: string
  open_count: number
  last_opened_at: string | null
  revoked_at: string | null
}

// --- Admin / export endpoints (session cookie) -----------------------

export async function exportProtected(
  body: ExportProtectedRequest,
): Promise<ExportProtectedResponse> {
  const { data } = await api.post<ExportProtectedResponse>('/irm/export', body)
  return data
}

export async function listIrmContainers(): Promise<IrmContainerSummary[]> {
  const { data } = await api.get<{ containers: IrmContainerSummary[] }>(
    '/admin/irm/containers',
  )
  return data.containers ?? []
}

export async function listContainerLicenses(
  containerId: string,
): Promise<IrmContainerLicense[]> {
  const { data } = await api.get<{ licenses: IrmContainerLicense[] }>(
    `/admin/irm/containers/${containerId}/licenses`,
  )
  return data.licenses ?? []
}

export async function revokeLicense(
  licenseId: string,
): Promise<{ status: string }> {
  const { data } = await api.post<{ status: string }>(
    `/admin/irm/licenses/${licenseId}/revoke`,
  )
  return data
}

// --- Viewer / license-check callback ---------------------------------

// The license check is the gate the recipient viewer hits on open.
// External recipients have no session, but internal-bound licenses
// return 403 { error: 'session_required' } — so we send credentials
// (cookie) when present. Using `api` gives us withCredentials + the
// CSRF header the backend expects on this POST.
export async function checkLicense(
  body: CheckLicenseRequest,
): Promise<CheckLicenseResponse> {
  const { data } = await api.post<CheckLicenseResponse>(
    '/irm/licenses/check',
    body,
  )
  return data
}

// irmContentUrl builds the streaming-content URL used as the src of an
// <iframe>/<embed> after a successful check. The token stays in the URL
// only — never persisted to web storage (C2 arch-test).
export function irmContentUrl(containerId: string, token: string): string {
  const p = new URLSearchParams({ container_id: containerId, lt: token })
  return `/api/v1/irm/licenses/content?${p.toString()}`
}
