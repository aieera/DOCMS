import { api } from './client'

export type MFAMode = 'optional' | 'required' | 'conditional'

export interface TenantMFAPolicy {
  mode: MFAMode
  allowed_methods: string[]
  conditional_actions: string[]
  updated_at?: string
}

export async function getMFAPolicy(): Promise<TenantMFAPolicy> {
  const { data } = await api.get<TenantMFAPolicy>('/admin/mfa/policy')
  return data
}

export async function saveMFAPolicy(policy: TenantMFAPolicy): Promise<void> {
  await api.put('/admin/mfa/policy', policy)
}
