import { api } from './client'
import type { User } from '@/types/api'

export async function login(email: string, password: string, tenantSlug: string) {
  const { data } = await api.post<{
    user: User
    session_token?: string
    expires_at?: string
    mfa_required?: boolean
    mfa_session_token?: string
  }>('/auth/login', { email, password, tenant_slug: tenantSlug })
  return data
}

export async function register(email: string, password: string, display_name: string, tenantSlug: string) {
  const { data } = await api.post('/auth/register', { email, password, display_name, tenant_slug: tenantSlug })
  return data
}

export async function acceptInvite(tenantSlug: string, token: string, password: string) {
  const { data } = await api.post<{
    user_id: string
    email: string
    display_name: string
    tenant_id: string
    tenant_slug: string
  }>('/auth/accept-invite', { tenant_slug: tenantSlug, token, password })
  return data
}

export async function getCurrentUser() {
  const { data } = await api.get<User>('/auth/me')
  return data
}

export async function logout() {
  await api.post('/auth/logout')
}

export async function verifyMFA(code: string) {
  const { data } = await api.post('/auth/mfa/verify', { code })
  return data
}
