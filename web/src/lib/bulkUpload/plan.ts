import { dirSegments } from './paths'
import type { PathFileMap, PlannedItem } from './types'

export interface PlanOpts {
  /** files larger than this are flagged 'oversize' (still shown, skipped at run) */
  maxFileBytes: number
  /** lowercase extensions that are flagged 'blocked_type' */
  blockedExt: string[]
}

export interface Plan {
  items: PlannedItem[]
  totalBytes: number
  fileCount: number
  folderCount: number
}

/**
 * Turn a sanitized path→File map into an engine-agnostic PlannedItem[] plus
 * summary counts. Folders are the distinct parent directories; files carry
 * size/mime and a validation status. Pure — no I/O.
 */
export function buildPlan(map: PathFileMap, opts: PlanOpts): Plan {
  const folderSet = new Set<string>()
  const items: PlannedItem[] = []
  let totalBytes = 0

  for (const [relPath, file] of map) {
    const segs = dirSegments(relPath)
    for (let i = 0; i < segs.length; i++) folderSet.add(segs.slice(0, i + 1).join('/'))

    const ext = relPath.includes('.') ? relPath.split('.').pop()!.toLowerCase() : ''
    let status: PlannedItem['status'] = 'ok'
    if (ext && opts.blockedExt.includes(ext)) status = 'blocked_type'
    else if (file.size > opts.maxFileBytes) status = 'oversize'

    totalBytes += file.size
    items.push({ relPath, type: 'file', sizeBytes: file.size, mimeType: file.type, status })
  }

  for (const dir of folderSet) items.push({ relPath: dir, type: 'folder', status: 'ok' })

  return { items, totalBytes, fileCount: map.size, folderCount: folderSet.size }
}
