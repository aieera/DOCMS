import * as FileSystem from 'expo-file-system'

import { api } from './client'

// The full document-ingestion flow, mirroring web/src/hooks/useUpload.ts so a
// mobile scan lands identically: create doc → initiate upload → PUT bytes →
// complete (scan + persist blob) → create version. The version write fires
// dms.version.uploaded.v1 → OCR + classify + index (BACKEND: none — reused).

export interface UploadSession {
  upload_id: string
  presigned_put_url?: string
  // Storage returns an existing blob id on a dedup hit (and may omit the URL).
  content_blob_id?: string
  existing_blob_id?: string
}

export async function createDocument(input: {
  workspace_id: string
  folder_id: string
  title: string
  custom_metadata?: Record<string, unknown>
}): Promise<{ id: string }> {
  const { data } = await api.post('/documents', input)
  return data
}

export async function initiateUpload(params: {
  filename: string
  mime_type: string
  size_bytes: number
  sha256_hash?: string
}): Promise<UploadSession> {
  const { data } = await api.post('/storage/uploads/initiate', params)
  return data
}

export async function completeUpload(uploadId: string, sha256?: string): Promise<{ content_blob_id?: string }> {
  const { data } = await api.post(`/storage/uploads/${uploadId}/complete`, sha256 ? { sha256_hash: sha256 } : {})
  return data ?? {}
}

export async function createVersion(documentId: string, contentBlobId: string): Promise<void> {
  await api.post(`/documents/${documentId}/versions`, {
    content_blob_id: contentBlobId,
    change_summary: 'mobile scan',
  })
}

// uploadScannedPdf runs the whole flow for a local PDF file and returns the new
// document id. Bytes stream straight from the file (expo-file-system), never
// fully read into JS memory.
export async function uploadScannedPdf(opts: {
  fileUri: string
  filename: string
  workspaceId: string
  folderId: string
  sizeBytes: number
  onProgress?: (fraction: number) => void
}): Promise<{ documentId: string }> {
  const { fileUri, filename, workspaceId, folderId, sizeBytes, onProgress } = opts
  onProgress?.(0.1)
  const doc = await createDocument({ workspace_id: workspaceId, folder_id: folderId, title: filename })
  onProgress?.(0.25)

  const session = await initiateUpload({ filename, mime_type: 'application/pdf', size_bytes: sizeBytes })
  const dedupBlob = session.content_blob_id ?? session.existing_blob_id

  // Dedup fast-path: storage already holds these exact bytes → link directly.
  if (!session.presigned_put_url) {
    if (!dedupBlob) throw new Error('initiate returned neither a presigned URL nor a blob id')
    await createVersion(doc.id, dedupBlob)
    onProgress?.(1)
    return { documentId: doc.id }
  }

  const put = await FileSystem.uploadAsync(session.presigned_put_url, fileUri, {
    httpMethod: 'PUT',
    uploadType: FileSystem.FileSystemUploadType.BINARY_CONTENT,
    headers: { 'Content-Type': 'application/pdf' },
  })
  onProgress?.(0.7)
  if (put.status < 200 || put.status >= 300) {
    throw new Error(`upload PUT failed: HTTP ${put.status}`)
  }

  const completion = await completeUpload(session.upload_id)
  const blobId = completion.content_blob_id ?? dedupBlob
  if (!blobId) throw new Error('storage did not return a content_blob_id')
  await createVersion(doc.id, blobId)
  onProgress?.(1)
  return { documentId: doc.id }
}
