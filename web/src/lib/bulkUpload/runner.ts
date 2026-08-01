import { readErrorMessage } from '@/api/client'
import { ensureWorkspace, makeFolderEnsurer, resolveRootFolder } from './folders'
import { uploadFileToFolder } from './uploadFile'
import { dirSegments } from './paths'
import type { Destination, ConflictPolicy, ImportResult, PathFileMap } from './types'

export type RunEvent = {
  relPath: string
  state: 'uploading' | 'done' | 'failed'
  pct?: number
  reason?: string
}

/**
 * Execute a bulk import: ensure the destination workspace, then upload each file
 * — creating its folder path on demand (idempotent, cached) — via a
 * concurrency-limited queue. Per-file failures are recorded and the batch
 * continues; a per-file progress event is emitted for the UI.
 *
 * NOTE (Phase 1): conflict policy is recorded but always creates a new document;
 * true new_version/skip/rename needs a find-document-by-path query (see plan
 * self-review). SHA dedup still applies via the pipeline.
 */
export async function runImport(args: {
  map: PathFileMap
  destination: Destination
  conflict: ConflictPolicy
  concurrency?: number
  onEvent?: (e: RunEvent) => void
}): Promise<ImportResult> {
  const { map, destination, onEvent } = args
  const concurrency = args.concurrency ?? 4

  const workspaceId = await ensureWorkspace(destination)
  // Root-level files need a real folder id (the endpoint rejects a null folder),
  // so resolve/auto-create the workspace root before building the ensurer.
  const rootFolderId = await resolveRootFolder(workspaceId, destination.targetFolderId)
  const ensureFolder = makeFolderEnsurer(workspaceId, rootFolderId)
  const result: ImportResult = { created: 0, skipped: 0, failed: 0, items: [] }

  const entries = [...map.entries()]
  let cursor = 0

  async function worker() {
    while (cursor < entries.length) {
      const [relPath, file] = entries[cursor++]
      try {
        onEvent?.({ relPath, state: 'uploading' })
        const folderId = await ensureFolder(dirSegments(relPath).join('/'))
        const r = await uploadFileToFolder({ file, title: file.name, workspaceId, folderId })
        result.created++
        result.items.push({ relPath, outcome: 'created', documentId: r.documentId })
        onEvent?.({ relPath, state: 'done' })
      } catch (e) {
        // Surface the backend's real reason (e.g. "file type not allowed"),
        // not the opaque "Request failed with status code 400".
        const reason = readErrorMessage(e) ?? (e instanceof Error ? e.message : String(e))
        result.failed++
        result.items.push({ relPath, outcome: 'failed', reason })
        onEvent?.({ relPath, state: 'failed', reason })
      }
    }
  }

  const workers = Math.max(1, Math.min(concurrency, entries.length))
  await Promise.all(Array.from({ length: workers }, worker))
  return result
}
