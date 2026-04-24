// Control-plane Tenants API. Internal endpoints live under
// /internal/v1/tenants and require an X-API-Key. The gateway
// injects this header for admin-role callers; the browser client
// assumes that path.

import { api } from './client'

export interface Tenant {
  id: string
  name: string
  slug: string
  plan: string
  primary_region: string
  created_at: string
  deleted_at: string | null
  dispose_scheduled_at: string | null
  disposed_at: string | null
}

export async function listTenants(): Promise<Tenant[]> {
  const { data } = await api.get<Tenant[]>('/internal/v1/tenants')
  return data ?? []
}

export async function deprovisionTenant(id: string): Promise<void> {
  await api.post(`/internal/v1/tenants/${id}/deprovision`)
}

export async function undoDeprovision(id: string): Promise<void> {
  await api.post(`/internal/v1/tenants/${id}/undo-deprovision`)
}
