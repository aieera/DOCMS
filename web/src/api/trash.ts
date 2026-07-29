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
  deleted_by?: string
  deleted_by_name?: string
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

// purgeFolderFromTrash permanently deletes a trashed folder cohort:
// every folder + document soft-deleted in the same cascade, their
// blobs in object storage included. Fails 409 if any cohort document
// is under legal hold or active retention (fail-closed: nothing is
// deleted). Works for legacy no-cohort rows too (purges the deleted
// subtree — the only way to remove those).
export interface FolderPurgeResult {
  folders_deleted: number
  documents_deleted: number
}

export async function purgeFolderFromTrash(folderId: string): Promise<FolderPurgeResult> {
  const { data } = await api.delete<FolderPurgeResult>(`/admin/trash/folders/${folderId}`)
  return data
}

// emptyTrash purges everything purgeable in the tenant's trash in one
// call. Items blocked by legal hold / retention are skipped and
// reported, never fatal.
export interface EmptyTrashSkipped {
  id: string
  type: 'folder' | 'document'
  name?: string
  reason: string
}

export interface EmptyTrashResult {
  purged_folders: number
  purged_documents: number
  skipped: EmptyTrashSkipped[]
}

export async function emptyTrash(): Promise<EmptyTrashResult> {
  const { data } = await api.delete<EmptyTrashResult>('/admin/trash')
  return data
}

// ---- Empty-folder cleanup (admin maintenance) -----------------------
// Removes the orphaned empty folders that accumulate from aborted
// ingests / integration runs. Deletions are soft (land in Trash above,
// restorable) and audited.

export interface EmptyFolder {
  id: string
  name: string
  workspace_id: string
  path: string
  depth: number
  created_at: string
}

export interface EmptyFolderScan {
  items: EmptyFolder[]
  count: number
  truncated: boolean
}

// listEmptyFolders is the dry-run: what WOULD be removed.
export async function listEmptyFolders(limit = 200): Promise<EmptyFolderScan> {
  const { data } = await api.get<EmptyFolderScan>('/admin/folders/empty', {
    params: { limit: String(limit) },
  })
  return data
}

export interface EmptyFolderCleanupResult {
  deleted: number
  more_remaining: boolean
  items: EmptyFolder[]
}

export async function cleanupEmptyFolders(max = 2000): Promise<EmptyFolderCleanupResult> {
  const { data } = await api.post<EmptyFolderCleanupResult>('/admin/folders/cleanup', { max })
  return data
}
