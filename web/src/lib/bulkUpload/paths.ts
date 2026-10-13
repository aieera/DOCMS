// Path handling for bulk upload. Every relative path from a zip entry or a
// folder drop is run through sanitizeRelPath BEFORE any folder/document is
// created — this is the zip-slip guard (reject `..`, absolute, and drive paths).

/**
 * Normalize a raw archive/folder path to a safe forward-slash relative path,
 * or return null if it must be rejected (traversal / absolute / drive prefix /
 * empty). Collapses `.` and empty segments.
 */
export function sanitizeRelPath(raw: string): string | null {
  if (!raw) return null
  if (/^[a-zA-Z]:[\\/]/.test(raw)) return null // Windows drive prefix
  const norm = raw.replace(/\\/g, '/').replace(/^\/+/, '')
  const out: string[] = []
  for (const seg of norm.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') return null // traversal — reject the whole path
    out.push(seg)
  }
  return out.length ? out.join('/') : null
}

const JUNK = [/(^|\/)__MACOSX(\/|$)/, /(^|\/)\.DS_Store$/, /(^|\/)Thumbs\.db$/i]

/** OS/archive cruft that should never become a document. */
export function isJunkPath(p: string): boolean {
  return JUNK.some((re) => re.test(p))
}

/** The parent directory segments of a relative file path (excludes the file). */
export function dirSegments(relPath: string): string[] {
  return relPath.split('/').slice(0, -1)
}

/**
 * Return relPath, or — when `taken` already holds it — the first free
 * "name (n).ext" variant in the same directory. Two archive entries that
 * sanitise to the same path (A/x.txt, A//x.txt, A/./x.txt) used to
 * overwrite each other silently; the later one is now kept under a
 * suffixed name. The returned path is added to `taken`.
 */
export function uniqueRelPath(relPath: string, taken: Set<string>): string {
  let candidate = relPath
  if (taken.has(candidate)) {
    const slash = relPath.lastIndexOf('/')
    const dir = slash === -1 ? '' : relPath.slice(0, slash + 1)
    const base = relPath.slice(slash + 1)
    const dot = base.lastIndexOf('.')
    const stem = dot > 0 ? base.slice(0, dot) : base
    const ext = dot > 0 ? base.slice(dot) : ''
    for (let n = 2; taken.has(candidate); n++) candidate = `${dir}${stem} (${n})${ext}`
  }
  taken.add(candidate)
  return candidate
}
