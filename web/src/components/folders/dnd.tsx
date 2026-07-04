// Drag-and-drop primitives for the workspace browser (folders + files).
// dnd-kit wrappers kept here so the grid/tree components stay readable.
//
// Move endpoints already exist (moveDocument, updateFolder w/ new parent);
// this is pure wiring. A grip handle is the drag activator so click-to-select
// and double-click-to-open on the tile itself keep working.
import type { ReactNode } from 'react'
import { useDraggable, useDroppable } from '@dnd-kit/core'
import { GripVertical } from 'lucide-react'
import { cn } from '@/lib/cn'

export type DragData = { kind: 'doc' | 'folder'; id: string; label: string }
export type DropData = { folderId: string }

// DraggableTile wraps a grid tile: the whole tile is the drag node (moves
// with the pointer at reduced opacity) but only the grip activates the drag.
export function DraggableTile({ dndId, data, children }: { dndId: string; data: DragData; children: ReactNode }) {
  const { listeners, attributes, setNodeRef, setActivatorNodeRef, isDragging } = useDraggable({ id: dndId, data })
  return (
    <div ref={setNodeRef} className={cn('group/tile relative', isDragging && 'opacity-40')}>
      <button
        ref={setActivatorNodeRef}
        {...listeners}
        {...attributes}
        type="button"
        onClick={(e) => e.stopPropagation()}
        aria-label={`Drag ${data.label} to move it`}
        title="Drag to move"
        className="absolute end-2 top-2 z-20 cursor-grab touch-none rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-accent group-hover/tile:opacity-100 focus-visible:opacity-100 active:cursor-grabbing"
        data-testid={`drag-${data.kind}-${data.id}`}
      >
        <GripVertical className="h-4 w-4" />
      </button>
      {children}
    </div>
  )
}

// DroppableFolder marks a folder tile as a drop target. ring-primary = an
// accepted drop; ring-red + not-allowed = an invalid one (dropping a folder
// onto itself). The server still enforces descendant/cycle/depth rules, which
// surface as a rejection → UI rollback.
export function DroppableFolder({ folderId, children }: { folderId: string; children: ReactNode }) {
  const { setNodeRef, isOver, active } = useDroppable({ id: `drop-folder:${folderId}`, data: { folderId } as DropData })
  const dragged = active?.data.current as DragData | undefined
  const invalid = isOver && dragged?.kind === 'folder' && dragged.id === folderId
  const valid = isOver && !invalid
  return (
    <div
      ref={setNodeRef}
      className={cn(
        'rounded-2xl transition-shadow',
        valid && 'ring-2 ring-primary ring-offset-2 ring-offset-background',
        invalid && 'cursor-not-allowed ring-2 ring-red-400',
      )}
    >
      {children}
    </div>
  )
}

// DragOverlayContent renders the floating preview under the cursor — the
// single item's label, or "N items" when a multi-selection is dragged.
export function DragOverlayContent({ label, count }: { label: string; count: number }) {
  return (
    <div className="pointer-events-none rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium shadow-lg">
      {count > 1 ? `${count} items` : label}
    </div>
  )
}
