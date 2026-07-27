// Drag-and-drop primitives for the workspace browser (folders + files).
// dnd-kit wrappers kept here so the grid/tree components stay readable.
//
// Move endpoints already exist (moveDocument, updateFolder w/ new parent);
// this is pure wiring. The whole item drags (no grip) — the pointer
// sensor's distance constraint keeps click-to-select and
// double-click-to-open working.
import type { ReactNode } from 'react'
import { useDraggable, useDroppable } from '@dnd-kit/core'
import { cn } from '@/lib/cn'

export type DragData = { kind: 'doc' | 'folder'; id: string; label: string }
export type DropData = { folderId: string }

// DraggableTile wraps a grid tile or list row: the whole item is both the
// drag node and the activator (Explorer-style — no separate grip). The
// PointerSensor's distance activation constraint (see the page's
// useSensors) is what keeps click-to-select and double-click-to-open
// working: a drag only starts after real pointer travel.
export function DraggableTile({ dndId, data, children }: { dndId: string; data: DragData; children: ReactNode }) {
  const { listeners, setNodeRef, isDragging } = useDraggable({ id: dndId, data })
  return (
    <div
      ref={setNodeRef}
      {...listeners}
      className={cn('group/tile relative', isDragging && 'opacity-40')}
      data-testid={`drag-${data.kind}-${data.id}`}
    >
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
