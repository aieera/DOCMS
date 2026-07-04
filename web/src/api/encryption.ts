import { api } from './client'

// External-KMS / KEK admin (§5/§8). Backed by services/auth
// /api/v1/admin/encryption/{status,register,rotate,revoke}.

export type KMSProvider = 'local' | 'vault' | 'aws_kms' | 'azure_kv'

export interface KEKVersion {
  version: number
  alias: string
  provider: KMSProvider
  external_key_ref?: string
  created_at: string
  retired_at?: string
  revoked_at?: string
  active: boolean
}

export interface RotateResult {
  status: string
  retired_version: number
  live_version: number
  note: string
}

export async function getEncryptionStatus(): Promise<{ versions: KEKVersion[] }> {
  const { data } = await api.get<{ versions: KEKVersion[] }>('/admin/encryption/status')
  return data
}

export async function registerKMS(body: {
  provider: KMSProvider
  external_key_ref: string
}): Promise<void> {
  await api.post('/admin/encryption/register', body)
}

export async function rotateKEK(body: {
  provider?: KMSProvider
  external_key_ref?: string
}): Promise<RotateResult> {
  const { data } = await api.post<RotateResult>('/admin/encryption/rotate', body)
  return data
}

export async function revokeKEK(version: number): Promise<void> {
  await api.post('/admin/encryption/revoke', { version })
}
