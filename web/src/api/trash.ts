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
