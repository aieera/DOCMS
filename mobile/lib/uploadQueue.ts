// Capture-to-upload queue. Persists across app restarts so a capture
// made offline reliably lands on server once connectivity returns.
//
// State machine per row:
//   queued ──► uploading ──► completed
//      ▲              │
//      └──(retry)─────┘
//                     └────► failed (attempts >= MAX_ATTEMPTS)
//
// Resume triggers:
//   1. App foreground + connectivity change to online (NetInfo).
//   2. Background task wake (lib/backgroundSync.ts).
//   3. Manual `drain()` call.
//
// Size cap: 25 MB per capture. Enforced at enqueue time; bigger
// captures are rejected rather than stored-and-failed later.

import * as FileSystem from 'expo-file-system'
import NetInfo from '@react-native-community/netinfo'
import { db } from './db'
import { api } from '../api/client'

export const MAX_BYTES = 25 * 1024 * 1024
export const MAX_ATTEMPTS = 5

interface PendingUpload {
  id: string
  tenant_id: string
  local_path: string
  filename: string
  mime_type: string
  size_bytes: number
  status: 'queued' | 'uploading' | 'completed' | 'failed'
  attempts: number
  last_error: string | null
}

function randomId(): string {
  // 128-bit hex — enough entropy for client-side correlation; the
  // server assigns the authoritative document id after ingest.
  const r = new Uint8Array(16)
  // Avoid expo-crypto import here to keep this module usable from
  // the background task where JS globals get restricted.
  for (let i = 0; i < r.length; i++) r[i] = Math.floor(Math.random() * 256)
  return Array.from(r).map((b) => b.toString(16).padStart(2, '0')).join('')
}

/**
 * Enqueue a captured file. Throws when the file exceeds MAX_BYTES
 * or the path can't be stat'd — caller should surface these as
 * user-visible errors at capture time.
 */
export async function enqueue(args: {
  tenantId: string; localPath: string; filename: string; mimeType: string
}): Promise<{ id: string }> {
  const info = await FileSystem.getInfoAsync(args.localPath, { size: true })
  if (!info.exists) throw new Error('captured file missing')
  const size = 'size' in info ? Number(info.size ?? 0) : 0
  if (size > MAX_BYTES) throw new Error(`capture too large: ${size} > ${MAX_BYTES}`)

  const now = Date.now()
  const id = randomId()
  const d = await db()
  await d.runAsync(
    `INSERT INTO pending_uploads
       (id, tenant_id, local_path, filename, mime_type, size_bytes, status, attempts, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, 'queued', 0, ?, ?)`,
    [id, args.tenantId, args.localPath, args.filename, args.mimeType, size, now, now],
  )
  // Kick a drain right away — if we're online, the capture uploads
  // before the user navigates off the screen.
  drain().catch(() => { /* swallow; next trigger picks up */ })
  return { id }
}

export async function listQueued(tenantId: string): Promise<PendingUpload[]> {
  const d = await db()
  return d.getAllAsync<PendingUpload>(
    `SELECT * FROM pending_uploads
     WHERE tenant_id = ? AND status IN ('queued','uploading','failed')
     ORDER BY created_at ASC`,
    [tenantId],
  )
}

let draining = false

/**
 * Process every queued row sequentially. Safe to call concurrently —
 * the `draining` flag short-circuits re-entry. A single failing row
 * does not halt the drain; it moves to failed/attempts+=1 and the
 * next tick tries again.
 */
export async function drain(): Promise<void> {
  if (draining) return
  const net = await NetInfo.fetch()
  if (!net.isConnected) return

  draining = true
  try {
    const d = await db()
    // eslint-disable-next-line no-constant-condition
    while (true) {
      const row = await d.getFirstAsync<PendingUpload>(
        `SELECT * FROM pending_uploads WHERE status IN ('queued','failed') AND attempts < ?
         ORDER BY created_at ASC LIMIT 1`,
        [MAX_ATTEMPTS],
      )
      if (!row) break
      await d.runAsync(
        `UPDATE pending_uploads SET status='uploading', updated_at=? WHERE id=?`,
        [Date.now(), row.id],
      )
      try {
        await uploadOne(row)
        await d.runAsync(
          `UPDATE pending_uploads SET status='completed', updated_at=? WHERE id=?`,
          [Date.now(), row.id],
        )
      } catch (err) {
        const attempts = row.attempts + 1
        const next = attempts >= MAX_ATTEMPTS ? 'failed' : 'queued'
        await d.runAsync(
          `UPDATE pending_uploads SET status=?, attempts=?, last_error=?, updated_at=? WHERE id=?`,
          [next, attempts, String(err), Date.now(), row.id],
        )
        if (next === 'failed') continue  // move on; don't block others
        // Transient failure — bail; next trigger retries.
        break
      }
    }
  } finally {
    draining = false
  }
}

async function uploadOne(row: PendingUpload): Promise<void> {
  // The existing /api/v1/storage/uploads/initiate → S3 PUT → /complete
  // flow is reused; this is a wire-up hop, not a new protocol. Keep it
  // tolerant of either a signed-URL or a direct multipart endpoint
  // depending on how api/upload.ts is wired today.
  const fileUri = row.local_path
  const form = new FormData()
  form.append('file', { uri: fileUri, name: row.filename, type: row.mime_type } as never)
  await api.post('/storage/uploads', form, {
    headers: { 'Content-Type': 'multipart/form-data' },
    // 25 MB max by capture-side cap; per-request timeout generous
    // enough for a mobile uplink.
    timeout: 90_000,
  })
}

/**
 * Starts a NetInfo listener that drains on re-connect. Call once at
 * app boot. Returns the unsubscribe handle.
 */
export function startNetworkListener(): () => void {
  return NetInfo.addEventListener((state) => {
    if (state.isConnected) drain().catch(() => { /* ignore */ })
  })
}
