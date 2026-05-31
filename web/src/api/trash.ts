import { api } from './client'

export interface TrashEntry {
  id: string
  title: string
  workspace_id: string
  folder_id?: string
  mime_type?: string
  total_size_bytes: number
  lifecycle_state: string
  created_by?: string
  created_by_name?: string
  deleted_at?: string
}

export interface TrashListResponse {
  items: TrashEntry[]
  next_page_token?: string
}

export async function listTrash(pageToken?: string, pageSize = 50): Promise<TrashListResponse> {
  const params: Record<string, string> = {}
  if (pageToken) params.page_token = pageToken
  if (pageSize) params.page_size = String(pageSize)
  const { data } = await api.get<TrashListResponse>('/admin/trash', { params })
  return data
}

export async function restoreFromTrash(documentId: string): Promise<void> {
  await api.post(`/admin/trash/${documentId}/restore`)
}

export async function purgeFromTrash(documentId: string): Promise<void> {
  await api.delete(`/admin/trash/${documentId}`)
}

// TrashedFolder — soft-deleted folder row for the Trash page. The
// backend returns only cohort-root folders (descendants share the
// cohort id and come back automatically when the root is restored),
// so each row represents a single restorable cascade.
//
// `restorable` is false for legacy soft-deletes that have no cohort
// id (pre-FIX-5). Those rows render with the Restore button hidden.
export interface TrashedFolder {
  id: string
  name: string
  workspace_id: string
  workspace_name: string
  visibility: 'shared' | 'private'
  deleted_by?: string
  deleted_at?: string
  cohort_docs: number
  restorable: boolean
}

export async function listTrashedFolders(): Promise<TrashedFolder[]> {
  const { data } = await api.get<{ items?: TrashedFolder[] }>('/admin/trash/folders')
  return data?.items ?? []
}

// restoreFolderFromTrash hits the cohort-scoped restore endpoint
// added by FIX-5. Returns 204 on success; 404 means the folder is
// no longer deleted (someone else restored it) or never had a
// cohort id.
export async function restoreFolderFromTrash(folderId: string): Promise<void> {
  await api.post(`/folders/${folderId}/restore`)
}
