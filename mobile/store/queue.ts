import NetInfo from '@react-native-community/netinfo'
import * as SQLite from 'expo-sqlite'

import { uploadScannedPdf } from '../api/upload'

// Persistent (expo-sqlite) outbox for scanned documents. A capture made
// offline survives app restarts and uploads when connectivity returns —
// satisfying the DoD's "offline capture syncs when back online".

// 'stalled' — retries exhausted; kept (never silently dropped) until the
// user retries it.
export type QueueStatus = 'pending' | 'uploading' | 'done' | 'failed' | 'stalled'

export interface QueueJob {
  id: number
  file_uri: string
  filename: string
  workspace_id: string
  folder_id: string
  size_bytes: number
  status: QueueStatus
  attempts: number
  last_error: string | null
  document_id: string | null
  content_blob_id: string | null
  claimed_at: number | null
}

const MAX_ATTEMPTS = 5
// A claim older than this is treated as abandoned (app killed mid-upload).
const STALE_CLAIM_MS = 10 * 60_000

let _db: Promise<SQLite.SQLiteDatabase> | null = null
function db(): Promise<SQLite.SQLiteDatabase> {
  if (!_db) {
    _db = SQLite.openDatabaseAsync('capture_queue.db').then(async (d) => {
      await d.execAsync(
        `CREATE TABLE IF NOT EXISTS upload_queue (
           id INTEGER PRIMARY KEY AUTOINCREMENT,
           file_uri TEXT NOT NULL,
           filename TEXT NOT NULL,
           workspace_id TEXT NOT NULL,
           folder_id TEXT NOT NULL,
           size_bytes INTEGER NOT NULL,
           status TEXT NOT NULL DEFAULT 'pending',
           attempts INTEGER NOT NULL DEFAULT 0,
           last_error TEXT,
           document_id TEXT,
           created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
         )`,
      )
      // W-2 progress columns (resumable replay + atomic claim). ALTER
      // fails harmlessly when the column already exists.
      for (const col of ['content_blob_id TEXT', 'claimed_at INTEGER']) {
        try {
          await d.runAsync(`ALTER TABLE upload_queue ADD COLUMN ${col}`)
        } catch {
          // already present
        }
      }
      return d
    })
  }
  return _db
}

export async function enqueue(
  job: Pick<QueueJob, 'file_uri' | 'filename' | 'workspace_id' | 'folder_id' | 'size_bytes'>,
): Promise<number> {
  const d = await db()
  const res = await d.runAsync(
    `INSERT INTO upload_queue (file_uri, filename, workspace_id, folder_id, size_bytes) VALUES (?,?,?,?,?)`,
    job.file_uri,
    job.filename,
    job.workspace_id,
    job.folder_id,
    job.size_bytes,
  )
  return res.lastInsertRowId
}

// active returns jobs that still need work (pending/failed under the retry cap,
// plus any stuck 'uploading' left by a crash).
export async function active(): Promise<QueueJob[]> {
  const d = await db()
  return d.getAllAsync<QueueJob>(
    `SELECT * FROM upload_queue
      WHERE status IN ('pending','failed','uploading') AND attempts < ?
      ORDER BY id`,
    MAX_ATTEMPTS,
  )
}

export async function all(): Promise<QueueJob[]> {
  const d = await db()
  return d.getAllAsync<QueueJob>(`SELECT * FROM upload_queue ORDER BY id DESC LIMIT 100`)
}

export async function isOnline(): Promise<boolean> {
  const s = await NetInfo.fetch()
  return !!s.isConnected && s.isInternetReachable !== false
}

export async function stalled(): Promise<QueueJob[]> {
  const d = await db()
  return d.getAllAsync<QueueJob>(`SELECT * FROM upload_queue WHERE status='stalled' ORDER BY id`)
}

// retryJob puts a stalled (or failed) capture back in the queue.
export async function retryJob(id: number): Promise<void> {
  const d = await db()
  await d.runAsync(`UPDATE upload_queue SET status='pending', attempts=?, last_error=NULL WHERE id=?`, 0, id)
}

// claim atomically moves a job to 'uploading'. Only one drain (or one
// process) wins; a claim left behind by a crash is reclaimable after
// STALE_CLAIM_MS.
async function claim(d: SQLite.SQLiteDatabase, id: number): Promise<boolean> {
  const now = Date.now()
  const res = await d.runAsync(
    `UPDATE upload_queue SET status='uploading', claimed_at=?
      WHERE id=? AND (status IN ('pending','failed')
        OR (status='uploading' AND (claimed_at IS NULL OR claimed_at < ?)))`,
    now,
    id,
    now - STALE_CLAIM_MS,
  )
  return res.changes === 1
}

let draining: Promise<number> | null = null

// drain uploads every active job. Concurrent calls (NetInfo reconnect +
// manual "upload now") share one pass, and each job is claimed atomically
// before work starts (W-2). Each completed step (blob stored, document
// created) is persisted, so a replay resumes instead of duplicating.
// Returns how many uploaded this pass.
export function drain(onChange?: () => void): Promise<number> {
  if (!draining) {
    draining = drainOnce(onChange).finally(() => { draining = null })
  }
  return draining
}

async function drainOnce(onChange?: () => void): Promise<number> {
  if (!(await isOnline())) return 0
  const d = await db()
  const jobs = await active()
  let uploaded = 0
  for (const job of jobs) {
    if (!(await claim(d, job.id))) continue
    onChange?.()
    try {
      const { documentId } = await uploadScannedPdf({
        fileUri: job.file_uri,
        filename: job.filename,
        workspaceId: job.workspace_id,
        folderId: job.folder_id,
        sizeBytes: job.size_bytes,
        resume: { contentBlobId: job.content_blob_id, documentId: job.document_id },
        onBlob: async (blobId) => {
          await d.runAsync(`UPDATE upload_queue SET content_blob_id=? WHERE id=?`, blobId, job.id)
        },
        onDocument: async (docId) => {
          await d.runAsync(`UPDATE upload_queue SET document_id=? WHERE id=?`, docId, job.id)
        },
      })
      await d.runAsync(
        `UPDATE upload_queue SET status='done', document_id=?, last_error=NULL WHERE id=?`,
        documentId,
        job.id,
      )
      uploaded++
    } catch (e) {
      const attempts = (job.attempts ?? 0) + 1
      await d.runAsync(
        `UPDATE upload_queue SET status=?, attempts=?, last_error=? WHERE id=?`,
        attempts >= MAX_ATTEMPTS ? 'stalled' : 'failed',
        attempts,
        String(e),
        job.id,
      )
    }
    onChange?.()
  }
  return uploaded
}

// startAutoDrain drains the queue whenever connectivity returns. Returns the
// NetInfo unsubscribe fn; call it on unmount.
export function startAutoDrain(onChange?: () => void): () => void {
  return NetInfo.addEventListener((state) => {
    if (state.isConnected) void drain(onChange)
  })
}
