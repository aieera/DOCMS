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

export async function createFolder(workspaceId: string, name: string, parentId?: string) {
  const { data } = await api.post<Folder>(`/workspaces/${workspaceId}/folders`, { name, parent_folder_id: parentId })
  return data
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
