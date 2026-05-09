// ADR 0063 — multi-method MFA client. Mirrors /api/v1/auth/mfa/* and
// /api/v1/admin/mfa/policy. The login picker uses the public
// (mfa-session-token gated) routes; the settings page uses the
// authenticated enrollment routes; the admin tenant page uses the
// policy routes.

import { api } from './client'

export type MFAMethod = 'passkey' | 'totp' | 'push' | 'email' | 'sms'

export interface EnrolledMethod {
  method: MFAMethod
  strength: number
  destination?: string // masked: "***2671" / "a***@example.com"
}

export interface TenantMFAPolicy {
  mode: 'disabled' | 'optional' | 'required' | 'conditional'
  allowed_methods: MFAMethod[]
  conditional_actions: string[]
  updated_at?: string
}

// ---- public (post-password, mfa_session_token gated) ---------------------

export async function listEnrolledMethodsByMFASession(mfa_session_token: string): Promise<EnrolledMethod[]> {
  const { data } = await api.post<{ methods: EnrolledMethod[] }>('/auth/mfa/methods', { mfa_session_token })
  return data.methods ?? []
}

export async function startEmailOTP(mfa_session_token: string): Promise<void> {
  await api.post('/auth/mfa/email/start', { mfa_session_token })
}
export async function verifyEmailOTP(mfa_session_token: string, code: string) {
  const { data } = await api.post('/auth/mfa/email/verify', { mfa_session_token, code })
  return data
}

export async function startSMSOTP(mfa_session_token: string): Promise<void> {
  await api.post('/auth/mfa/sms/start', { mfa_session_token })
}
export async function verifySMSOTP(mfa_session_token: string, code: string) {
  const { data } = await api.post('/auth/mfa/sms/verify', { mfa_session_token, code })
  return data
}

export async function startPushChallenge(mfa_session_token: string): Promise<{ challenge_id: string }> {
  const { data } = await api.post<{ challenge_id: string }>('/auth/mfa/push/start', { mfa_session_token })
  return data
}
export async function verifyPushChallenge(mfa_session_token: string, challenge_id: string, ack: string) {
  const { data } = await api.post('/auth/mfa/push/verify', { mfa_session_token, challenge_id, ack })
  return data
}

// ---- authenticated enrollment --------------------------------------------

export async function listMyEnrolledMethods(): Promise<EnrolledMethod[]> {
  const { data } = await api.get<{ methods: EnrolledMethod[] }>('/auth/mfa/methods')
  return data.methods ?? []
}

export async function enrollEmailMFA(email: string): Promise<void> {
  await api.post('/auth/mfa/email/enroll', { email })
}
export async function enrollSMSMFA(phone: string): Promise<void> {
  await api.post('/auth/mfa/sms/enroll', { phone })
}
export async function registerPushDevice(input: { platform: 'fcm' | 'apns'; token: string; label: string }) {
  const { data } = await api.post<{ device_id: string; ack_key: string }>('/auth/mfa/push/devices', input)
  return data
}
export async function disableMFAMethod(method: MFAMethod): Promise<void> {
  await api.delete(`/auth/mfa/methods/${method}`)
}

// ---- admin tenant policy --------------------------------------------------

export async function getTenantMFAPolicy(): Promise<TenantMFAPolicy> {
  const { data } = await api.get<TenantMFAPolicy>('/admin/mfa/policy')
  return data
}
export async function saveTenantMFAPolicy(p: TenantMFAPolicy): Promise<void> {
  await api.put('/admin/mfa/policy', p)
}
