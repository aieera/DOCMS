// Per-tenant upload format allowlist (migration 000060). Admin-only
// surface — owner|admin enforced server-side. Empty arrays mean "no
// allowlist", so the user-facing form treats them as the unrestricted
// default rather than blocking everything.
import { api } from './client'

export interface UploadPolicy {
  allowed_mime_types: string[]
  allowed_extensions: string[]
}

export async function getUploadPolicy(): Promise<UploadPolicy> {
  const { data } = await api.get<UploadPolicy>('/admin/tenant/upload-policy')
  return data
}

export async function setUploadPolicy(p: UploadPolicy): Promise<UploadPolicy> {
  const { data } = await api.put<UploadPolicy>('/admin/tenant/upload-policy', p)
  return data
}
