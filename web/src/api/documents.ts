import { api } from './client'
import { unwrapList } from '@/lib/unwrapList'
import type { Document, Version, PaginatedResponse } from '@/types/api'

// DuplicateMatch is an existing document whose content (by version
// sha256) matches a file about to be uploaded.
export interface DuplicateMatch {
  document_id: string
  title: string
  workspace_id: string
  folder_id?: string
  created_at: string
}

// findDuplicateDocuments asks the backend whether any existing document
// already holds this content hash, scoped to the target workspace. The
// server permission-filters the result to folders the caller can access.
export async function findDuplicateDocuments(
  sha256: string,
  workspaceId?: string,
): Promise<DuplicateMatch[]> {
  const { data } = await api.get<{ matches: DuplicateMatch[]; count: number }>(
    '/documents/duplicates',
    { params: { sha256, ...(workspaceId ? { workspace_id: workspaceId } : {}) } },
  )
  return data.matches ?? []
}

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

/** Create a note/wiki — a document (doc_type=note|wiki) with no file upload.
 *  Content is added later as a collaborative markdown version. Returns the
 *  new document (incl. doc_type) so the caller can navigate + open the
 *  editor. */
export async function createNote(input: {
  workspace_id: string
  folder_id: string
  title?: string
  doc_type?: 'note' | 'wiki'
}) {
  const { data } = await api.post<Document>('/notes', input)
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

// copyDocument creates a new document row in the target folder that
// shares the same current version + content blob as the source.
// Shallow copy at the version level: versions / share-links / comments
// / annotations / OCR / chunks stay with the source doc.
export async function copyDocument(id: string, folderId: string) {
  const { data } = await api.post<Document>(`/documents/${id}/copy`, { target_folder_id: folderId })
  return data
}

export async function getDownloadURL(documentId: string, versionId: string) {
  const { data } = await api.get<{ url: string; expires_at: string }>(
    `/storage/downloads/${documentId}/${versionId}`,
  )
  return data
}

export async function getVersions(documentId: string): Promise<Version[]> {
  // H-3 / Wave 5 pattern 3: grpc-gateway wraps ListVersionsResponse
  // as { versions: [...] } but legacy deploys returned a bare array.
  // unwrapList accepts both and throws UnknownListShapeError on any
  // other shape (e.g. an error envelope), routing the failure to
  // the consuming hook's isError branch instead of silently rendering
  // "no versions" on a malformed response.
  const { data } = await api.get<unknown>(`/documents/${documentId}/versions`)
  return unwrapList<Version>(data, 'versions')
}

export async function restoreVersion(documentId: string, versionId: string) {
  const { data } = await api.post(`/documents/${documentId}/versions/${versionId}/restore`)
  return data
}

// Phase 9 — named versions. Empty label clears any existing one.
// Backend enforces "edit" capability on the document; surfaces 403
// via the standard error envelope.
export async function setVersionLabel(documentId: string, versionId: string, label: string) {
  const { data } = await api.patch<{
    id: string
    document_id: string
    version_number: number
    label: string
  }>(`/documents/${documentId}/versions/${versionId}/label`, { label })
  return data
}

// Phase 5 — per-document retention exemption. Distinct from legal hold:
// holds are litigation-driven and freeze ALL lifecycle changes;
// exemption is a business waiver that just excludes the document from
// the retention sweep. Reason is required when exempt=true so the
// audit event (dms.document.retention_exempt_set.v1) carries the
// business justification an FRCP reviewer would expect.
export async function setRetentionExempt(documentId: string, exempt: boolean, reason?: string) {
  await api.post(`/documents/${documentId}/retention-exempt`, {
    exempt,
    reason: reason ?? '',
  })
}
