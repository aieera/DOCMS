import { api } from './client'

export type Provider = 'saml' | 'oidc'

export interface SSOConfig {
  id: string
  provider: Provider
  display_name: string
  is_active: boolean
  config: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface SAMLConfigBody {
  idp_metadata_url?: string
  idp_metadata_xml?: string
  attribute_mapping?: {
    email?: string
    display_name?: string
    groups?: string
  }
  nameid_format?: string
  require_signed_assertion?: boolean
  require_signed_response?: boolean
  clock_skew_seconds?: number
}

export interface OIDCConfigBody {
  issuer_url: string
  client_id: string
  client_secret: string
  redirect_url: string
  scopes?: string[]
}

export interface ValidateResult {
  ok: boolean
  provider: Provider
  warnings?: string[]
  details?: Record<string, string>
  error?: string
}

export async function listSSOConfigs(): Promise<SSOConfig[]> {
  const { data } = await api.get<SSOConfig[]>('/admin/sso-configs')
  return data ?? []
}

export async function createSSOConfig(input: {
  provider: Provider
  display_name: string
  config: SAMLConfigBody | OIDCConfigBody
  is_active?: boolean
}): Promise<{ id: string; provider: Provider }> {
  const { data } = await api.post<{ id: string; provider: Provider }>('/admin/sso-configs', input)
  return data
}

export async function updateSSOConfig(
  id: string,
  input: { display_name?: string; config?: SAMLConfigBody | OIDCConfigBody; is_active?: boolean },
): Promise<void> {
  await api.patch(`/admin/sso-configs/${id}`, input)
}

export async function deleteSSOConfig(id: string): Promise<void> {
  await api.delete(`/admin/sso-configs/${id}`)
}

export async function validateSSOConfig(
  provider: Provider,
  config: SAMLConfigBody | OIDCConfigBody,
): Promise<ValidateResult> {
  const { data } = await api.post<ValidateResult>('/admin/sso-configs/validate', {
    provider,
    display_name: 'validation',
    config,
  })
  return data
}
