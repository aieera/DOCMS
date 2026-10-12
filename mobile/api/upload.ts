import * as FileSystem from 'expo-file-system'

import { api } from './client'

// The full document-ingestion flow, mirroring web/src/hooks/useUpload.ts so a
// mobile scan lands identically: initiate upload → PUT bytes → complete (scan +
// persist blob) → create doc → create version. The version write fires
// dms.version.uploaded.v1 → OCR + classify + index (BACKEND: none — reused).
//
// W-2: the document row is created only AFTER the bytes are stored, so a
// refused or failed upload leaves nothing behind. Storage never hands back
// an existing blob at initiate (DEC-12), so the PUT is unconditional.

export interface UploadSession {
  upload_id: string
  presigned_put_url: string
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
  workspace_id?: string
  folder_id?: string
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
//
// Resumable (W-2): `resume` carries the blob / document a previous attempt
// already produced, and `onBlob` / `onDocument` let the offline queue persist
// each step as it completes — a replay after a late failure then neither
// re-uploads the bytes nor creates a second document.
export async function uploadScannedPdf(opts: {
  fileUri: string
  filename: string
  workspaceId: string
  folderId: string
  sizeBytes: number
  onProgress?: (fraction: number) => void
  resume?: { contentBlobId?: string | null; documentId?: string | null }
  onBlob?: (contentBlobId: string) => Promise<void> | void
  onDocument?: (documentId: string) => Promise<void> | void
}): Promise<{ documentId: string }> {
  const { fileUri, filename, workspaceId, folderId, sizeBytes, onProgress, resume } = opts
  onProgress?.(0.1)

  let blobId = resume?.contentBlobId || undefined
  if (!blobId) {
    const session = await initiateUpload({
      filename,
      mime_type: 'application/pdf',
      size_bytes: sizeBytes,
      workspace_id: workspaceId,
      folder_id: folderId,
    })
    if (!session.presigned_put_url) throw new Error('initiate returned no presigned URL')
    const put = await FileSystem.uploadAsync(session.presigned_put_url, fileUri, {
      httpMethod: 'PUT',
      uploadType: FileSystem.FileSystemUploadType.BINARY_CONTENT,
      headers: { 'Content-Type': 'application/pdf' },
    })
    onProgress?.(0.6)
    if (put.status < 200 || put.status >= 300) {
      throw new Error(`upload PUT failed: HTTP ${put.status}`)
    }
    const completion = await completeUpload(session.upload_id)
    blobId = completion.content_blob_id
    if (!blobId) throw new Error('storage did not return a content_blob_id')
    await opts.onBlob?.(blobId)
  }
  onProgress?.(0.75)

  let documentId = resume?.documentId || undefined
  if (!documentId) {
    const doc = await createDocument({ workspace_id: workspaceId, folder_id: folderId, title: filename })
    documentId = doc.id
    await opts.onDocument?.(documentId)
  }
  onProgress?.(0.9)

  await createVersion(documentId, blobId)
  onProgress?.(1)
  return { documentId }
}
