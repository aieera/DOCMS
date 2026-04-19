import { api } from './client'

export interface Permission {
  id: string
  resource_type: string
  resource_id: string
  principal_type: string
  principal_id: string
  capability: string
  granted_by: string
  granted_at: string
  expires_at?: string | null
}

export async function getPermissions(resourceType: string, resourceId: string) {
  const { data } = await api.get<Permission[]>(`/permissions/${resourceType}/${resourceId}`)
  return data
}

export async function checkPermission(action: string, resourceType: string, resourceId: string) {
  const { data } = await api.post<{ allowed: boolean; reason?: string }>(
    '/permissions/check',
    { action, resource_type: resourceType, resource_id: resourceId },
  )
  return data.allowed
}

export async function grantPermission(
  resourceType: string,
  resourceId: string,
  principalType: 'user' | 'group',
  principalId: string,
  capability: 'view' | 'share' | 'edit' | 'delete' | 'admin',
  expiresAt?: string,
) {
  const { data } = await api.post<Permission>(`/permissions/${resourceType}/${resourceId}`, {
    principal_type: principalType,
    principal_id: principalId,
    capability,
    expires_at: expiresAt,
  })
  return data
}

export async function revokePermission(resourceType: string, resourceId: string, principalId: string) {
  await api.delete(`/permissions/${resourceType}/${resourceId}/${principalId}`)
}
