import { useCallback } from 'react'
import { useUploadStore } from '@/store/uploadStore'
import { initiateUpload, uploadToPresigned, completeUpload } from '@/api/upload'
import toast from 'react-hot-toast'

export function useUpload(workspaceId?: string, folderId?: string) {
  const { addUpload, updateProgress, setStatus } = useUploadStore()

  const uploadFiles = useCallback(async (files: File[]) => {
    for (const file of files) {
      const id = crypto.randomUUID()
      addUpload({ id, file, progress: 0, status: 'pending' })
      try {
        const session = await initiateUpload({
          filename: file.name,
          mime_type: file.type || 'application/octet-stream',
          size_bytes: file.size,
          workspace_id: workspaceId,
          folder_id: folderId,
        })
        if (session.deduplicated) {
          setStatus(id, 'completed')
          toast.success(`${file.name} — deduplicated, no upload needed`)
          continue
        }
        setStatus(id, 'uploading')
        await uploadToPresigned(session.presigned_put_url, file, (pct) => updateProgress(id, pct))
        await completeUpload(session.upload_id)
        setStatus(id, 'completed')
        toast.success(`${file.name} uploaded`)
      } catch (e) {
        setStatus(id, 'failed', String(e))
        toast.error(`${file.name} upload failed`)
      }
    }
  }, [workspaceId, folderId, addUpload, updateProgress, setStatus])

  return { uploadFiles }
}
