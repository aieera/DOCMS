import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useDroppable } from '@dnd-kit/core'
import { ChevronRight, Trash2 } from 'lucide-react'

import { cn } from '@/lib/cn'
import { formatFileSize } from '@/lib/formatters'
import { useFolders } from '@/hooks/useFolders'
import { findRootFolder } from '@/lib/rootFolder'
import { FolderGlyph } from '@/components/folders/BrowserTiles'
import type { Folder, Workspace } from '@/types/api'

interface Props {
  workspaceId: string
  currentFolderId: string | null
  onNavigate: (folderId: string | null) => void
  workspace?: Workspace
  /** Sum of the sizes of the files currently in view — drives the soft
   *  storage bar (there's no per-tenant quota API yet, so this is a real
   *  "in this folder" figure against a soft 2 GB reference). */
  usedBytes: number
}

const SOFT_CAP = 2 * 1024 ** 3 // 2 GB reference for the bar

export function BrowserTreeSidebar({ workspaceId, currentFolderId, onNavigate, workspace, usedBytes }: Props) {
  const pct = Math.min(100, Math.round((usedBytes / SOFT_CAP) * 100))
  return (
    <aside className="hidden w-[252px] flex-none flex-col border-e border-border lg:flex">
      <div className="flex items-center justify-between px-5 pb-3 pt-5">
        <span className="text-[11px] font-bold uppercase tracking-[0.12em] text-muted-foreground">Folders</span>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-3">
        <button
          type="button"
          onClick={() => onNavigate(null)}
          className={cn(
            'mb-1 flex h-10 w-full items-center gap-2.5 rounded-xl px-2.5 text-sm font-semibold transition-colors',
            currentFolderId === null ? 'bg-primary/10 text-primary' : 'text-foreground hover:bg-accent/40',
          )}
        >
          <FolderGlyph size={22} />
          <span className="truncate">{workspace?.name ?? 'All files'}</span>
        </button>
        <TreeChildren workspaceId={workspaceId} parentId={undefined} currentFolderId={currentFolderId} onNavigate={onNavigate} depth={0} />
      </div>

      <div className="border-t border-border p-3">
        <Link
          to="/trash"
          search={{ workspace: workspaceId }}
          className="mb-3 flex h-10 items-center gap-2.5 rounded-xl px-2.5 text-sm font-semibold text-foreground transition-colors hover:bg-accent/40"
        >
          <Trash2 className="h-[18px] w-[18px] text-muted-foreground" />
          <span>Trash</span>
        </Link>
        <div className="px-2.5">
          <div className="mb-2 flex items-baseline justify-between">
            <span className="text-[13px] font-semibold text-foreground">Storage</span>
            <span className="text-xs font-semibold text-muted-foreground tabular-nums">{formatFileSize(usedBytes)}</span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-muted">
            <span
              className="block h-full rounded-full bg-primary transition-[width] duration-500"
              style={{ width: `${Math.max(4, pct)}%` }}
            />
          </div>
        </div>
      </div>
    </aside>
  )
}

function TreeChildren({
  workspaceId, parentId, currentFolderId, onNavigate, depth,
}: { workspaceId: string; parentId: string | undefined; currentFolderId: string | null; onNavigate: (id: string | null) => void; depth: number }) {
  const { data, isLoading } = useFolders(workspaceId, parentId)
  // At the top level, hide the designated root folder — the browser shows
  // its files as the workspace root itself, so a "Root" node here would
  // duplicate the workspace entry above.
  const fetched = data ?? []
  const folders = parentId ? fetched : fetched.filter((f) => f.id !== findRootFolder(fetched)?.id)
  if (isLoading) {
    return <div className={cn('space-y-1.5 py-1', depth > 0 && 'ms-5 border-s border-dashed border-border ps-3')}>{[0, 1].map((i) => <div key={i} className="h-7 animate-pulse rounded-lg bg-muted/60" />)}</div>
  }
  if (folders.length === 0) {
    return depth === 0 ? <p className="px-3 py-2 text-xs text-muted-foreground">No folders yet.</p> : null
  }
  return (
    <div className={cn(depth > 0 && 'ms-[21px] border-s border-dashed border-border ps-3')}>
      {folders.map((f) => (
        <TreeNode key={f.id} workspaceId={workspaceId} folder={f} currentFolderId={currentFolderId} onNavigate={onNavigate} depth={depth} />
      ))}
    </div>
  )
}

function TreeNode({
  workspaceId, folder, currentFolderId, onNavigate, depth,
}: { workspaceId: string; folder: Folder; currentFolderId: string | null; onNavigate: (id: string | null) => void; depth: number }) {
  const [open, setOpen] = useState(false)
  const hasChildren = (folder.child_folder_count ?? folder.children_count ?? 0) > 0
  const active = folder.id === currentFolderId
  // Drop target: a dragged doc/folder can land on this tree node. The dragged
  // item rides in active.data; a folder dropped on itself is invalid.
  const { setNodeRef, isOver, active: dragActive } = useDroppable({ id: `tree-drop:${folder.id}`, data: { folderId: folder.id } })
  const dragged = dragActive?.data.current as { kind?: string; id?: string } | undefined
  const invalidDrop = isOver && dragged?.kind === 'folder' && dragged.id === folder.id
  const validDrop = isOver && !invalidDrop
  // Auto-expand on hover: pause ~700ms over a collapsed node mid-drag, then
  // reveal its children so you can drill into a deep target without dropping.
  const expandTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => {
    if (isOver && hasChildren && !open) {
      expandTimer.current = setTimeout(() => setOpen(true), 700)
      return () => { if (expandTimer.current) clearTimeout(expandTimer.current) }
    }
  }, [isOver, hasChildren, open])
  return (
    <div>
      <div
        ref={setNodeRef}
        role="button"
        tabIndex={0}
        aria-current={active ? 'page' : undefined}
        // role="button" (not <button>) because this row contains the
        // expand <button> and nesting buttons is invalid HTML. A real
        // button responds to Enter AND Space, so we mirror that here —
        // previously only Enter worked (WCAG 2.1.1 keyboard).
        onClick={() => onNavigate(folder.id)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onNavigate(folder.id)
          }
        }}
        className={cn(
          'flex h-10 cursor-pointer items-center gap-2 rounded-xl px-2 text-sm transition-colors',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
          active ? 'bg-primary/10 font-semibold text-primary' : 'font-medium text-foreground hover:bg-accent/40',
          validDrop && 'ring-2 ring-primary',
          invalidDrop && 'cursor-not-allowed ring-2 ring-red-400',
        )}
      >
        {hasChildren ? (
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); setOpen((o) => !o) }}
            className="grid h-5 w-5 flex-none place-items-center rounded text-muted-foreground hover:text-foreground"
            aria-label={open ? 'Collapse' : 'Expand'}
            aria-expanded={open}
          >
            <ChevronRight className={cn('h-4 w-4 transition-transform', open && 'rotate-90')} />
          </button>
        ) : (
          <span className="h-5 w-5 flex-none" />
        )}
        <FolderGlyph size={20} />
        <span className="truncate">{folder.name}</span>
      </div>
      {open && hasChildren && (
        <TreeChildren workspaceId={workspaceId} parentId={folder.id} currentFolderId={currentFolderId} onNavigate={onNavigate} depth={depth + 1} />
      )}
    </div>
  )
}
