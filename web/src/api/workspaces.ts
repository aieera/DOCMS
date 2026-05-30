import { api } from './client'
import { unwrapList } from '@/lib/unwrapList'
import type { Workspace, Folder } from '@/types/api'

// M-2 + Wave 5 pattern 3: dashboard cast was removed when this
// helper started returning Workspace[]; routing through unwrapList
// now gives the additional throw-on-unknown-shape guarantee shared
// with getVersions / listSessions / getFolders. Contract for
// existing callers (Workspace[]) is unchanged for the two supported
// shapes; malformed responses now reach react-query's isError
// branch instead of pretending zero workspaces.
export async function getWorkspaces(): Promise<Workspace[]> {
  const { data } = await api.get<unknown>('/workspaces')
  return unwrapList<Workspace>(data, 'workspaces')
}

export async function getWorkspace(id: string) {
  const { data } = await api.get<Workspace>(`/workspaces/${id}`)
  return data
}

export async function createWorkspace(name: string, description?: string) {
  const { data } = await api.post<Workspace>('/workspaces', { name, description })
  return data
}

export async function getFolders(workspaceId: string, parentId?: string): Promise<Folder[]> {
  // Wave 5 pattern 3 consolidation: same dual-shape + bonus
  // throw-on-unknown that getWorkspaces just adopted. Not named in
  // the audit but identical bug class — folding it in here keeps
  // the workspace caller graph internally consistent.
  const { data } = await api.get<unknown>(
    `/workspaces/${workspaceId}/folders`,
    { params: parentId ? { parent_folder_id: parentId } : {} },
  )
  return unwrapList<Folder>(data, 'folders')
}

export async function createFolder(
  workspaceId: string,
  name: string,
  parentId?: string,
  visibility?: 'shared' | 'private',
) {
  const { data } = await api.post<Folder>(`/workspaces/${workspaceId}/folders`, {
    name,
    parent_folder_id: parentId,
    visibility,
  })
  return data
}

export async function setFolderVisibility(
  folderId: string,
  visibility: 'shared' | 'private',
): Promise<Folder> {
  const { data } = await api.post<Folder>(`/folders/${folderId}/visibility`, { visibility })
  return data
}

export interface FolderGrant {
  id: string
  folder_id: string
  grantee_type: 'user' | 'group'
  grantee_id: string
  granted_by?: string
  created_at: string
}

export async function listFolderGrants(folderId: string): Promise<FolderGrant[]> {
  const { data } = await api.get<{ grants?: FolderGrant[] }>(`/folders/${folderId}/grants`)
  return data?.grants ?? []
}

export async function addFolderGrant(
  folderId: string,
  granteeType: 'user' | 'group',
  granteeId: string,
): Promise<FolderGrant> {
  const { data } = await api.post<FolderGrant>(`/folders/${folderId}/grants`, {
    grantee_type: granteeType,
    grantee_id: granteeId,
  })
  return data
}

export async function removeFolderGrant(
  folderId: string,
  granteeType: 'user' | 'group',
  granteeId: string,
): Promise<void> {
  await api.delete(`/folders/${folderId}/grants/${granteeType}/${granteeId}`)
}

export async function updateFolder(
  folderId: string,
  input: { name?: string; new_parent_folder_id?: string },
) {
  const { data } = await api.patch<Folder>(`/folders/${folderId}`, input)
  return data
}

export async function deleteFolder(folderId: string) {
  await api.delete(`/folders/${folderId}`)
}

// FolderDetail extends Folder with the ancestor chain the backend
// pre-loads on a single GetFolder call. Used by the workspace view's
// breadcrumb so we don't N+1 walk parents one at a time.
export interface FolderDetail extends Folder {
  ancestors?: Folder[]
}

export async function getFolder(folderId: string): Promise<FolderDetail> {
  const { data } = await api.get<FolderDetail>(`/folders/${folderId}`)
  return data
}

export async function updateWorkspace(id: string, input: { name?: string; description?: string }) {
  const { data } = await api.patch<Workspace>(`/workspaces/${id}`, input)
  return data
}

export async function deleteWorkspace(id: string) {
  await api.delete(`/workspaces/${id}`)
}

// Phase 3 — transfer the workspace's created_by (the canonical single-
// owner field). Backend gates on tenant role=owner OR current creator
// and requires the new owner be an active member.
// Returns the resolved (workspace_id, created_by) pair so callers can
// invalidate the workspace query without a follow-up GET.
export interface TransferWorkspaceOwnershipResponse {
  workspace_id: string
  created_by: string
}

export async function transferWorkspaceOwnership(
  workspaceId: string,
  newOwnerId: string,
): Promise<TransferWorkspaceOwnershipResponse> {
  const { data } = await api.post<TransferWorkspaceOwnershipResponse>(
    `/workspaces/${workspaceId}/transfer-ownership`,
    { new_owner_id: newOwnerId },
  )
  return data
}
