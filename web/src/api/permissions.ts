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

// The caller's own active grants — direct + via group membership.
// Self-scoped (no admin needed); powers the "shared with me" surfaces.
export async function getMyGrants() {
  const { data } = await api.get<Permission[]>('/permissions/mine')
  return Array.isArray(data) ? data : []
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

export interface EffectivePrincipal {
  principal_type: 'user' | 'group'
  principal_id: string
  capability: string
  reasons: string[]
}

export interface EffectiveAccess {
  principals: EffectivePrincipal[]
  org_admins_have_access: boolean
  workspace_baseline_view_workspace_id?: string
  private_folder: boolean
  folder_owner_id?: string
}

// getEffectiveAccess answers "who can see this, and why" for a resource,
// composing the same cascade the authorization check uses. Pass the
// cascade context (workspace/folder) the caller already knows.
export async function getEffectiveAccess(
  resourceType: string,
  resourceId: string,
  opts: { workspaceId?: string; folderId?: string } = {},
) {
  const params: Record<string, string> = {}
  if (opts.workspaceId) params.workspace_id = opts.workspaceId
  if (opts.folderId) params.folder_id = opts.folderId
  const { data } = await api.get<EffectiveAccess>(
    `/permissions/${resourceType}/${resourceId}/effective`,
    { params },
  )
  return data
}
