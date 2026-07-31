import { unzip } from 'fflate'
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

/** Unzip a .zip File into a path→File map. Directory entries + junk are skipped. */
export function parseZip(file: File): Promise<PathFileMap> {
  return file.arrayBuffer().then(
    (buf) =>
      new Promise<PathFileMap>((resolve, reject) => {
        unzip(new Uint8Array(buf), (err, unzipped) => {
          if (err) return reject(err)
          const out: PathFileMap = new Map()
          for (const [name, bytes] of Object.entries(unzipped)) {
            if (name.endsWith('/')) continue // directory entry
            const rel = sanitizeRelPath(name)
            if (!rel || isJunkPath(rel)) continue
            out.set(rel, new File([bytes as Uint8Array], rel.split('/').pop()!))
          }
          resolve(out)
        })
      }),
  )
}
