// ADR 0062 — LDAP/AD direct bind admin client. Mirrors the
// /api/v1/admin/ldap/* routes mounted by the auth service. Bind
// passwords are never echoed back from the server (the DTO carries
// `has_bind_password: boolean` instead).

import { api } from './client'

export interface LDAPConfig {
  id: string
  url: string
  use_starttls: boolean
  allow_insecure: boolean
  bind_dn: string
  user_search_base: string
  user_search_filter: string
  email_attribute: string
  display_name_attribute: string
  group_search_base: string
  group_search_filter: string
  nested_groups: boolean
  fallback_to_local: boolean
  is_active: boolean
  has_bind_password: boolean
  last_sync_at?: string
  last_sync_status?: string
  last_sync_error?: string
  created_at: string
  updated_at: string
}

// Server accepts a write body that's a strict superset of LDAPConfig
// minus the read-only fields, plus an optional bind_password.
export interface LDAPWriteBody {
  url: string
  use_starttls?: boolean
  allow_insecure?: boolean
  bind_dn: string
  bind_password?: string // omit on PATCH to keep the existing one
  user_search_base: string
  user_search_filter: string
  email_attribute?: string
  display_name_attribute?: string
  group_search_base: string
  group_search_filter: string
  nested_groups?: boolean
  fallback_to_local?: boolean
  is_active?: boolean
}

export interface LDAPTestBindResult {
  ok: boolean
  bind_ok: boolean
  user_found: boolean
  groups_found: number
  warnings?: string[]
  error?: string
}

export interface LDAPMapping {
  ldap_group_dn: string
  dms_group_id: string
  /** Optional SeDoc role (owner|admin|member|guest) this AD group grants. */
  dms_role?: string
}

export interface LDAPSyncHistoryRow {
  id: string
  trigger: string
  started_at: string
  finished_at?: string
  status: string
  users_synced: number
  groups_synced: number
  errors: number
  error_summary?: string
}

// ---- Configs --------------------------------------------------------------

export async function getActiveLDAPConfig(): Promise<LDAPConfig | null> {
  try {
    const { data } = await api.get<LDAPConfig>('/admin/ldap/config')
    return data
  } catch (err: unknown) {
    // 404 here means "no config saved yet" — silent null is the
    // contract the caller expects. Anything else bubbles.
    const status = (err as { response?: { status?: number } } | null)?.response?.status
    if (status === 404) return null
    throw err
  }
}

export async function listLDAPConfigs(): Promise<LDAPConfig[]> {
  const { data } = await api.get<LDAPConfig[]>('/admin/ldap/configs')
  return data ?? []
}

export async function createLDAPConfig(body: LDAPWriteBody): Promise<{ id: string }> {
  const { data } = await api.post<{ id: string }>('/admin/ldap/configs', body)
  return data
}

export async function updateLDAPConfig(id: string, body: Partial<LDAPWriteBody>): Promise<void> {
  await api.patch(`/admin/ldap/configs/${id}`, body)
}

export async function deleteLDAPConfig(id: string): Promise<void> {
  await api.delete(`/admin/ldap/configs/${id}`)
}

// ---- Test bind ------------------------------------------------------------

export async function testLDAPBind(input: {
  existing_config_id?: string
  draft?: LDAPWriteBody
  sample_username?: string
  sample_password?: string
}): Promise<LDAPTestBindResult> {
  const { data } = await api.post<LDAPTestBindResult>('/admin/ldap/test-bind', input)
  return data
}

// ---- Group mappings -------------------------------------------------------

export async function listLDAPMappings(configId: string): Promise<LDAPMapping[]> {
  const { data } = await api.get<LDAPMapping[]>(`/admin/ldap/configs/${configId}/mappings`)
  return data ?? []
}

export async function addLDAPMapping(
  configId: string,
  ldap_group_dn: string,
  dms_group_id: string,
  dms_role?: string,
): Promise<void> {
  await api.post(`/admin/ldap/configs/${configId}/mappings`, { ldap_group_dn, dms_group_id, dms_role })
}

export async function deleteLDAPMapping(
  configId: string,
  ldap_group_dn: string,
  dms_group_id: string,
): Promise<void> {
  await api.delete(`/admin/ldap/configs/${configId}/mappings`, {
    data: { ldap_group_dn, dms_group_id },
  })
}

// ---- Sync history & manual trigger ----------------------------------------

export async function listLDAPHistory(configId: string): Promise<LDAPSyncHistoryRow[]> {
  const { data } = await api.get<LDAPSyncHistoryRow[]>(`/admin/ldap/configs/${configId}/history`)
  return data ?? []
}

export async function syncLDAPNow(configId: string): Promise<void> {
  await api.post(`/admin/ldap/configs/${configId}/sync`)
}
