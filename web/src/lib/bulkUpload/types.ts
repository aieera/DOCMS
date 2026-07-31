// Shared, engine-agnostic contract for bulk content upload (ZIP / folder →
// workspace tree). The same PlannedItem / ImportResult shapes are produced by
// the Phase-1 client-side engine and (later) a Phase-2 server engine, so the
// wizard UI never has to change. See docs/superpowers/specs/2026-07-31-bulk-upload-design.md.

export type ConflictPolicy = 'new_version' | 'skip' | 'rename'

/** relPath (sanitized, forward-slash, no leading '/') → the file to upload. */
export type PathFileMap = Map<string, File>

export interface Destination {
  mode: 'new' | 'existing'
  /** existing target workspace (mode='existing') */
  workspaceId?: string
  /** name for a workspace to create (mode='new') */
  newWorkspaceName?: string
  /** optional folder under the workspace to import the tree into */
  targetFolderId?: string
}

export interface PlannedItem {
  relPath: string
  type: 'folder' | 'file'
  sizeBytes?: number
  mimeType?: string
  status: 'ok' | 'blocked_type' | 'oversize' | 'empty'
  note?: string
}

export interface ImportResultItem {
  relPath: string
  outcome: 'created' | 'versioned' | 'skipped' | 'renamed' | 'failed'
  reason?: string
  documentId?: string
}

export interface ImportResult {
  created: number
  skipped: number
  failed: number
  items: ImportResultItem[]
}
