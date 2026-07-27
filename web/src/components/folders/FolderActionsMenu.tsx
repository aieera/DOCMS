import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { MoreVertical, FolderOpen, Pencil, ShieldCheck, Lock, Unlock, Trash2 } from 'lucide-react'

import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator,
} from '@/components/ui/shadcn/dropdown-menu'
import {
  ContextMenu, ContextMenuTrigger, ContextMenuContent, ContextMenuItem, ContextMenuSeparator,
} from '@/components/ui/shadcn/context-menu'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

import { useDeleteFolder, useRenameFolder, useSetFolderVisibility } from '@/hooks/useFolders'
import { ManageFolderAccessDialog } from '@/components/folders/ManageFolderAccessDialog'
import { readErrorMessage } from '@/api/client'
import type { Folder } from '@/types/api'

interface Props {
  folder: Folder
  canManage: boolean
  onOpen: () => void
  children: ReactNode
  /** Positions the ⋯ trigger wrapper — default suits tiles, rows pass a centered anchor. */
  triggerClassName?: string
}

/**
 * Right-click context menu + ⋯ dropdown for a folder tile — the folder
 * analogue of DocumentActionsMenu. Self-contained: owns its rename /
 * manage-access / delete dialogs and the folder mutation hooks.
 */
export function FolderActionsMenu({ folder, canManage, onOpen, children, triggerClassName }: Props) {
  const [renameOpen, setRenameOpen] = useState(false)
  const [accessOpen, setAccessOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const del = useDeleteFolder()
  const setVis = useSetFolderVisibility()
  const isPrivate = folder.visibility === 'private'

  const toggleVisibility = () => {
    setVis.mutate(
      { folderId: folder.id, visibility: isPrivate ? 'shared' : 'private' },
      {
        onSuccess: () => toast.success(isPrivate ? 'Folder is now shared' : 'Folder is now private'),
        onError: (e) => toast.error(readErrorMessage(e) ?? 'Could not change visibility'),
      },
    )
  }

  type Item =
    | { kind: 'action'; key: string; label: string; icon: ReactNode; onSelect: () => void; destructive?: boolean }
    | { kind: 'separator'; key: string }

  const items: Item[] = [
    { kind: 'action', key: 'open', label: 'Open', icon: <FolderOpen className="h-4 w-4" />, onSelect: onOpen },
    ...(canManage
      ? ([
          { kind: 'action', key: 'rename', label: 'Rename', icon: <Pencil className="h-4 w-4" />, onSelect: () => setRenameOpen(true) },
          { kind: 'action', key: 'access', label: 'Manage access…', icon: <ShieldCheck className="h-4 w-4" />, onSelect: () => setAccessOpen(true) },
          {
            kind: 'action', key: 'visibility',
            label: isPrivate ? 'Make shared' : 'Make private',
            icon: isPrivate ? <Unlock className="h-4 w-4" /> : <Lock className="h-4 w-4" />,
            onSelect: toggleVisibility,
          },
          { kind: 'separator', key: 'sep' },
          { kind: 'action', key: 'delete', label: 'Delete', icon: <Trash2 className="h-4 w-4" />, onSelect: () => setConfirmDelete(true), destructive: true },
        ] as Item[])
      : []),
  ]

  const renderItems = (Cmp: typeof ContextMenuItem | typeof DropdownMenuItem, Sep: typeof ContextMenuSeparator | typeof DropdownMenuSeparator) =>
    items.map((it) => it.kind === 'separator'
      ? <Sep key={it.key} />
      : (
        <Cmp
          key={it.key}
          onSelect={it.onSelect}
          className={it.destructive ? 'text-destructive focus:text-destructive' : ''}
          data-testid={`folder-action-${it.key}`}
        >
          {it.icon}<span>{it.label}</span>
        </Cmp>
      ))

  return (
    <div className="group relative">
      <ContextMenu>
        <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
        <ContextMenuContent data-testid="folder-context-menu">
          {renderItems(ContextMenuItem, ContextMenuSeparator)}
        </ContextMenuContent>
      </ContextMenu>

      <div className={triggerClassName ?? 'absolute end-2 top-2'}>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 opacity-0 transition-opacity hover:opacity-100 focus-visible:opacity-100 group-hover:opacity-100 data-[state=open]:opacity-100"
              onClick={(e) => { e.preventDefault(); e.stopPropagation() }}
              aria-label="Folder actions"
            >
              <MoreVertical className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {renderItems(DropdownMenuItem, DropdownMenuSeparator)}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {renameOpen && <RenameFolder folder={folder} onClose={() => setRenameOpen(false)} />}

      <ManageFolderAccessDialog open={accessOpen} onOpenChange={setAccessOpen} folder={folder} />

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete folder?"
        description={`"${folder.name}" will be deleted. The folder must be empty first.`}
        confirmLabel="Delete"
        destructive
        loading={del.isPending}
        onConfirm={() =>
          del.mutate(folder.id, {
            onSuccess: () => { toast.success('Folder deleted'); setConfirmDelete(false) },
            onError: (e) => {
              const msg = readErrorMessage(e) ?? ''
              toast.error(msg.includes('not empty') ? 'Folder isn’t empty — remove its contents first' : (msg || 'Could not delete folder'))
            },
          })
        }
      />
    </div>
  )
}

function RenameFolder({ folder, onClose }: { folder: Folder; onClose: () => void }) {
  // Mounted fresh each time the dialog opens (rendered behind renameOpen), so
  // the initializer is enough — no prop-sync effect needed.
  const [name, setName] = useState(folder.name)
  const rename = useRenameFolder()

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n) { toast.error('Name is required'); return }
    rename.mutate(
      { folderId: folder.id, name: n },
      {
        onSuccess: () => { toast.success('Folder renamed'); onClose() },
        onError: (err) => toast.error(readErrorMessage(err) ?? 'Rename failed'),
      },
    )
  }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }} title="Rename folder" description="Letters, numbers, spaces, hyphens and underscores.">
      <form onSubmit={submit} className="space-y-4">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} autoFocus maxLength={120} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose} disabled={rename.isPending}>Cancel</Button>
          <Button type="submit" loading={rename.isPending} disabled={rename.isPending}>Rename</Button>
        </div>
      </form>
    </Dialog>
  )
}
