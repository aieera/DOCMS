// Platform-admin info surfaces. Read-only.
import { api } from './client'

export type CapabilityStatus = 'native' | 'degraded' | 'unsupported' | 'not_implemented'

export interface Capability {
  name: string
  description: string
  status: CapabilityStatus
  notes?: string
}

export interface DriverInfo {
  name: string
  display_name: string
  status: 'active' | 'not_implemented'
  version?: string
  capabilities: Capability[]
}

export interface DBInfoResponse {
  active: DriverInfo
  alternate_drivers: DriverInfo[]
  adr: string
}

// getDBInfo returns the current DB driver + version + per-feature
// capability matrix. Honestly reports today's state: PostgreSQL only,
// alt drivers marked "not_implemented". Updated automatically when
// the §13.3 alt-driver work eventually lands (ADR 0094).
export async function getDBInfo(): Promise<DBInfoResponse> {
  const { data } = await api.get<DBInfoResponse>('/admin/platform/db-info')
  return data
}
