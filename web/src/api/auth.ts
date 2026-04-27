import { api } from './client'
import type { User } from '@/types/api'

export async function login(email: string, password: string, tenantSlug: string) {
  const { data } = await api.post<{
    user: User
    session_token?: string
    expires_at?: string
    mfa_required?: boolean
    mfa_session_token?: string
    require_password_change?: boolean
    one_time_change_token?: string
  }>('/auth/login', { email, password, tenant_slug: tenantSlug })
  return data
}

export async function changePassword(oneTimeChangeToken: string, newPassword: string) {
  const { data } = await api.post<{ user: User; expires_at?: string }>(
    '/auth/change-password',
    { one_time_change_token: oneTimeChangeToken, new_password: newPassword }
  )
  return data
}

export async function register(email: string, password: string, display_name: string, tenantSlug: string) {
  const { data } = await api.post('/auth/register', { email, password, display_name, tenant_slug: tenantSlug })
  return data
}

export async function getCurrentUser() {
  const { data } = await api.get<User>('/auth/me')
  return data
}

export async function logout() {
  await api.post('/auth/logout')
}

export async function verifyMFA(mfaSessionToken: string, totpCode: string) {
  const { data } = await api.post<{ user: User; expires_at?: string }>('/auth/mfa/verify', {
    mfa_session_token: mfaSessionToken,
    totp_code: totpCode,
  })
  return data
}

// GAP-3: Public forgot-password entry. Always 202 with a generic
// message regardless of whether the email matches a real user — the
// no-oracle defense means the UI shouldn't surface backend distinctions
// either. Errors are swallowed by the api client at this layer.
export async function forgotPassword(tenantSlug: string, email: string) {
  const { data } = await api.post<{ accepted: boolean; message: string }>(
    '/auth/forgot-password',
    { tenant_slug: tenantSlug, email },
  )
  return data
}

export async function recoverMFA(mfaSessionToken: string, recoveryCode: string) {
  const { data } = await api.post<{ user: User; expires_at?: string }>('/auth/mfa/recovery', {
    mfa_session_token: mfaSessionToken,
    recovery_code: recoveryCode,
  })
  return data
}
