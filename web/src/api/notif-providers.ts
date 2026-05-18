// Per-tenant Twilio (SMS MFA) + SMTP (transactional + email OTP)
// admin credentials. Mounted by the auth service at
// /api/v1/admin/notifications/*. Secrets are never returned by GET;
// has_auth_token / has_password are presence flags only.
import { api } from './client'

export interface TwilioConfigPublic {
  account_sid: string
  has_auth_token: boolean
  verify_service_sid: string
  updated_at: string
}

export interface SaveTwilioConfigInput {
  account_sid: string
  auth_token: string // leave blank on edit to keep the existing sealed token
  verify_service_sid: string
}

export interface SMTPConfigPublic {
  host: string
  port: number
  username: string
  has_password: boolean
  from_addr: string
  starttls: boolean
  updated_at: string
}

export interface SaveSMTPConfigInput {
  host: string
  port: number
  username: string
  password: string // leave blank on edit to keep the existing sealed password
  from_addr: string
  starttls: boolean
}

async function getOrNull<T>(url: string): Promise<T | null> {
  try {
    const { data } = await api.get<T>(url)
    return data
  } catch (e) {
    if ((e as { response?: { status?: number } }).response?.status === 404) return null
    throw e
  }
}

export const getTwilioConfig = () => getOrNull<TwilioConfigPublic>('/admin/notifications/twilio')
export async function saveTwilioConfig(input: SaveTwilioConfigInput): Promise<void> {
  await api.put('/admin/notifications/twilio', input)
}
export async function deleteTwilioConfig(): Promise<void> {
  await api.delete('/admin/notifications/twilio')
}
export async function testTwilio(phoneE164: string): Promise<void> {
  await api.post('/admin/notifications/twilio/test', { phone_e164: phoneE164 })
}

export const getSMTPConfig = () => getOrNull<SMTPConfigPublic>('/admin/notifications/smtp')
export async function saveSMTPConfig(input: SaveSMTPConfigInput): Promise<void> {
  await api.put('/admin/notifications/smtp', input)
}
export async function deleteSMTPConfig(): Promise<void> {
  await api.delete('/admin/notifications/smtp')
}
export async function testSMTP(to: string): Promise<void> {
  await api.post('/admin/notifications/smtp/test', { to })
}
