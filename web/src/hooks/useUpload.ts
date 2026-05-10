import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useUploadStore } from '@/store/uploadStore'
import { initiateUpload, uploadToPresigned, completeUpload } from '@/api/upload'
import { createDocument, createVersion } from '@/api/documents'
import { getFolders, createFolder } from '@/api/workspaces'
import { toast } from 'sonner'

// Full upload flow:
//   1. CreateDocument          → documents row (no content yet)
//   2. InitiateUpload          → upload_session + presigned PUT URL
//   3. PUT to presigned URL    → bytes land in MinIO
//   4. CompleteUpload          → content_blob row + scan + return blob_id
//   5. CreateVersion           → links blob_id to documents row
//
// Steps 1 + 5 used to be missing — file landed in MinIO with no row in
// documents table → invisible in the UI's ListDocuments query. Adding
// them here completes the chain. Default title is the filename;
// description blank; tags empty. The UI can grow a "rename / metadata"
// pre-upload dialog later.
export function useUpload(workspaceId?: string, folderId?: string) {
  const { addUpload, updateProgress, setStatus } = useUploadStore()
  const qc = useQueryClient()

  const uploadFiles = useCallback(async (files: File[]) => {
    if (!workspaceId) {
      toast.error('Pick a workspace before uploading')
      return
    }
    // Resolve target folder once for the batch. The CreateDocument
    // endpoint requires a non-nil folder_id even at workspace root.
    // Workspaces created post backend-fix auto-get a "Root" folder;
    // legacy workspaces don't, so fall back to creating one if the
    // workspace has none yet. Caching at the batch level means we
    // don't query for every file in the batch.
    let resolvedFolderId = folderId
    if (!resolvedFolderId) {
      try {
        const folders = await getFolders(workspaceId)
        const folderList = folders as unknown as { id: string; parent_folder_id?: string | null; parent_id?: string | null }[]
        const root = folderList.find((f) => !f.parent_folder_id && !f.parent_id)
        if (root) {
          resolvedFolderId = root.id
        } else if (folderList.length === 0) {
          // Legacy workspace with no folders — auto-create the root
          // so the upload can proceed. Best-effort: if creation fails
          // (permission, race), we surface the original error from
          // createDocument below.
          try {
            const created = await createFolder(workspaceId, 'Root')
            resolvedFolderId = (created as unknown as { id: string }).id
          } catch {
            // fall through; createDocument will surface a clearer error
          }
        }
      } catch {
        // fall through; createDocument will surface a clearer error
      }
    }
    if (!resolvedFolderId) {
      toast.error('No folder available in this workspace — create one first or contact an admin.')
      return
    }
    for (const file of files) {
      const id = crypto.randomUUID()
      addUpload({ id, file, progress: 0, status: 'pending' })
      try {
        // Step 1: create the document row.
        const doc = await createDocument({
          workspace_id: workspaceId,
          folder_id: resolvedFolderId,
          title: file.name,
          tags: [],
        })

        // Step 2: get a presigned URL.
        const session = await initiateUpload({
          filename: file.name,
          mime_type: file.type || 'application/octet-stream',
          size_bytes: file.size,
          workspace_id: workspaceId,
          folder_id: folderId,
        })
        if (session.deduplicated) {
          // Deduplication still needs a version row pointing at the
          // existing blob — the storage server returns the existing
          // blob_id on a dedup hit.
          const dedupBlobId = (session as { content_blob_id?: string; existing_blob_id?: string }).content_blob_id
            ?? session.existing_blob_id
          if (dedupBlobId) {
            await createVersion({
              document_id: doc.id,
              content_blob_id: dedupBlobId,
              change_summary: 'initial',
            })
          }
          setStatus(id, 'completed')
          toast.success(`${file.name} — deduplicated, no upload needed`)
          await qc.invalidateQueries({ queryKey: ['documents', workspaceId] })
          continue
        }

        // Step 3: PUT bytes to MinIO.
        setStatus(id, 'uploading')
        await uploadToPresigned(
          session.presigned_put_url,
          file,
          (pct) => updateProgress(id, pct),
        )

        // Step 4: complete the upload (scan + persist blob).
        const completion = await completeUpload(session.upload_id)
        const blobID = (completion as { content_blob_id?: string } | null)?.content_blob_id
          ?? (session as { content_blob_id?: string }).content_blob_id
          ?? session.existing_blob_id
        if (!blobID) {
          throw new Error('storage did not return a content_blob_id')
        }

        // Step 5: link the blob to the document.
        await createVersion({
          document_id: doc.id,
          content_blob_id: blobID,
          change_summary: 'initial',
        })

        setStatus(id, 'completed')
        toast.success(`${file.name} uploaded`)
        await qc.invalidateQueries({ queryKey: ['documents', workspaceId] })
      } catch (e) {
        setStatus(id, 'failed', String(e))
        toast.error(`${file.name} upload failed`)
      }
    }
  }, [workspaceId, folderId, addUpload, updateProgress, setStatus, qc])

  return { uploadFiles }
}
