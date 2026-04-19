import { api } from './client'
import type { Workspace, Folder } from '@/types/api'

export async function getWorkspaces() {
  // Backend wraps in { workspaces: [...] } per ListWorkspacesResponse;
  // legacy shape was a raw array. Handle both until every deploy runs
  // the new backend.
  const { data } = await api.get<{ workspaces: Workspace[] } | Workspace[]>('/workspaces')
  return Array.isArray(data) ? data : (data.workspaces ?? [])
}

export async function getWorkspace(id: string) {
  const { data } = await api.get<Workspace>(`/workspaces/${id}`)
  return data
}

export async function createWorkspace(name: string, description?: string) {
  const { data } = await api.post<Workspace>('/workspaces', { name, description })
  return data
}

export async function getFolders(workspaceId: string, parentId?: string) {
  const { data } = await api.get<{ folders: Folder[] } | Folder[]>(
    `/workspaces/${workspaceId}/folders`,
    { params: parentId ? { parent_folder_id: parentId } : {} },
  )
  return Array.isArray(data) ? data : (data.folders ?? [])
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
