// W-2: mobile capture upload + offline queue.
//   - The document row was created BEFORE the bytes were stored, so every
//     failed/refused upload left a contentless document, and each queue
//     replay created another one (non-idempotent).
//   - Two drains (NetInfo reconnect + manual) could pick the same job.
//   - After 5 attempts a capture vanished from the active list silently.
// W-5 lives in authLogout.test.ts.

// ---- in-memory stand-ins for the native modules -----------------------
type Row = Record<string, unknown>
const h = vi.hoisted(() => ({
  rows: [] as Record<string, unknown>[],
  nextId: 1,
  online: true,
  calls: [] as string[],
  initiateResult: { upload_id: 'up-1', presigned_put_url: 'https://s3/put' } as Record<string, unknown>,
  completeResult: { content_blob_id: 'blob-1' } as Record<string, unknown>,
  failVersion: 0,
  failPut: 0,
}))

vi.mock('expo-sqlite', () => {
  // A tiny SQL interpreter for exactly the statements store/queue.ts issues.
  const db = {
    async execAsync() {},
    async runAsync(sql: string, ...args: unknown[]) {
      const s = sql.replace(/\s+/g, ' ').trim()
      if (s.startsWith('INSERT INTO upload_queue')) {
        const [file_uri, filename, workspace_id, folder_id, size_bytes] = args
        const row = { id: h.nextId++, file_uri, filename, workspace_id, folder_id, size_bytes,
          status: 'pending', attempts: 0, last_error: null, document_id: null, content_blob_id: null, claimed_at: null }
        h.rows.push(row)
        return { lastInsertRowId: row.id, changes: 1 }
      }
      if (s.startsWith('ALTER TABLE')) return { changes: 0 }
      if (s.startsWith('UPDATE upload_queue SET status=\'uploading\', claimed_at=?')) {
        const [now, id, staleBefore] = args as [number, number, number]
        const r = h.rows.find((x) => x.id === id)
        if (!r) return { changes: 0 }
        const claimable = r.status === 'pending' || r.status === 'failed' ||
          (r.status === 'uploading' && ((r.claimed_at as number | null) ?? 0) < staleBefore)
        if (!claimable) return { changes: 0 }
        r.status = 'uploading'; r.claimed_at = now
        return { changes: 1 }
      }
      const set = /^UPDATE upload_queue SET (.+) WHERE id=\?$/.exec(s)
      if (set) {
        const id = args[args.length - 1]
        const r = h.rows.find((x) => x.id === id)!
        let i = 0
        for (const part of set[1].split(/,\s*/)) {
          const [col, val] = part.split('=')
          if (val === '?') r[col] = args[i++]
          else if (val === 'NULL') r[col] = null
          else if (val === 'attempts+1') r.attempts = (r.attempts as number) + 1
          else r[col] = val.replace(/'/g, '')
        }
        return { changes: 1 }
      }
      throw new Error('unexpected SQL: ' + s)
    },
    async getAllAsync(sql: string, ...args: unknown[]) {
      const s = sql.replace(/\s+/g, ' ')
      if (s.includes("status IN ('pending','failed','uploading') AND attempts < ?")) {
        return h.rows.filter((r) => ['pending', 'failed', 'uploading'].includes(r.status as string) && (r.attempts as number) < (args[0] as number))
      }
      return [...h.rows].reverse()
    },
  }
  return { openDatabaseAsync: async () => db }
})
vi.mock('@react-native-community/netinfo', () => ({
  default: { fetch: async () => ({ isConnected: h.online, isInternetReachable: h.online }), addEventListener: () => () => {} },
}))
vi.mock('expo-file-system', () => ({
  FileSystemUploadType: { BINARY_CONTENT: 0 },
  uploadAsync: async (url: string) => {
    h.calls.push('PUT ' + url)
    if (h.failPut > 0) { h.failPut--; return { status: 503 } }
    return { status: 200 }
  },
}))
vi.mock('../api/client', () => ({
  api: {
    post: async (url: string, body?: Record<string, unknown>) => {
      h.calls.push('POST ' + url + ' ' + JSON.stringify(body ?? {}))
      if (url === '/storage/uploads/initiate') return { data: h.initiateResult }
      if (url.endsWith('/complete')) return { data: h.completeResult }
      if (url === '/documents') return { data: { id: 'doc-' + h.calls.filter((c) => c.startsWith('POST /documents ')).length } }
      if (url.endsWith('/versions')) {
        if (h.failVersion > 0) { h.failVersion--; throw new Error('version 503') }
        return { data: {} }
      }
      throw new Error('unexpected ' + url)
    },
  },
}))

import { uploadScannedPdf } from '../api/upload'
import { enqueue, drain, all, retryJob } from '../store/queue'

const job = { file_uri: 'file:///scan.pdf', filename: 'scan.pdf', workspace_id: 'ws-1', folder_id: 'f-1', size_bytes: 10 }

beforeEach(() => {
  h.rows.length = 0
  h.nextId = 1
  h.online = true
  h.calls.length = 0
  h.initiateResult = { upload_id: 'up-1', presigned_put_url: 'https://s3/put' }
  h.completeResult = { content_blob_id: 'blob-1' }
  h.failVersion = 0
  h.failPut = 0
})

describe('uploadScannedPdf order (W-2)', () => {
  it('stores bytes before creating the document, initiate carries the folder', async () => {
    const r = await uploadScannedPdf({ fileUri: 'file:///a.pdf', filename: 'a.pdf', workspaceId: 'ws-1', folderId: 'f-1', sizeBytes: 10 })
    expect(r.documentId).toBe('doc-1')
    const idx = (p: string) => h.calls.findIndex((c) => c.startsWith(p))
    expect(idx('POST /storage/uploads/initiate')).toBeLessThan(idx('PUT '))
    expect(idx('PUT ')).toBeLessThan(idx('POST /storage/uploads/up-1/complete'))
    expect(idx('POST /storage/uploads/up-1/complete')).toBeLessThan(idx('POST /documents '))
    expect(h.calls[idx('POST /storage/uploads/initiate')]).toContain('"folder_id":"f-1"')
  })

  it('a refused PUT creates no document', async () => {
    h.failPut = 1
    await expect(uploadScannedPdf({ fileUri: 'file:///a.pdf', filename: 'a.pdf', workspaceId: 'ws-1', folderId: 'f-1', sizeBytes: 10 })).rejects.toThrow()
    expect(h.calls.some((c) => c.startsWith('POST /documents '))).toBe(false)
  })

  it('ignores a stale dedup hint and always PUTs (DEC-12)', async () => {
    h.initiateResult = { upload_id: 'up-1', presigned_put_url: 'https://s3/put', existing_blob_id: 'foreign', deduplicated: true }
    await uploadScannedPdf({ fileUri: 'file:///a.pdf', filename: 'a.pdf', workspaceId: 'ws-1', folderId: 'f-1', sizeBytes: 10 })
    expect(h.calls.some((c) => c.startsWith('PUT '))).toBe(true)
    expect(h.calls.some((c) => c.includes('foreign'))).toBe(false)
  })
})

describe('offline queue (W-2)', () => {
  it('a replay after a version failure does not create a second document', async () => {
    await enqueue(job)
    h.failVersion = 1
    await drain()
    expect(h.rows[0].status).toBe('failed')
    expect(h.rows[0].document_id).toBe('doc-1')
    await drain()
    expect(h.rows[0].status).toBe('done')
    expect(h.calls.filter((c) => c.startsWith('POST /documents ')).length).toBe(1)
    // bytes were not re-uploaded either
    expect(h.calls.filter((c) => c.startsWith('PUT ')).length).toBe(1)
  })

  it('concurrent drains upload a job once', async () => {
    await enqueue(job)
    await Promise.all([drain(), drain()])
    expect(h.calls.filter((c) => c.startsWith('PUT ')).length).toBe(1)
    expect(h.calls.filter((c) => c.startsWith('POST /documents ')).length).toBe(1)
  })

  it('a capture that exhausts its retries stays visible as stalled and can be retried', async () => {
    await enqueue(job)
    for (let i = 0; i < 5; i++) { h.failPut = 1; await drain() }
    const [row] = await all()
    expect(row.status).toBe('stalled')
    expect(row.last_error).toBeTruthy()
    await retryJob(row.id)
    await drain()
    expect(h.rows[0].status).toBe('done')
  })
})
