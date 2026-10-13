// DEC-12 / M-1: storage initiate never returns an existing blob any more
// (deduplicated is always false, existing_blob_id empty). The Word save
// flow must always PUT and complete, and link only the blob complete
// returns — never a session-supplied existing_blob_id.
vi.mock('../src/auth', () => ({
  getSession: async () => ({ token: 't', userID: 'u' }),
  clearSession: () => {},
}))

import { uploadBlob } from '../src/api'

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('Word save upload (DEC-12)', () => {
  it('always PUTs and completes, ignoring a stale dedup hint', async () => {
    const calls: string[] = []
    globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      const u = String(url)
      calls.push(`${init?.method ?? 'GET'} ${u}`)
      if (u.endsWith('/uploads/initiate')) {
        return json({ upload_id: 'up-1', presigned_put_url: 'https://s3/put', deduplicated: true, existing_blob_id: 'foreign' })
      }
      if (u === 'https://s3/put') return new Response(null, { status: 200 })
      if (u.endsWith('/uploads/up-1/complete')) return json({ content_blob_id: 'blob-1' })
      return json({}, 500)
    }) as unknown as typeof fetch

    const blob = await uploadBlob({ bytes: new Uint8Array([1, 2]), filename: 'a.docx', mimeType: 'x', documentID: 'd', workspaceID: 'w' })
    expect(blob).toBe('blob-1')
    expect(calls).toContain('PUT https://s3/put')
    expect(calls.some((c) => c.endsWith('/complete'))).toBe(true)
  })

  it('throws when complete returns no blob id', async () => {
    globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
      const u = String(url)
      if (u.endsWith('/uploads/initiate')) return json({ upload_id: 'up-1', presigned_put_url: 'https://s3/put', existing_blob_id: 'foreign' })
      if (u === 'https://s3/put') return new Response(null, { status: 200 })
      return json({})
    }) as unknown as typeof fetch
    await expect(uploadBlob({ bytes: new Uint8Array([1]), filename: 'a.docx', mimeType: 'x', documentID: 'd' })).rejects.toThrow(/blob id/)
  })
})
