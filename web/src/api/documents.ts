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

// CreateDocument + CreateVersion are the two halves of the full upload
// flow that the storage uploads/{initiate,complete} pair doesn't cover:
// storage owns the bytes, document owns the row that ties bytes to a
// workspace. Without these calls a file lands in MinIO with no
// documents row pointing at it — invisible in the UI.

export async function createDocument(input: {
  workspace_id: string
  folder_id?: string
  title: string
  description?: string
  region_pin?: string
  tags?: string[]
}) {
  const { data } = await api.post<Document>('/documents', input)
  return data
}

export async function createVersion(input: {
  document_id: string
  content_blob_id: string
  change_summary?: string
}) {
  const { data } = await api.post<Version>(
    `/documents/${input.document_id}/versions`,
    {
      content_blob_id: input.content_blob_id,
      change_summary: input.change_summary,
    },
  )
  return data
}

export async function updateDocument(id: string, body: Partial<Document>) {
  const { data } = await api.patch<Document>(`/documents/${id}`, body)
  return data
}

export async function deleteDocument(id: string) {
  await api.delete(`/documents/${id}`)
}

export async function moveDocument(id: string, folderId: string) {
  const { data } = await api.post<Document>(`/documents/${id}/move`, { folder_id: folderId })
  return data
}

export async function getDownloadURL(documentId: string, versionId: string) {
  const { data } = await api.get<{ url: string; expires_at: string }>(
    `/storage/downloads/${documentId}/${versionId}`,
  )
  return data
}

export async function getVersions(documentId: string): Promise<Version[]> {
  // The grpc-gateway translates ListVersionsResponse.versions into a
  // top-level `versions` key — NOT a bare array. Earlier callers
  // assumed bare-array and broke .map() at runtime. Unwrap once here
  // so every consumer can rely on Version[].
  const { data } = await api.get<{ versions?: Version[] } | Version[]>(
    `/documents/${documentId}/versions`,
  )
  if (Array.isArray(data)) return data
  return data?.versions ?? []
}

export async function restoreVersion(documentId: string, versionId: string) {
  const { data } = await api.post(`/documents/${documentId}/versions/${versionId}/restore`)
  return data
}
