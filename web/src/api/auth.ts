import { api } from './client'
import { unwrapList } from '@/lib/unwrapList'
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

// Minimal tenant people directory — available to EVERY authenticated
// user (the /admin/users list is admin-gated). Feeds @mention pickers
// and the Share dialog's people mode.
export interface DirectoryUser {
  id: string
  display_name: string
  email: string
}

export async function listUserDirectory(q?: string) {
  const { data } = await api.get<{ users: DirectoryUser[] }>('/auth/users/directory', {
    params: q ? { q } : undefined,
  })
  return data.users ?? []
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

export async function listSessions(): Promise<SessionRow[]> {
  // L-3 / Wave 5 pattern 3: same dual-shape normalization as
  // getVersions. Throws on unknown shapes so the consumer's react-
  // query hook surfaces an error state instead of an empty list
  // pretending the user has no other sessions.
  const { data } = await api.get<unknown>('/auth/sessions')
  return unwrapList<SessionRow>(data, 'sessions')
}

export async function revokeSession(id: string) {
  await api.delete(`/auth/sessions/${id}`)
}

export async function revokeAllSessions() {
  await api.post('/auth/sessions/revoke-all')
}

// ADR 0106 — persist the user's UI-language preference. Server
// validates against the SupportedLocales map; 400 means the FE shipped
// a locale we don't have a bundle for. Caller handles the toast.
export async function updateLocale(locale: 'en' | 'ar'): Promise<void> {
  await api.patch('/auth/me/locale', { locale })
}
