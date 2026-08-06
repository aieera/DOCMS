import type { Folder } from '@/types/api'

/** Pick the workspace's designated root folder from a top-level folder list.
 *
 * Documents always live inside a folder (CreateDocument requires a
 * folder_id), so "the workspace root" is a convention: the parentless
 * container whose contents the root view shows and where root-level uploads
 * land. Preference order keeps it deterministic across workspace vintages:
 * the auto-created "Root" (legacy upload fallback), then the seeded
 * "Shared Documents", then the oldest parentless folder. Previously the
 * upload path took *whichever parentless folder came first*, which could
 * silently file root uploads into a sibling like "Quotes".
 */
export function findRootFolder<T extends Pick<Folder, 'id' | 'name'> & {
  parent_id?: string | null
  parent_folder_id?: string | null
  created_at?: string
}>(folders: T[]): T | undefined {
  const topLevel = folders.filter((f) => !f.parent_folder_id && !f.parent_id)
  if (topLevel.length === 0) return undefined
  return (
    topLevel.find((f) => f.name === 'Root')
    ?? topLevel.find((f) => f.name === 'Shared Documents')
    ?? [...topLevel].sort((a, b) => (a.created_at ?? '').localeCompare(b.created_at ?? ''))[0]
  )
}
