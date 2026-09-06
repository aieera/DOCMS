import { it, expect, vi, beforeEach } from 'vitest'

const initiateUpload = vi.fn()
const uploadToPresigned = vi.fn()
const completeUpload = vi.fn()
const createDocument = vi.fn()
const createVersion = vi.fn()
vi.mock('@/api/upload', () => ({
  initiateUpload: (...a: unknown[]) => initiateUpload(...a),
  uploadToPresigned: (...a: unknown[]) => uploadToPresigned(...a),
  completeUpload: (...a: unknown[]) => completeUpload(...a),
}))
vi.mock('@/api/documents', () => ({
  createDocument: (...a: unknown[]) => createDocument(...a),
  createVersion: (...a: unknown[]) => createVersion(...a),
}))

import { uploadFileToFolder } from '../uploadFile'

beforeEach(() => {
  ;[initiateUpload, uploadToPresigned, completeUpload, createDocument, createVersion].forEach((m) => m.mockReset())
  createDocument.mockResolvedValue({ id: 'doc1' })
})

const file = new File([new Uint8Array(3)], 'x.pdf', { type: 'application/pdf' })

it('uploads + versions on the happy path', async () => {
  initiateUpload.mockResolvedValue({ upload_id: 'u1', presigned_put_url: 'http://put', deduplicated: false })
  completeUpload.mockResolvedValue({ content_blob_id: 'blob1' })
  const r = await uploadFileToFolder({ file, title: 'x.pdf', workspaceId: 'ws1', folderId: 'f1' })
  expect(r).toEqual({ documentId: 'doc1', deduplicated: false })
  expect(uploadToPresigned).toHaveBeenCalledOnce()
  expect(createVersion).toHaveBeenCalledWith({ document_id: 'doc1', content_blob_id: 'blob1', change_summary: 'initial' })
})

it('skips the PUT on a dedup hit', async () => {
  initiateUpload.mockResolvedValue({ upload_id: 'u1', presigned_put_url: '', deduplicated: true, existing_blob_id: 'blobX' })
  const r = await uploadFileToFolder({ file, title: 'x.pdf', workspaceId: 'ws1' })
  expect(r.deduplicated).toBe(true)
  expect(uploadToPresigned).not.toHaveBeenCalled()
  expect(createVersion).toHaveBeenCalledWith({ document_id: 'doc1', content_blob_id: 'blobX', change_summary: 'initial' })
})

it('throws when storage returns no blob id', async () => {
  initiateUpload.mockResolvedValue({ upload_id: 'u1', presigned_put_url: 'http://put', deduplicated: false })
  completeUpload.mockResolvedValue({})
  await expect(uploadFileToFolder({ file, title: 'x.pdf', workspaceId: 'ws1' })).rejects.toThrow(/content_blob_id/)
})

// SD-06: a refused upload must leave no document row behind. The old
// order created the row first, so an initiate/PUT/scan refusal left an
// orphan "No content" document (with no rollback at all in this path).
it('creates no document row when initiate is refused (SD-06)', async () => {
  initiateUpload.mockRejectedValue(new Error('INVALID_ARGUMENT: must be > 0'))
  await expect(uploadFileToFolder({ file, title: 'x.pdf', workspaceId: 'ws1' })).rejects.toThrow()
  expect(createDocument).not.toHaveBeenCalled()
})

it('creates the document only after the bytes are stored (SD-06)', async () => {
  initiateUpload.mockResolvedValue({ upload_id: 'u1', presigned_put_url: 'http://put', deduplicated: false })
  completeUpload.mockResolvedValue({ content_blob_id: 'blob1' })
  await uploadFileToFolder({ file, title: 'x.pdf', workspaceId: 'ws1' })
  expect(createDocument.mock.invocationCallOrder[0])
    .toBeGreaterThan(completeUpload.mock.invocationCallOrder[0])
})
