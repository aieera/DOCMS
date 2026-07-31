import { createWorkspace, getFolders, createFolder } from '@/api/workspaces'
import type { Destination } from './types'

/** Resolve the destination workspace id, creating a new workspace when asked. */
export async function ensureWorkspace(dest: Destination): Promise<string> {
  if (dest.mode === 'existing') {
    if (!dest.workspaceId) throw new Error('workspace required')
    return dest.workspaceId
  }
  const name = (dest.newWorkspaceName ?? '').trim()
  if (!name) throw new Error('new workspace name required')
  const ws = await createWorkspace(name)
  return ws.id
}

/**
 * Returns an idempotent folder ensurer for one workspace. `ensure(dirPath)`
 * resolves (or creates) every segment of dirPath under the optional root folder,
 * reusing an existing same-name child when present, and caches path→folderId so
 * a directory is only resolved once across the whole import.
 */
export function makeFolderEnsurer(workspaceId: string, rootFolderId?: string) {
  const cache = new Map<string, string | undefined>()
  cache.set('', rootFolderId)

  async function ensure(dirPath: string): Promise<string | undefined> {
    if (cache.has(dirPath)) return cache.get(dirPath)
    const segs = dirPath.split('/')
    const name = segs[segs.length - 1]
    const parentPath = segs.slice(0, -1).join('/')
    const parentId = await ensure(parentPath)
    const siblings = await getFolders(workspaceId, parentId)
    const existing = siblings.find((f) => f.name === name)
    const id = existing ? existing.id : (await createFolder(workspaceId, name, parentId)).id
    cache.set(dirPath, id)
    return id
  }
  return ensure
}
