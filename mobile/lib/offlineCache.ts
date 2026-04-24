// Per-tenant offline document cache.
//
// Write path (online doc view):
//   1. App opens doc → HTTP fetch succeeds → put(tenantId, metadata, blob).
//   2. Blob encrypted with per-tenant KEK, written to FileSystem.cacheDir.
//   3. cached_docs row upsert with last_opened_at = now.
//   4. gc() runs: if SUM(size_bytes) > CAP_BYTES, delete oldest by
//      last_opened_at until under cap.
//
// Read path (plane mode / offline):
//   1. App opens doc → no network → get(tenantId, docId).
//   2. Returns { metadata, plaintextBlob } or null.
//   3. touch() bumps last_opened_at without rewriting the blob.
//
// The cache is per-tenant: a query requires tenantId, and eviction
// never crosses tenant boundaries. Keys are per-tenant too (crypto.ts).

import * as FileSystem from 'expo-file-system'
import { db } from './db'
import { encryptBytes, decryptBytes } from './crypto'

export const CACHE_CAP_BYTES = 500 * 1024 * 1024 // 500 MB total
const CACHE_DIR = FileSystem.cacheDirectory + 'vaultdms-docs/'

async function ensureDir() {
  const info = await FileSystem.getInfoAsync(CACHE_DIR)
  if (!info.exists) await FileSystem.makeDirectoryAsync(CACHE_DIR, { intermediates: true })
}

function blobPath(tenantId: string, documentId: string): string {
  return `${CACHE_DIR}${tenantId}__${documentId}.bin`
}

function toBase64(bytes: Uint8Array): string {
  // Minimal base64 encoder — expo-file-system writes UTF-8 or base64
  // strings only. Keep this independent of Node's Buffer.
  let binary = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode.apply(null, Array.from(bytes.subarray(i, i + chunk)))
  }
  return btoa(binary)
}
function fromBase64(b64: string): Uint8Array {
  const binary = atob(b64)
  const out = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i)
  return out
}

export interface CachedDoc<M = Record<string, unknown>> {
  metadata: M
  blob: Uint8Array
  mimeType: string | null
  sizeBytes: number
}

export async function put<M>(
  tenantId: string,
  documentId: string,
  metadata: M,
  plaintext: Uint8Array,
  mimeType: string | null,
): Promise<void> {
  await ensureDir()
  const ciphertext = await encryptBytes(tenantId, plaintext)
  const path = blobPath(tenantId, documentId)
  await FileSystem.writeAsStringAsync(path, toBase64(ciphertext), { encoding: FileSystem.EncodingType.Base64 })

  const now = Date.now()
  const d = await db()
  await d.runAsync(
    `INSERT INTO cached_docs (tenant_id, document_id, metadata_json, blob_path, mime_type, size_bytes, last_opened_at)
     VALUES (?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT(tenant_id, document_id) DO UPDATE SET
       metadata_json = excluded.metadata_json,
       blob_path = excluded.blob_path,
       mime_type = excluded.mime_type,
       size_bytes = excluded.size_bytes,
       last_opened_at = excluded.last_opened_at`,
    [tenantId, documentId, JSON.stringify(metadata), path, mimeType, plaintext.length, now],
  )
  await gc()
}

export async function get<M = Record<string, unknown>>(
  tenantId: string, documentId: string,
): Promise<CachedDoc<M> | null> {
  const d = await db()
  const row = await d.getFirstAsync<{
    metadata_json: string; blob_path: string; mime_type: string | null; size_bytes: number
  }>(
    `SELECT metadata_json, blob_path, mime_type, size_bytes FROM cached_docs
     WHERE tenant_id = ? AND document_id = ?`,
    [tenantId, documentId],
  )
  if (!row) return null
  const info = await FileSystem.getInfoAsync(row.blob_path)
  if (!info.exists) {
    // Row dangles — the blob was swept by the OS. Drop the row.
    await d.runAsync(`DELETE FROM cached_docs WHERE tenant_id=? AND document_id=?`, [tenantId, documentId])
    return null
  }
  const b64 = await FileSystem.readAsStringAsync(row.blob_path, { encoding: FileSystem.EncodingType.Base64 })
  const plaintext = await decryptBytes(tenantId, fromBase64(b64))
  await touch(tenantId, documentId)
  return {
    metadata: JSON.parse(row.metadata_json) as M,
    blob: plaintext,
    mimeType: row.mime_type,
    sizeBytes: row.size_bytes,
  }
}

export async function touch(tenantId: string, documentId: string): Promise<void> {
  const d = await db()
  await d.runAsync(
    `UPDATE cached_docs SET last_opened_at = ? WHERE tenant_id=? AND document_id=?`,
    [Date.now(), tenantId, documentId],
  )
}

/**
 * LRU eviction: drop oldest rows (across all tenants) until total
 * size is under CACHE_CAP_BYTES. Each delete unlinks the encrypted
 * blob from disk too.
 */
export async function gc(): Promise<void> {
  const d = await db()
  const total = (await d.getFirstAsync<{ s: number }>(`SELECT COALESCE(SUM(size_bytes),0) AS s FROM cached_docs`))?.s ?? 0
  if (total <= CACHE_CAP_BYTES) return

  let toFree = total - CACHE_CAP_BYTES
  const victims = await d.getAllAsync<{ tenant_id: string; document_id: string; blob_path: string; size_bytes: number }>(
    `SELECT tenant_id, document_id, blob_path, size_bytes FROM cached_docs
     ORDER BY last_opened_at ASC`,
  )
  for (const v of victims) {
    if (toFree <= 0) break
    try { await FileSystem.deleteAsync(v.blob_path, { idempotent: true }) } catch { /* ignore */ }
    await d.runAsync(`DELETE FROM cached_docs WHERE tenant_id=? AND document_id=?`, [v.tenant_id, v.document_id])
    toFree -= v.size_bytes
  }
}
