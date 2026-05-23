// useUpload — regression tests for BUG-C2 (rollback on failure) and
// BUG-C3 (folder-id consistency between createDocument + initiateUpload).
//
// We mock @/api/documents and @/api/upload directly rather than spinning
// up MSW handlers for every step of the 5-step flow — the contract we
// care about here is "the hook calls the right API in the right shape
// in the right order", not the wire format (which the api/* modules
// already test).

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

// --- mocks ------------------------------------------------------------

const createDocument = vi.fn()
const createVersion = vi.fn()
const deleteDocument = vi.fn()
const initiateUpload = vi.fn()
const uploadToPresigned = vi.fn()
const completeUpload = vi.fn()
const getFolders = vi.fn()
const createFolder = vi.fn()
const sendFilingFeedback = vi.fn()

vi.mock('@/api/documents', () => ({
  createDocument: (...args: unknown[]) => createDocument(...args),
  createVersion: (...args: unknown[]) => createVersion(...args),
  deleteDocument: (...args: unknown[]) => deleteDocument(...args),
}))
vi.mock('@/api/upload', () => ({
  initiateUpload: (...args: unknown[]) => initiateUpload(...args),
  uploadToPresigned: (...args: unknown[]) => uploadToPresigned(...args),
  completeUpload: (...args: unknown[]) => completeUpload(...args),
}))
vi.mock('@/api/workspaces', () => ({
  getFolders: (...args: unknown[]) => getFolders(...args),
  createFolder: (...args: unknown[]) => createFolder(...args),
}))
vi.mock('@/api/predictiveFiling', () => ({
  sendFilingFeedback: (...args: unknown[]) => sendFilingFeedback(...args),
}))
vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

// The hook reads from + writes to the upload store. We don't care about
// the store contents in these tests, but we do need the actions to be
// safe to call.
vi.mock('@/store/uploadStore', () => ({
  useUploadStore: () => ({
    addUpload: vi.fn(),
    updateProgress: vi.fn(),
    setStatus: vi.fn(),
  }),
}))

// Import AFTER the mocks are registered so the hook sees them.
import { useUpload, preflightFile } from '@/hooks/useUpload'

function wrap({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

beforeEach(() => {
  createDocument.mockReset()
  createVersion.mockReset()
  deleteDocument.mockReset()
  initiateUpload.mockReset()
  uploadToPresigned.mockReset()
  completeUpload.mockReset()
  getFolders.mockReset()
  createFolder.mockReset()
  sendFilingFeedback.mockReset()
})

// --- preflightFile unit tests ----------------------------------------

describe('preflightFile', () => {
  it('returns null for a valid PDF', () => {
    const file = new File(['pdf content'], 'report.pdf', { type: 'application/pdf' })
    expect(preflightFile(file)).toBeNull()
  })

  it('rejects a file with a blocked MIME type', () => {
    const file = new File(['data'], 'setup.exe', { type: 'application/x-msdownload' })
    expect(preflightFile(file)).toMatch(/executable file types/i)
  })

  it('rejects a .ps1 script even when the MIME type is blank', () => {
    const file = new File([''], 'deploy.ps1')
    expect(preflightFile(file)).toMatch(/\.ps1.*is not permitted/i)
  })

  it('rejects a .exe file regardless of declared MIME type', () => {
    const file = new File([''], 'trojan.exe', { type: 'application/octet-stream' })
    expect(preflightFile(file)).toMatch(/\.exe.*is not permitted/i)
  })

  it('rejects a file exceeding 5 GiB', () => {
    const oversized = { name: 'huge.iso', size: 6 * 1024 * 1024 * 1024, type: 'application/octet-stream' } as File
    expect(preflightFile(oversized)).toMatch(/5 GiB/i)
  })
})

// --- preflight integration: blocked files don't reach the server -----

describe('useUpload — preflight', () => {
  it('skips createDocument for a blocked MIME file', async () => {
    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([
        new File(['hack'], 'exploit.exe', { type: 'application/x-msdownload' }),
      ])
    })

    expect(createDocument).not.toHaveBeenCalled()
  })

  it('skips createDocument for a blocked extension when MIME is blank', async () => {
    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([new File([''], 'script.ps1')])
    })

    expect(createDocument).not.toHaveBeenCalled()
  })

  it('continues uploading the valid file in a mixed batch after skipping the blocked one', async () => {
    createDocument.mockResolvedValue({ id: 'doc-ok' })
    initiateUpload.mockResolvedValue({
      upload_id: 'up-1',
      presigned_put_url: 'https://example.test/put',
    })
    uploadToPresigned.mockResolvedValue(undefined)
    completeUpload.mockResolvedValue({ content_blob_id: 'blob-1' })
    createVersion.mockResolvedValue({ id: 'ver-1' })

    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([
        new File(['bad'], 'bad.exe', { type: 'application/x-msdownload' }),
        new File(['ok'], 'report.pdf', { type: 'application/pdf' }),
      ])
    })

    // Only the valid file should reach the document-creation step
    expect(createDocument).toHaveBeenCalledTimes(1)
  })
})

// --- BUG-C3: folder-id consistency -----------------------------------

describe('useUpload — BUG-C3 folder_id consistency', () => {
  it('passes the SAME folder_id to createDocument and initiateUpload', async () => {
    createDocument.mockResolvedValue({ id: 'doc-1' })
    initiateUpload.mockResolvedValue({
      upload_id: 'up-1',
      presigned_put_url: 'https://example.test/put',
    })
    uploadToPresigned.mockResolvedValue(undefined)
    completeUpload.mockResolvedValue({ content_blob_id: 'blob-1' })
    createVersion.mockResolvedValue({ id: 'ver-1' })

    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([new File(['hello'], 'a.txt', { type: 'text/plain' })])
    })

    expect(createDocument).toHaveBeenCalledTimes(1)
    expect(initiateUpload).toHaveBeenCalledTimes(1)
    const createFolderArg = createDocument.mock.calls[0][0].folder_id
    const initiateFolderArg = initiateUpload.mock.calls[0][0].folder_id
    expect(createFolderArg).toBe('folder-A')
    expect(initiateFolderArg).toBe('folder-A')
    expect(createFolderArg).toBe(initiateFolderArg)
  })

  it('uses the resolved root folder when no folderId prop is supplied (no desync to undefined)', async () => {
    getFolders.mockResolvedValue([{ id: 'root-folder' }])
    createDocument.mockResolvedValue({ id: 'doc-1' })
    initiateUpload.mockResolvedValue({
      upload_id: 'up-1',
      presigned_put_url: 'https://example.test/put',
    })
    uploadToPresigned.mockResolvedValue(undefined)
    completeUpload.mockResolvedValue({ content_blob_id: 'blob-1' })
    createVersion.mockResolvedValue({ id: 'ver-1' })

    const { result } = renderHook(
      () => useUpload('ws-1'), // folderId omitted
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([new File(['hello'], 'a.txt', { type: 'text/plain' })])
    })

    expect(createDocument).toHaveBeenCalled()
    expect(initiateUpload).toHaveBeenCalled()
    const createFolderArg = createDocument.mock.calls[0][0].folder_id
    const initiateFolderArg = initiateUpload.mock.calls[0][0].folder_id
    expect(createFolderArg).toBe('root-folder')
    expect(initiateFolderArg).toBe('root-folder')
  })
})

// --- BUG-C2: rollback on failure -------------------------------------

describe('useUpload — BUG-C2 rollback on failure', () => {
  it('calls deleteDocument with the new doc id when initiateUpload throws', async () => {
    createDocument.mockResolvedValue({ id: 'doc-2' })
    initiateUpload.mockRejectedValue(new Error('storage 500'))

    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([new File(['x'], 'b.txt', { type: 'text/plain' })])
    })

    expect(createDocument).toHaveBeenCalledTimes(1)
    expect(deleteDocument).toHaveBeenCalledTimes(1)
    expect(deleteDocument).toHaveBeenCalledWith('doc-2')
  })

  it('calls deleteDocument when uploadToPresigned throws', async () => {
    createDocument.mockResolvedValue({ id: 'doc-3' })
    initiateUpload.mockResolvedValue({
      upload_id: 'up-1',
      presigned_put_url: 'https://example.test/put',
    })
    uploadToPresigned.mockRejectedValue(new Error('network'))

    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([new File(['x'], 'c.txt', { type: 'text/plain' })])
    })

    expect(deleteDocument).toHaveBeenCalledWith('doc-3')
  })

  it('does NOT call deleteDocument when createDocument itself throws (nothing to roll back)', async () => {
    createDocument.mockRejectedValue(new Error('forbidden'))

    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([new File(['x'], 'd.txt', { type: 'text/plain' })])
    })

    expect(createDocument).toHaveBeenCalledTimes(1)
    expect(deleteDocument).not.toHaveBeenCalled()
  })

  it('surfaces the ORIGINAL upload error even when the cleanup delete also fails', async () => {
    const sonner = await import('sonner')
    const toastError = vi.mocked(sonner.toast.error)
    toastError.mockClear()

    createDocument.mockResolvedValue({ id: 'doc-4' })
    initiateUpload.mockRejectedValue(new Error('upload-original-error'))
    deleteDocument.mockRejectedValue(new Error('cleanup-also-broken'))

    const { result } = renderHook(
      () => useUpload('ws-1', 'folder-A'),
      { wrapper: wrap },
    )

    await act(async () => {
      await result.current.uploadFiles([new File(['x'], 'e.txt', { type: 'text/plain' })])
    })

    expect(deleteDocument).toHaveBeenCalled()
    // The toast should describe the upload failure, NOT the cleanup
    // failure — the user cares about why their upload didn't work.
    expect(toastError).toHaveBeenCalledTimes(1)
    const toastArg = String(toastError.mock.calls[0][0])
    expect(toastArg).toContain('upload-original-error')
    expect(toastArg).not.toContain('cleanup-also-broken')
  })
})
