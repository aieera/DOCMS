// W-5: every decrypted document byte the app writes to disk (preview
// copies, download-and-share files) lives under ONE app-owned directory,
// and logout deletes that directory. Previously react-native-pdf's own
// network cache and loose files in cacheDirectory survived logout, so the
// next user on a shared device could reach the previous account's files.
import * as FileSystem from 'expo-file-system'

export const DOC_CACHE_DIR = `${FileSystem.cacheDirectory}sedoc-docs/`

async function ensureDir(): Promise<void> {
  const info = await FileSystem.getInfoAsync(DOC_CACHE_DIR)
  if (!info.exists) await FileSystem.makeDirectoryAsync(DOC_CACHE_DIR, { intermediates: true })
}

function safeName(s: string): string {
  return s.replace(/[^\w.-]+/g, '_').slice(0, 80) || 'document'
}

function extFor(mime?: string): string {
  if (mime === 'application/pdf') return '.pdf'
  if (mime === 'image/png') return '.png'
  if (mime === 'image/jpeg') return '.jpg'
  if (mime === 'image/webp') return '.webp'
  return ''
}

/**
 * Download a document's bytes into the app-owned cache and return the
 * local file URI. Callers render/share that file instead of letting a
 * native component keep its own (un-wipeable) network cache.
 */
export async function localDocumentCopy(opts: {
  url: string
  documentID: string
  mime?: string
  token: string
  name?: string
}): Promise<string> {
  await ensureDir()
  const base = safeName(opts.name ?? opts.documentID)
  const dest = `${DOC_CACHE_DIR}${safeName(opts.documentID)}-${base}${extFor(opts.mime)}`
  const res = await FileSystem.downloadAsync(opts.url, dest, {
    headers: { Authorization: `Bearer ${opts.token}` },
  })
  if (res.status !== 200) {
    await FileSystem.deleteAsync(dest, { idempotent: true })
    throw new Error(`download failed (${res.status})`)
  }
  return res.uri
}

/** Delete every cached document byte (logout). Best-effort. */
export async function wipeDocCache(): Promise<void> {
  try {
    await FileSystem.deleteAsync(DOC_CACHE_DIR, { idempotent: true })
  } catch {
    // Nothing cached, or already gone.
  }
}
