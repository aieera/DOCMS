import NetInfo from '@react-native-community/netinfo'
import * as SQLite from 'expo-sqlite'

import { uploadScannedPdf } from '../api/upload'

// Persistent (expo-sqlite) outbox for scanned documents. A capture made
// offline survives app restarts and uploads when connectivity returns —
// satisfying the DoD's "offline capture syncs when back online".

export type QueueStatus = 'pending' | 'uploading' | 'done' | 'failed'

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
}

const MAX_ATTEMPTS = 5

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

// drain uploads every active job; safe to call repeatedly (idempotent enough —
// a 'done' job is never retried). Returns how many uploaded this pass.
export async function drain(onChange?: () => void): Promise<number> {
  if (!(await isOnline())) return 0
  const d = await db()
  const jobs = await active()
  let uploaded = 0
  for (const job of jobs) {
    await d.runAsync(`UPDATE upload_queue SET status='uploading' WHERE id=?`, job.id)
    onChange?.()
    try {
      const { documentId } = await uploadScannedPdf({
        fileUri: job.file_uri,
        filename: job.filename,
        workspaceId: job.workspace_id,
        folderId: job.folder_id,
        sizeBytes: job.size_bytes,
      })
      await d.runAsync(
        `UPDATE upload_queue SET status='done', document_id=?, last_error=NULL WHERE id=?`,
        documentId,
        job.id,
      )
      uploaded++
    } catch (e) {
      await d.runAsync(
        `UPDATE upload_queue SET status='failed', attempts=attempts+1, last_error=? WHERE id=?`,
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
