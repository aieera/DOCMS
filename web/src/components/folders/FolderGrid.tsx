import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Folder as FolderIcon,
  MoreHorizontal,
  Pencil,
  Trash2,
  FolderOpen,
  Lock,
  Unlock,
  UserPlus,
} from 'lucide-react'
import { cn } from '@/lib/cn'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/shadcn/dialog'
import { ManageFolderAccessDialog } from './ManageFolderAccessDialog'
import type { Folder } from '@/types/api'

interface Props {
  folders: Folder[]
  isLoading?: boolean
  onOpen: (folder: Folder) => void
  onRename?: (folder: Folder, name: string) => void
  onDelete?: (folder: Folder) => void
  // onSetVisibility flips shared↔private. Backend re-asserts owner/admin
  // gating; the menu just routes the click.
  onSetVisibility?: (folder: Folder, visibility: 'shared' | 'private') => void
  // canManage = caller has rename/delete permission on these folders
  // (admins always; owners on their own folders). Hides the ⋮ menu
  // entirely when false.
  canManage?: boolean
  // dropTargetId — used by the drag-drop pass (Phase 3) to ring the
  // folder currently under the drag pointer. nil = no drag in flight.
  dropTargetId?: string | null
}

// FolderGrid renders the folders that live inside the current
// location. Empty array = nothing rendered (the workspace page shows
// its own empty state above the document list).
export function FolderGrid({
  folders,
  isLoading,
  onOpen,
  onRename,
  onDelete,
  onSetVisibility,
  canManage = false,
  dropTargetId = null,
}: Props) {
  const { t } = useTranslation('folders')
  const [renameFor, setRenameFor] = useState<Folder | null>(null)
  const [deleteFor, setDeleteFor] = useState<Folder | null>(null)
  const [flipFor, setFlipFor] = useState<Folder | null>(null)
  const [accessFor, setAccessFor] = useState<Folder | null>(null)

  if (isLoading) {
    return (
      <ul
        className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
        aria-busy="true"
      >
        {Array.from({ length: 4 }).map((_, i) => (
          <li
            key={i}
            className="h-20 animate-pulse rounded-xl border border-border bg-muted/40"
          />
        ))}
      </ul>
    )
  }
  if (folders.length === 0) return null
  return (
    <>
      <ul
        className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
        data-testid="folder-grid"
      >
        {folders.map((f) => (
          <li key={f.id}>
            <FolderCard
              folder={f}
              onOpen={() => onOpen(f)}
              onRename={canManage && onRename ? () => setRenameFor(f) : undefined}
              onDelete={canManage && onDelete ? () => setDeleteFor(f) : undefined}
              onFlipVisibility={
                canManage && onSetVisibility ? () => setFlipFor(f) : undefined
              }
              onManageAccess={
                canManage && f.visibility === 'private' ? () => setAccessFor(f) : undefined
              }
              isDropTarget={dropTargetId === f.id}
            />
          </li>
        ))}
      </ul>

      {renameFor && (
        <RenameFolderDialog
          folder={renameFor}
          onOpenChange={(open) => { if (!open) setRenameFor(null) }}
          onConfirm={(name) => {
            onRename?.(renameFor, name)
            setRenameFor(null)
          }}
        />
      )}

      {deleteFor && (
        <ConfirmDialog
          open={!!deleteFor}
          onOpenChange={(o) => !o && setDeleteFor(null)}
          title={t('delete_confirm.title')}
          description={t('delete_confirm.description', { name: deleteFor.name })}
          confirmLabel={t('delete_confirm.confirm')}
          destructive
          onConfirm={() => {
            onDelete?.(deleteFor)
            setDeleteFor(null)
          }}
        />
      )}

      {flipFor && (() => {
        const target: 'shared' | 'private' =
          flipFor.visibility === 'private' ? 'shared' : 'private'
        const keyBase =
          target === 'private'
            ? 'visibility.flip_to_private_confirm'
            : 'visibility.flip_to_shared_confirm'
        return (
          <ConfirmDialog
            open={!!flipFor}
            onOpenChange={(o) => !o && setFlipFor(null)}
            title={t(`${keyBase}.title`)}
            description={t(`${keyBase}.description`)}
            confirmLabel={t(`${keyBase}.confirm`)}
            onConfirm={() => {
              onSetVisibility?.(flipFor, target)
              setFlipFor(null)
            }}
          />
        )
      })()}

      {accessFor && (
        <ManageFolderAccessDialog
          open={!!accessFor}
          onOpenChange={(o) => !o && setAccessFor(null)}
          folder={accessFor}
        />
      )}
    </>
  )
}

interface CardProps {
  folder: Folder
  onOpen: () => void
  onRename?: () => void
  onDelete?: () => void
  onFlipVisibility?: () => void
  onManageAccess?: () => void
  isDropTarget?: boolean
}

function FolderCard({
  folder,
  onOpen,
  onRename,
  onDelete,
  onFlipVisibility,
  onManageAccess,
  isDropTarget,
}: CardProps) {
  const { t } = useTranslation('folders')
  // Coerce with Number(): the API serializes these counts (Postgres bigint)
  // as strings, so a bare `+` string-concatenates "0" + "0" → "00", which then
  // fails the `=== 0` check and renders "00 items".
  const childCount = Number(folder.child_folder_count ?? 0)
  const docCount = Number(folder.document_count ?? 0)
  const total = childCount + docCount
  const isPrivate = folder.visibility === 'private'
  return (
    <div
      role="group"
      data-folder-id={folder.id}
      className={cn(
        'group relative flex items-start gap-3 rounded-xl border bg-card p-3 transition-all',
        'cursor-pointer hover:border-primary/40 hover:bg-muted/40',
        isDropTarget
          ? 'border-primary ring-2 ring-primary/40 ring-offset-2 ring-offset-background'
          : 'border-border',
      )}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return // portal-bubbled (dialog/menu) keys are not activation
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen()
        }
      }}
      tabIndex={0}
      aria-label={t('card_aria_label', { name: folder.name })}
    >
      <span
        className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary"
        aria-hidden
      >
        <FolderIcon className="h-5 w-5" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <h3 className="truncate text-sm font-semibold" title={folder.name}>
            {folder.name}
          </h3>
          {isPrivate && (
            <span
              className="inline-flex items-center gap-0.5 rounded-full bg-warning/15 px-1.5 py-0 text-[10px] font-medium text-warning-strong"
              title={t('private_tooltip')}
            >
              <Lock className="h-3 w-3" />
              {t('private_label')}
            </span>
          )}
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {total === 0
            ? t('empty_folder')
            : t('item_count', {
                count: total,
                defaultValue: total === 1 ? '1 item' : `${total} items`,
              })}
        </p>
      </div>
      {(onRename || onDelete || onFlipVisibility || onManageAccess) && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="h-9 w-9 shrink-0 text-muted-foreground opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
              onClick={(e) => e.stopPropagation()}
              aria-label={t('actions_aria_label')}
              data-testid={`folder-menu-${folder.id}`}
            >
              <MoreHorizontal className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" onClick={(e) => e.stopPropagation()}>
            <DropdownMenuItem onSelect={onOpen}>
              <FolderOpen className="me-2 h-4 w-4" />
              {t('actions.open')}
            </DropdownMenuItem>
            {onRename && (
              <DropdownMenuItem onSelect={onRename}>
                <Pencil className="me-2 h-4 w-4" />
                {t('actions.rename')}
              </DropdownMenuItem>
            )}
            {(onFlipVisibility || onManageAccess) && <DropdownMenuSeparator />}
            {onFlipVisibility && (
              <DropdownMenuItem
                onSelect={onFlipVisibility}
                data-testid={`folder-flip-visibility-${folder.id}`}
              >
                {folder.visibility === 'private' ? (
                  <>
                    <Unlock className="me-2 h-4 w-4" />
                    {t('actions.make_shared')}
                  </>
                ) : (
                  <>
                    <Lock className="me-2 h-4 w-4" />
                    {t('actions.make_private')}
                  </>
                )}
              </DropdownMenuItem>
            )}
            {onManageAccess && (
              <DropdownMenuItem
                onSelect={onManageAccess}
                data-testid={`folder-manage-access-${folder.id}`}
              >
                <UserPlus className="me-2 h-4 w-4" />
                {t('actions.manage_access')}
              </DropdownMenuItem>
            )}
            {onDelete && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onSelect={onDelete}
                  className="text-destructive focus:bg-destructive/10 focus:text-destructive"
                >
                  <Trash2 className="me-2 h-4 w-4" />
                  {t('actions.delete')}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  )
}

function RenameFolderDialog({
  folder,
  onOpenChange,
  onConfirm,
}: {
  folder: Folder
  onOpenChange: (open: boolean) => void
  onConfirm: (name: string) => void
}) {
  const { t } = useTranslation('folders')
  const [name, setName] = useState(folder.name)
  const trimmed = name.trim()
  const canSave = trimmed.length > 0 && trimmed !== folder.name
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('rename_dialog.title')}</DialogTitle>
          <DialogDescription>{t('rename_dialog.name_label')}</DialogDescription>
        </DialogHeader>
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          onKeyDown={(e) => {
            if (e.key === 'Enter' && canSave) onConfirm(trimmed)
          }}
          aria-label={t('rename_dialog.name_label')}
        />
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('actions.cancel')}
          </Button>
          <Button onClick={() => onConfirm(trimmed)} disabled={!canSave}>
            {t('actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
