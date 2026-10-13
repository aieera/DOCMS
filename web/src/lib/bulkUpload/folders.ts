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
 * Resolve the folder that root-level files should land in. The upload/create
 * endpoints require a non-nil folder_id even at the workspace root, so we can't
 * pass undefined (that 400s). Prefer an explicit target folder; otherwise use
 * the workspace's root folder, auto-creating a "Root" folder for legacy
 * workspaces that have none. Mirrors src/hooks/useUpload.ts.
 */
export async function resolveRootFolder(
  workspaceId: string,
  targetFolderId?: string,
): Promise<string | undefined> {
  if (targetFolderId) return targetFolderId
  const folders = await getFolders(workspaceId)
  const root = folders.find((f) => !f.parent_folder_id && !f.parent_id)
  if (root) return root.id
  if (folders.length === 0) {
    try {
      return (await createFolder(workspaceId, 'Root')).id
    } catch {
      // fall through — the upload will surface a clearer error
    }
  }
  return undefined
}

/**
 * Returns an idempotent folder ensurer for one workspace. `ensure(dirPath)`
 * resolves (or creates) every segment of dirPath under the optional root folder,
 * reusing an existing same-name child when present.
 *
 * W-3: the cache holds the in-flight PROMISE per path, not the resolved id.
 * The runner drives 4 workers, and caching only after resolution let two
 * files in the same new directory both miss the cache, both see "no
 * sibling", and both create it — duplicate sibling folders. A rejected
 * promise is evicted so a later file can retry the path.
 */
export function makeFolderEnsurer(workspaceId: string, rootFolderId?: string) {
  const cache = new Map<string, Promise<string | undefined>>()
  cache.set('', Promise.resolve(rootFolderId))

  async function resolvePath(dirPath: string): Promise<string | undefined> {
    const segs = dirPath.split('/')
    const name = segs[segs.length - 1]
    const parentPath = segs.slice(0, -1).join('/')
    const parentId = await ensure(parentPath)
    const siblings = await getFolders(workspaceId, parentId)
    const existing = siblings.find((f) => f.name === name)
    return existing ? existing.id : (await createFolder(workspaceId, name, parentId)).id
  }

  function ensure(dirPath: string): Promise<string | undefined> {
    const hit = cache.get(dirPath)
    if (hit) return hit
    const p = resolvePath(dirPath)
    cache.set(dirPath, p)
    p.catch(() => {
      if (cache.get(dirPath) === p) cache.delete(dirPath)
    })
    return p
  }
  return ensure
}
