import { initiateUpload, uploadToPresigned, completeUpload } from '@/api/upload'
import { createDocument, createVersion } from '@/api/documents'

/**
 * Upload one file as a new document under (workspaceId, folderId), reusing the
 * standard pipeline: createDocument → initiate → (dedup ? link : PUT + complete)
 * → createVersion. Because it goes through the same endpoints as a normal single
 * upload, virus scan / OCR / dedup / region_pin all apply automatically.
 */
export async function uploadFileToFolder(args: {
  file: File
  title: string
  workspaceId: string
  folderId?: string
  onProgress?: (pct: number) => void
}): Promise<{ documentId: string; deduplicated: boolean }> {
  const { file, title, workspaceId, folderId, onProgress } = args

  const doc = await createDocument({ workspace_id: workspaceId, folder_id: folderId, title })

  const session = await initiateUpload({
    filename: file.name,
    mime_type: file.type || 'application/octet-stream',
    size_bytes: file.size,
    workspace_id: workspaceId,
    folder_id: folderId,
  })

  const dedupBlob =
    (session as { content_blob_id?: string }).content_blob_id ?? session.existing_blob_id
  if (session.deduplicated && dedupBlob) {
    await createVersion({ document_id: doc.id, content_blob_id: dedupBlob, change_summary: 'initial' })
    return { documentId: doc.id, deduplicated: true }
  }

  await uploadToPresigned(session.presigned_put_url, file, onProgress)
  const completion = await completeUpload(session.upload_id)
  const blobId =
    (completion as { content_blob_id?: string } | null)?.content_blob_id ??
    (session as { content_blob_id?: string }).content_blob_id ??
    session.existing_blob_id
  if (!blobId) throw new Error('storage did not return a content_blob_id')

  await createVersion({ document_id: doc.id, content_blob_id: blobId, change_summary: 'initial' })
  return { documentId: doc.id, deduplicated: false }
}
