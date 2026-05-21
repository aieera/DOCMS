import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useUploadStore } from '@/store/uploadStore'
import { initiateUpload, uploadToPresigned, completeUpload } from '@/api/upload'
import { createDocument, createVersion, deleteDocument } from '@/api/documents'
import { getFolders, createFolder } from '@/api/workspaces'
import { sendFilingFeedback } from '@/api/predictiveFiling'
import type { FilingDecision } from '@/components/documents/FilingSuggestionPanel'
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

  // ADR 0102 — optional per-file filing decisions from
  // UploadReviewDialog. Same length & index as `files` when supplied;
  // a null entry means "no prediction available for this file, use
  // defaults". `useUpload` applies the decision to CreateDocument
  // (folder_id + tags) and POSTs sendFilingFeedback after a
  // successful upload so the training corpus grows on every batch.
  const uploadFiles = useCallback(async (
    files: File[],
    decisions?: (FilingDecision | null)[],
  ) => {
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
    for (let i = 0; i < files.length; i++) {
      const file = files[i]
      const id = crypto.randomUUID()
      addUpload({ id, file, progress: 0, status: 'pending' })

      // ADR 0102: pull the per-file decision (if any). The decision
      // can override target folder (finalFolderId) and stamp initial
      // tags (finalTags). Classification feeds the feedback row but
      // doesn't currently set a column on documents — Phase 2 wires a
      // `document_class` column once the intelligence service's
      // post-OCR refinement also writes there.
      const decision = decisions?.[i] ?? null
      const targetFolderId =
        decision?.folderAccepted && decision.finalFolderId
          ? decision.finalFolderId
          : resolvedFolderId
      const initialTags = decision?.finalTags ?? []

      // BUG-C2 rollback bookkeeping: track the document row we create
      // in step 1 so we can delete it if any later step throws. Without
      // this, a failed upload leaves an orphan row visible in the UI
      // with no content. The cleanup is best-effort — if delete fails
      // (network, race), we log but still surface the ORIGINAL upload
      // error to the user, not the cleanup error.
      let createdDocId: string | null = null

      try {
        // Step 1: create the document row.
        const doc = await createDocument({
          workspace_id: workspaceId,
          folder_id: targetFolderId,
          title: file.name,
          tags: initialTags,
        })
        createdDocId = doc.id

        // Step 2: get a presigned URL. BUG-C3: pass the SAME resolved
        // folder id that createDocument used (targetFolderId), not the
        // raw `folderId` prop. Otherwise the documents row and the
        // storage upload session can disagree on folder context when
        // either (a) `folderId` was undefined and we resolved to the
        // workspace root, or (b) a predictive-filing decision
        // overrode the user's selection.
        const session = await initiateUpload({
          filename: file.name,
          mime_type: file.type || 'application/octet-stream',
          size_bytes: file.size,
          workspace_id: workspaceId,
          folder_id: targetFolderId,
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

        // ADR 0102 — record the filing decision for the training
        // corpus. Best-effort: a feedback failure must not break the
        // upload UX, so we swallow the error and only log it.
        if (decision) {
          try {
            await sendFilingFeedback({
              prediction_id:         decision.predictionId,
              class_accepted:        decision.classAccepted,
              folder_accepted:       decision.folderAccepted,
              tags_accepted:         decision.tagsAccepted,
              tags_rejected:         decision.tagsRejected,
              final_class:           decision.finalClass,
              final_folder_id:       decision.finalFolderId,
              final_tags:            decision.finalTags,
              predicted_class:       decision.predictedClass,
              predicted_class_score: decision.predictedClassScore,
              predicted_folder_id:   decision.predictedFolderId,
              predicted_folder_score: decision.predictedFolderScore,
              predicted_tags:        decision.predictedTags,
              filename:              file.name,
              mime_type:             file.type || 'application/octet-stream',
              workspace_id:          workspaceId,
            })
          } catch (e) {
            console.warn('filing feedback failed', e)
          }
        }
      } catch (e) {
        // Detail comes from axios's response interceptor (toast already
        // surfaced the field error). Persist the underlying message on
        // the upload row so the user can see it after the toast fades.
        const detail =
          (e as { response?: { data?: { error?: string; message?: string } } }).response?.data?.error
          ?? (e as { response?: { data?: { error?: string; message?: string } } }).response?.data?.message
          ?? (e as Error).message
          ?? String(e)
        // BUG-C2: roll back the document row created in step 1 so the
        // workspace doesn't accumulate orphan rows ("No content"
        // badges) every time an upload fails partway. Best-effort:
        // we swallow cleanup errors and log to console so the toast
        // / status keeps surfacing the ORIGINAL upload failure — the
        // user cares about why their upload didn't go through, not
        // why our cleanup also didn't go through.
        if (createdDocId) {
          try {
            await deleteDocument(createdDocId)
          } catch (cleanupErr) {
            console.warn('upload rollback: deleteDocument failed', {
              documentId: createdDocId,
              originalError: detail,
              cleanupError: cleanupErr,
            })
          }
        }
        setStatus(id, 'failed', detail)
        toast.error(`${file.name} — ${detail}`)
      }
    }
  }, [workspaceId, folderId, addUpload, updateProgress, setStatus, qc])

  return { uploadFiles }
}
