import { api } from './client'
import type { User } from '@/types/api'

export async function login(email: string, password: string, tenantSlug: string) {
  const { data } = await api.post<{
    user: User
    session_token?: string
    expires_at?: string
    mfa_required?: boolean
    mfa_session_token?: string
    // ADR 0063 — populated when mfa_required is true. Strongest-first.
    mfa_methods?: { method: 'passkey' | 'totp' | 'push' | 'email' | 'sms'; strength: number; destination?: string }[]
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

export async function verifyMFA(mfa_session_token: string, totp_code: string) {
  const { data } = await api.post<{ user: User; session_token?: string }>(
    '/auth/mfa/verify', { mfa_session_token, totp_code },
  )
  return data
}

// Active sessions for the Settings → Sessions panel.
export interface SessionRow {
  id: string
  ip_address?: string
  user_agent?: string
  created_at: string
  last_activity_at: string
  expires_at: string
  current?: boolean
}

export async function listSessions() {
  const { data } = await api.get<SessionRow[] | { sessions: SessionRow[] }>('/auth/sessions')
  return Array.isArray(data) ? data : (data.sessions ?? [])
}

export async function revokeSession(id: string) {
  await api.delete(`/auth/sessions/${id}`)
}

export async function revokeAllSessions() {
  await api.post('/auth/sessions/revoke-all')
}
