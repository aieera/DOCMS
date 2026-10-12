import { Unzip, UnzipInflate, type UnzipFile } from 'fflate'
import { sanitizeRelPath, isJunkPath } from './paths'
import type { PathFileMap } from './types'

// A folder picker (`<input webkitdirectory>`) prefixes every path with the
// chosen directory's name, e.g. "MyDocs/A/x.pdf". Strip that first segment so
// the tree maps under the chosen destination rather than an extra wrapper folder.
function stripRoot(relPath: string): string {
  const i = relPath.indexOf('/')
  return i === -1 ? relPath : relPath.slice(i + 1)
}

/** Build a path→File map from a folder drop / `<input webkitdirectory>` selection. */
export function parseFolderDrop(files: FileList | File[]): PathFileMap {
  const out: PathFileMap = new Map()
  for (const file of Array.from(files)) {
    const raw = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name
    const rel = sanitizeRelPath(stripRoot(raw))
    if (!rel || isJunkPath(rel)) continue
    out.set(rel, file)
  }
  return out
}

// W-7 limits. The archive used to be read whole into memory and inflated
// in one go with no gate (a zip bomb or a multi-GB archive froze or
// crashed the tab). Now: a hard cap on the archive size checked BEFORE
// any byte is read, then a chunked streaming inflate with per-entry,
// total and entry-count caps enforced as bytes arrive.
export const MAX_ARCHIVE_BYTES = 2 * 1024 ** 3 // 2 GiB
export const MAX_ENTRY_BYTES = 512 * 1024 ** 2 // 512 MiB inflated per entry
export const MAX_TOTAL_BYTES = 2 * 1024 ** 3 // 2 GiB inflated in total
export const MAX_ENTRIES = 10_000
const CHUNK_BYTES = 1024 ** 2

export interface ParseZipLimits {
  maxArchiveBytes?: number
  maxEntryBytes?: number
  maxTotalBytes?: number
  maxEntries?: number
  /** read size per slice (tests use tiny chunks to exercise streaming) */
  chunkBytes?: number
}

export class ArchiveLimitError extends Error {}

const fmtGiB = (n: number) => `${+(n / 1024 ** 3).toFixed(2)} GiB`

/**
 * Unzip a .zip File into a path→File map. Directory entries + junk are
 * skipped (and never inflated). Rejects — never throws asynchronously —
 * on an oversize archive, an over-cap entry/total, too many entries, or a
 * corrupt archive.
 */
export async function parseZip(file: File, limits: ParseZipLimits = {}): Promise<PathFileMap> {
  const maxArchive = limits.maxArchiveBytes ?? MAX_ARCHIVE_BYTES
  const maxEntry = limits.maxEntryBytes ?? MAX_ENTRY_BYTES
  const maxTotal = limits.maxTotalBytes ?? MAX_TOTAL_BYTES
  const maxEntries = limits.maxEntries ?? MAX_ENTRIES
  const chunk = limits.chunkBytes ?? CHUNK_BYTES

  if (file.size > maxArchive) {
    throw new ArchiveLimitError(`Archive is larger than the ${fmtGiB(maxArchive)} limit`)
  }

  const out: PathFileMap = new Map()
  const pending: Promise<void>[] = []
  let failure: Error | null = null
  let entries = 0
  let total = 0
  let started = 0
  let settled = 0
  const fail = (e: Error) => { if (!failure) failure = e }

  const uz = new Unzip()
  uz.register(UnzipInflate)
  uz.onfile = (f: UnzipFile) => {
    if (failure) return
    if (++entries > maxEntries) {
      fail(new ArchiveLimitError(`Archive has too many entries (limit ${maxEntries})`))
      return
    }
    if (f.name.endsWith('/')) return // directory entry
    const rel = sanitizeRelPath(f.name)
    if (!rel || isJunkPath(rel)) return
    if (f.originalSize !== undefined && f.originalSize > maxEntry) {
      fail(new ArchiveLimitError(`${rel} is too large to import from an archive`))
      return
    }
    const parts: Uint8Array[] = []
    let size = 0
    started++
    pending.push(new Promise<void>((done) => {
      const resolve = () => { settled++; done() }
      f.ondata = (err, data, final) => {
        if (failure) { resolve(); return }
        if (err) { fail(err); resolve(); return }
        size += data.length
        total += data.length
        if (size > maxEntry) {
          f.terminate()
          fail(new ArchiveLimitError(`${rel} is too large to import from an archive`))
          resolve()
          return
        }
        if (total > maxTotal) {
          f.terminate()
          fail(new ArchiveLimitError(`Archive inflates past the ${fmtGiB(maxTotal)} limit — too large to import`))
          resolve()
          return
        }
        parts.push(data)
        if (final) {
          out.set(rel, new File(parts as BlobPart[], rel.split('/').pop()!))
          resolve()
        }
      }
    }))
    try {
      f.start()
    } catch (e) {
      fail(e as Error)
    }
  }

  try {
    for (let off = 0; off < file.size && !failure; off += chunk) {
      const end = Math.min(off + chunk, file.size)
      const buf = new Uint8Array(await file.slice(off, end).arrayBuffer())
      uz.push(buf, end >= file.size)
    }
    if (file.size === 0) uz.push(new Uint8Array(0), true)
  } catch (e) {
    fail(e instanceof Error ? e : new Error(String(e)))
  }
  // UnzipInflate is synchronous: once the final chunk is pushed every
  // entry has either finished or never will (truncated archive) — fail
  // rather than await a promise that cannot settle.
  if (!failure && settled < started) fail(new Error('ZIP archive is truncated or corrupt'))
  if (!failure) await Promise.all(pending)
  if (failure) throw failure
  if (entries === 0) throw new Error('Not a readable ZIP archive')
  return out
}
