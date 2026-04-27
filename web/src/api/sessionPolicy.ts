// Tenant session-policy API client. Backend: Blueprint §8.1,
// auth service GET/PUT /api/v1/tenant/session-policy.

import { api } from './client'

export type BindingStrictness = 'none' | 'warn' | 'enforce'

export interface SessionPolicy {
  ttl_hours: number
  sliding_minutes: number
  absolute_max_days: number
  concurrent_limit: number
  binding_strictness: BindingStrictness
}

export async function getSessionPolicy(): Promise<SessionPolicy> {
  const { data } = await api.get<SessionPolicy>('/tenant/session-policy')
  return data
}

export async function putSessionPolicy(input: SessionPolicy): Promise<SessionPolicy> {
  const { data } = await api.put<SessionPolicy>('/tenant/session-policy', input)
  return data
}
