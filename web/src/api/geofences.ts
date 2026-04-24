import { api } from './client'

// Wave 15.2 — geofence policies.

export type GeofenceScope = 'tenant' | 'workspace' | 'document'
export type GeofenceMode = 'allow' | 'deny' | 'step_up'
export type GeofenceAction = 'read' | 'write' | 'admin' | '*'

export interface Geofence {
  id: string
  tenant_id: string
  scope: GeofenceScope
  scope_id?: string
  mode: GeofenceMode
  country_codes?: string[]
  cidr_allowlist?: string[]
  cidr_denylist?: string[]
  apply_to: GeofenceAction
  enabled: boolean
  created_by_user_id?: string
  created_at: string
  updated_at: string
}

export interface GeofenceDryRunResult {
  allow: boolean
  require_step_up: boolean
  reason: string
  matched_policy_id?: string
}

export async function listGeofences() {
  const { data } = await api.get<{ geofences: Geofence[] }>('/admin/geofences')
  return data.geofences ?? []
}

export async function createGeofence(input: {
  scope: GeofenceScope
  scope_id?: string
  mode: GeofenceMode
  country_codes?: string[]
  cidr_allowlist?: string[]
  cidr_denylist?: string[]
  apply_to?: GeofenceAction
  enabled?: boolean
}) {
  const { data } = await api.post<Geofence>('/admin/geofences', input)
  return data
}

export async function deleteGeofence(id: string) {
  await api.delete(`/admin/geofences/${id}`)
}

export async function dryRunGeofence(input: {
  ip: string
  action?: GeofenceAction
  workspace_id?: string
  document_id?: string
}) {
  const { data } = await api.post<GeofenceDryRunResult>('/admin/geofences/test', input)
  return data
}
