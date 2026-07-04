import { api } from './client'

export interface LoginUser {
  id: string
  email: string
  display_name: string
  role: string
  tenant_id: string
}

// Shape returned by POST /auth/login and POST /auth/mfa/verify
// (services/auth handler loginResponse). tenant_id is on the user view.
export interface LoginResponse {
  session_token?: string
  expires_at?: string
  user?: LoginUser
  mfa_required?: boolean
  mfa_session_token?: string
  mfa_methods?: string[]
}

export async function login(email: string, password: string): Promise<LoginResponse> {
  const { data } = await api.post<LoginResponse>('/auth/login', { email, password })
  return data
}

export async function verifyMFA(mfaSessionToken: string, totpCode: string): Promise<LoginResponse> {
  const { data } = await api.post<LoginResponse>('/auth/mfa/verify', {
    mfa_session_token: mfaSessionToken,
    totp_code: totpCode,
  })
  return data
}
