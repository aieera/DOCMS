import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight, Folder, FolderOpen } from 'lucide-react'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { useMoveDocument } from '@/hooks/useDocuments'
import { getFolders } from '@/api/workspaces'
import { cn } from '@/lib/cn'
import type { Folder as FolderType } from '@/types/api'

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  documentId: string
  workspaceId: string
  currentFolderId?: string
}

// Picker walks the folder tree lazily — each expanded folder fires a
// separate query keyed by parentId, mirroring FolderTree's pattern.
function FolderRow({
  folder,
  workspaceId,
  depth,
  selectedId,
  onSelect,
  disabledId,
}: {
  folder: FolderType
  workspaceId: string
  depth: number
  selectedId?: string
  onSelect: (id: string) => void
  disabledId?: string
}) {
  const [expanded, setExpanded] = useState(false)
  const children = useQuery({
    queryKey: ['folders', workspaceId, folder.id],
    queryFn: () => getFolders(workspaceId, folder.id),
    enabled: expanded && folder.children_count > 0,
  })

  const selected = selectedId === folder.id
  const disabled = disabledId === folder.id

  return (
    <div>
      <div
        className={cn(
          'flex items-center gap-1 rounded-md px-2 py-1 text-sm',
          selected && 'bg-[var(--color-accent)] text-[var(--color-primary)] font-medium',
          !disabled && 'cursor-pointer hover:bg-slate-100 dark:hover:bg-slate-800',
          disabled && 'opacity-50',
        )}
        style={{ paddingInlineStart: `${depth * 16 + 8}px` }}
        onClick={() => { if (!disabled) onSelect(folder.id) }}
        data-testid={`move-picker-folder-${folder.id}`}
      >
        {folder.children_count > 0 ? (
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); setExpanded((v) => !v) }}
            className="rounded p-0.5 hover:bg-slate-200 dark:hover:bg-slate-700"
            aria-label={expanded ? 'Collapse' : 'Expand'}
          >
            <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', expanded && 'rotate-90')} />
          </button>
        ) : (
          <span className="inline-block w-4" />
        )}
        {expanded ? <FolderOpen className="h-4 w-4" /> : <Folder className="h-4 w-4" />}
        <span className="truncate">{folder.name}</span>
        {disabled && <span className="ms-auto text-xs text-muted-foreground">current</span>}
      </div>
      {expanded && (
        children.isLoading ? (
          <div className="ps-8 py-1"><Skeleton className="h-4 w-32" /></div>
        ) : children.data?.length ? (
          children.data.map((c) => (
            <FolderRow
              key={c.id}
              folder={c}
              workspaceId={workspaceId}
              depth={depth + 1}
              selectedId={selectedId}
              onSelect={onSelect}
              disabledId={disabledId}
            />
          ))
        ) : null
      )}
    </div>
  )
}

export function MoveDocumentDialog({ open, onOpenChange, documentId, workspaceId, currentFolderId }: Props) {
  const [selected, setSelected] = useState<string | undefined>(undefined)
  const move = useMoveDocument()

  const roots = useQuery({
    queryKey: ['folders', workspaceId, 'root'],
    queryFn: () => getFolders(workspaceId),
    enabled: open && !!workspaceId,
  })

  // MoveDocument requires a valid folder UUID on the backend — see
  // services/document/internal/handler/handler.go:207 (parseUUID on
  // target_folder_id). There's no "workspace root" target, so the
  // picker only offers existing folders and disables the current one.
  const canSubmit =
    !move.isPending &&
    !!selected &&
    selected !== currentFolderId

  const submit = () => {
    if (!canSubmit || !selected) return
    move.mutate(
      { id: documentId, folderId: selected },
      { onSuccess: () => { onOpenChange(false); setSelected(undefined) } },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Move document" size="md">
      <div className="space-y-4">
        <div
          className="max-h-80 overflow-y-auto rounded-md border border-border bg-background p-2"
          data-testid="move-picker-tree"
        >
          {roots.isLoading ? (
            <div className="space-y-1 px-2 py-1">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-4 w-32" />
            </div>
          ) : roots.isError ? (
            <p className="px-2 py-2 text-sm text-destructive">Could not load folders.</p>
          ) : roots.data && roots.data.length > 0 ? (
            roots.data.map((f) => (
              <FolderRow
                key={f.id}
                folder={f}
                workspaceId={workspaceId}
                depth={0}
                selectedId={selected}
                onSelect={setSelected}
                disabledId={currentFolderId}
              />
            ))
          ) : (
            <p className="px-2 py-2 text-xs text-muted-foreground">No folders in this workspace.</p>
          )}
        </div>

        <div className="flex justify-end gap-2">
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={move.isPending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            onClick={submit}
            disabled={!canSubmit}
            loading={move.isPending}
            data-testid="move-document-submit"
          >
            Move
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
