import { api } from './client'

// -------- MFA -------------------------------------------------------------

export interface MFASetupResult {
  secret: string
  qr_code_uri: string
  recovery_codes: string[]
  warning?: string
}

export async function setupMFA() {
  const { data } = await api.post<MFASetupResult>('/auth/mfa/setup')
  return data
}

export async function confirmMFA(totpCode: string) {
  const { data } = await api.post<{ mfa_enabled: boolean }>('/auth/mfa/confirm', {
    totp_code: totpCode,
  })
  return data
}

export async function disableMFA(input: { totp_code?: string; recovery_code?: string }) {
  const { data } = await api.post<{ mfa_enabled: boolean }>('/auth/mfa/disable', input)
  return data
}

// -------- Sessions --------------------------------------------------------

// listSessions + its row type (SessionRow) live in api/auth.ts — the
// canonical version routes through unwrapList<SessionRow> so a
// malformed response surfaces as react-query isError instead of the
// silent `data.sessions ?? []` fail this module used to do.
// useSecurity imports listSessions from there.

export async function revokeSession(id: string) {
  await api.delete(`/auth/sessions/${id}`)
}

export async function revokeAllOtherSessions() {
  const { data } = await api.post<{ revoked: number }>('/auth/sessions/revoke-all')
  return data
}

// -------- API keys --------------------------------------------------------

export interface APIKey {
  key_id: string
  key_prefix: string
  name: string
  scopes: string[]
  created_at: string
  last_used_at?: string | null
  expires_at?: string | null
  revoked_at?: string | null
}

export interface APIKeyIssued extends APIKey {
  api_key: string // plaintext; displayed once
  warning?: string
}

export async function listAPIKeys() {
  const { data } = await api.get<{ api_keys: APIKey[] }>('/auth/api-keys')
  return data.api_keys ?? []
}

export async function createAPIKey(input: { name: string; scopes: string[]; expires_in_days?: number }) {
  const { data } = await api.post<APIKeyIssued>('/auth/api-keys', input)
  return data
}

export async function revokeAPIKey(id: string) {
  await api.delete(`/auth/api-keys/${id}`)
}
