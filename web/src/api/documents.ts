import { api } from './client'
import type { Document, Version, PaginatedResponse } from '@/types/api'

export async function getDocuments(workspaceId: string, params: Record<string, string> = {}) {
  const { data } = await api.get<PaginatedResponse<Document>>(
    `/workspaces/${workspaceId}/documents`,
    { params },
  )
  return data
}

export async function getDocument(id: string) {
  const { data } = await api.get<Document>(`/documents/${id}`)
  return data
}

export async function updateDocument(id: string, body: Partial<Document>) {
  const { data } = await api.patch<Document>(`/documents/${id}`, body)
  return data
}

export async function deleteDocument(id: string) {
  await api.delete(`/documents/${id}`)
}

// GAP-4 — soft-delete trash + restore. Admin/owner-gated server-side;
// the client sends the same headers it does for /admin/* surfaces.
export interface TrashItem {
  id: string
  title: string
  workspace_id: string
  workspace_name?: string
  folder_id?: string
  mime_type?: string
  size_bytes: number
  deleted_at: string
  created_by?: string
}

export async function listTrash(): Promise<TrashItem[]> {
  const { data } = await api.get<{ items: TrashItem[] }>('/admin/documents/trash')
  return data.items ?? []
}

export async function restoreDocument(id: string) {
  await api.post(`/admin/documents/${id}/restore`)
}

export async function moveDocument(id: string, folderId: string) {
  const { data } = await api.post<Document>(`/documents/${id}/move`, { folder_id: folderId })
  return data
}

export async function getVersions(documentId: string) {
  const { data } = await api.get<Version[]>(`/documents/${documentId}/versions`)
  return data
}

export async function restoreVersion(documentId: string, versionId: string) {
  const { data } = await api.post(`/documents/${documentId}/versions/${versionId}/restore`)
  return data
}
