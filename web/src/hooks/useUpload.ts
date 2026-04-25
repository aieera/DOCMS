// Upload orchestration. Three enhancements over the minimal flow:
//
//   1. MIME-mismatch surface — initiate/complete can return 409 with type
//      MIME_MISMATCH. We catch it, flip to status 'mime_mismatch', and the
//      UploadProgress row renders inline copy + help-article link.
//
//   2. Scan-pending retry — complete can return 202 / 503 with
//      SCAN_PENDING. We poll /storage/uploads/:id/complete at 2 s intervals.
//      A spinner appears past 10 s; at 120 s we give up and mark failed
//      with a clear error.
//
//   3. 413 PAYLOAD_TOO_LARGE from initiate surfaces as a dedicated toast
//      citing the tenant tier (backend names it in the error message).

import { useCallback } from 'react'
import { useUploadStore } from '@/store/uploadStore'
import { initiateUpload, uploadToPresigned, completeUpload } from '@/api/upload'
import toast from 'react-hot-toast'
import axios from 'axios'

const SCAN_POLL_INTERVAL_MS = 2_000
const SCAN_SPINNER_DELAY_MS = 10_000
const SCAN_MAX_WAIT_MS = 120_000

export function useUpload(workspaceId?: string, folderId?: string) {
  const { addUpload, updateProgress, setStatus, markScanning } = useUploadStore()

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
        markScanning(id)
        await completeWithRetry(session.upload_id)
        setStatus(id, 'completed')
        toast.success(`${file.name} uploaded`)
      } catch (e) {
        handleUploadError(id, file.name, e, setStatus)
      }
    }
  }, [workspaceId, folderId, addUpload, updateProgress, setStatus, markScanning])

  return { uploadFiles }
}

// completeWithRetry calls /complete until the backend returns a terminal
// response. scan_pending (202/503 with code SCAN_PENDING) is non-terminal;
// everything else — ok, quarantined, mime_mismatch, payload_too_large —
// is terminal and either resolves or throws.
async function completeWithRetry(uploadId: string) {
  const deadline = Date.now() + SCAN_MAX_WAIT_MS
  for (;;) {
    try {
      return await completeUpload(uploadId)
    } catch (e) {
      if (!isScanPending(e)) throw e
      if (Date.now() > deadline) {
        throw new Error('Scan did not complete within 2 minutes. Try re-uploading.')
      }
      await sleep(SCAN_POLL_INTERVAL_MS)
    }
  }
}

function handleUploadError(
  id: string,
  filename: string,
  e: unknown,
  setStatus: (id: string, status: 'failed' | 'mime_mismatch', error?: string) => void,
) {
  if (isMimeMismatch(e)) {
    setStatus(id, 'mime_mismatch', mimeMismatchMessage(e))
    toast.error(`${filename}: file type doesn't match extension`)
    return
  }
  if (isPayloadTooLarge(e)) {
    const msg = extractMessage(e) ?? 'File exceeds your plan upload limit'
    setStatus(id, 'failed', msg)
    toast.error(`${filename}: ${msg}`)
    return
  }
  const msg = extractMessage(e) ?? String(e)
  setStatus(id, 'failed', msg)
  toast.error(`${filename} upload failed: ${msg}`)
}

function isScanPending(e: unknown): boolean {
  if (!axios.isAxiosError(e)) return false
  const body = e.response?.data as { type?: string } | undefined
  if (body?.type === 'SCAN_PENDING') return true
  // Legacy / transport shape: 202 Accepted with a retry-after header.
  return e.response?.status === 202
}

function isMimeMismatch(e: unknown): boolean {
  if (!axios.isAxiosError(e)) return false
  const body = e.response?.data as { type?: string } | undefined
  return body?.type === 'MIME_MISMATCH'
}

function isPayloadTooLarge(e: unknown): boolean {
  if (!axios.isAxiosError(e)) return false
  return e.response?.status === 413
}

function mimeMismatchMessage(e: unknown): string {
  if (!axios.isAxiosError(e)) return "File type doesn't match extension"
  const body = e.response?.data as { message?: string; detected_mime?: string; declared_mime?: string } | undefined
  if (body?.detected_mime && body?.declared_mime) {
    return `File type doesn't match extension (declared ${body.declared_mime}, detected ${body.detected_mime})`
  }
  return body?.message ?? "File type doesn't match extension"
}

function extractMessage(e: unknown): string | null {
  if (!axios.isAxiosError(e)) return null
  const body = e.response?.data as { message?: string } | undefined
  return body?.message ?? null
}

function sleep(ms: number) {
  return new Promise<void>((res) => setTimeout(res, ms))
}

export const __testables = { SCAN_POLL_INTERVAL_MS, SCAN_SPINNER_DELAY_MS, SCAN_MAX_WAIT_MS }
