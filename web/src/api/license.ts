// Tenant license state. Read-only.
// Today returns `unlicensed_dev_mode`; future JWT validator (ADR 0095)
// surfaces real claims here without changing the response shape.
import { api } from './client'

export type LicenseStatus = 'active' | 'grace' | 'expired' | 'unlicensed_dev_mode'

export interface LicenseFeatureFlags {
  esign?: boolean
  mcp?: boolean
  ipaas?: boolean
  intel_llm?: boolean
  connectors?: string[]
  regions?: string[]
}

export interface LicenseResponse {
  status: LicenseStatus
  tenant_name?: string
  seat_limit?: number
  seats_used?: number
  feature_flags?: LicenseFeatureFlags
  expiry?: string
  days_remaining?: number
  grace_days: number
  issued_to?: string
  issued_by?: string
  adr: string
  enforcement_message: string
}

export async function getLicense(): Promise<LicenseResponse> {
  const { data } = await api.get<LicenseResponse>('/admin/tenant/license')
  return data
}
