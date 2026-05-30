import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight, Folder, FolderOpen } from 'lucide-react'
import { toast } from 'sonner'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { useCopyDocument, useMoveDocument } from '@/hooks/useDocuments'
import { getFolders } from '@/api/workspaces'
import { cn } from '@/lib/cn'
import type { Folder as FolderType } from '@/types/api'

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  documentId: string
  workspaceId: string
  currentFolderId?: string
  // mode controls the verb + endpoint. 'move' is the legacy default;
  // 'copy' calls /documents/{id}/copy and creates a new row in the
  // target folder. The picker UI is identical for both — only the
  // submit button label, mutation, and the disabledId-vs-no-disable
  // rule differ (a copy back to the current folder is still legal,
  // it just produces a clone). Defaults to 'move' for back-compat.
  mode?: 'move' | 'copy'
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
    enabled: expanded && (folder.child_folder_count ?? folder.children_count ?? 0) > 0,
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
        {(folder.child_folder_count ?? folder.children_count ?? 0) > 0 ? (
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

export function MoveDocumentDialog({
  open, onOpenChange, documentId, workspaceId, currentFolderId, mode = 'move',
}: Props) {
  const [selected, setSelected] = useState<string | undefined>(undefined)
  const move = useMoveDocument()
  const copy = useCopyDocument()
  const isCopy = mode === 'copy'
  const pending = isCopy ? copy.isPending : move.isPending

  const roots = useQuery({
    queryKey: ['folders', workspaceId, 'root'],
    queryFn: () => getFolders(workspaceId),
    enabled: open && !!workspaceId,
  })

  // Move can't target the current folder (would be a no-op); Copy CAN
  // target the current folder (produces a same-folder duplicate).
  const canSubmit =
    !pending &&
    !!selected &&
    (isCopy || selected !== currentFolderId)

  const submit = () => {
    if (!canSubmit || !selected) return
    const finalize = () => {
      onOpenChange(false)
      setSelected(undefined)
    }
    if (isCopy) {
      copy.mutate(
        { id: documentId, folderId: selected },
        {
          onSuccess: () => {
            toast.success('Document copied')
            finalize()
          },
        },
      )
    } else {
      move.mutate(
        { id: documentId, folderId: selected },
        {
          onSuccess: () => {
            toast.success('Document moved')
            finalize()
          },
        },
      )
    }
  }

  const title = isCopy ? 'Copy document' : 'Move document'
  const submitLabel = isCopy ? 'Copy' : 'Move'

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={title} size="md">
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
                // Move disables the current folder; Copy doesn't.
                disabledId={isCopy ? undefined : currentFolderId}
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
            disabled={pending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            onClick={submit}
            disabled={!canSubmit}
            loading={pending}
            data-testid={`${mode}-document-submit`}
          >
            {submitLabel}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
