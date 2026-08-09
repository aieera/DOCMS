import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import {
  MoreVertical, Download, Pencil, FolderInput, Share2, Trash2,
  Copy, ListTodo, ShieldCheck, Eye, Lock,
} from 'lucide-react'

import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/shadcn/dropdown-menu'
import {
  ContextMenu, ContextMenuTrigger, ContextMenuContent, ContextMenuItem,
  ContextMenuSeparator,
} from '@/components/ui/shadcn/context-menu'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { Button } from '@/components/ui/shadcn/button'

import { useDeleteDocument } from '@/hooks/useDocuments'
import { getVersions, getDownloadURL } from '@/api/documents'
import { readErrorMessage } from '@/api/client'

import { RenameDocumentDialog } from './RenameDocumentDialog'
import { MoveDocumentDialog } from './MoveDocumentDialog'
import { ShareDialog } from './ShareDialog'
import { ProtectShareDialog } from './ProtectShareDialog'
import { TaskCreateDialog } from '@/components/tasks/TaskCreateDialog'
import { ManageAccessDialog } from './ManageAccessDialog'

import type { Document } from '@/types/api'

/**
 * A surface-specific action appended after the shared core actions
 * (e.g. "Compare with…", which only makes sense on the detail page).
 * Kept as data so both the dropdown and the context menu render it
 * through the same path as everything else.
 */
export interface ExtraAction {
  key: string
  label: string
  icon: ReactNode
  onSelect: () => void
}

interface Props {
  doc: Document
  /**
   * The card surface to wrap. Right-clicking anywhere inside fires the
   * ContextMenu; the ⋯-button is positioned absolutely on top-right.
   * Only used by variant="card".
   */
  children?: ReactNode
  /**
   * Opens the document viewer. When provided, an "Open" item is added
   * to the top of the menu so users have a discoverable single-action
   * way to open a file (the card itself only opens on double-click).
   */
  onOpen?: () => void
  /**
   * Positions the ⋯ trigger wrapper. Default suits grid tiles
   * (top-end corner); list rows pass a vertically-centered anchor so
   * the trigger lands in the row's trailing gutter.
   */
  triggerClassName?: string
  /**
   * 'card' (default) wraps `children` with a secondary-click context
   * menu and floats the ⋯ trigger over the tile. 'inline' renders the
   * trigger on its own, in flow — for toolbars like the document
   * detail header, which has no card surface to wrap.
   *
   * BUG-15: the detail header used to hand-roll its own four-item menu
   * (Share / Compare / Manage access / Create task) with no rename,
   * move, copy or delete. Reusing this component there is what keeps
   * the two surfaces from drifting apart again — the item list and
   * every dialog it opens live in exactly one place.
   */
  variant?: 'card' | 'inline'
  /** Items to append after the core actions, before the delete group. */
  extraActions?: ExtraAction[]
  /**
   * Core action keys to omit. Used by surfaces that already offer the
   * action as a dedicated control (the detail header has a labelled
   * Download button, so it hides the menu's duplicate).
   */
  hideActions?: string[]
}

type MenuItem =
  | {
      kind: 'action'
      key: string
      label: string
      icon: ReactNode
      onSelect: () => void
      destructive?: boolean
      /**
       * When set, the item is rendered as visually disabled and the
       * click is consumed locally (toast the hint). Used for actions
       * whose backend isn't built yet, so the menu doesn't pretend
       * to support them but the user understands WHY they're inert.
       */
      disabledHint?: string
    }
  | { kind: 'separator'; key: string }

// Downloading takes two hops: GET /documents/{id}/versions returns the
// latest version, then GET /storage/downloads/{doc}/{vid} mints a
// signed URL. We anchor-click the URL so the browser handles the
// actual fetch + Save dialog (cross-browser reliable, no blob memory).
async function downloadLatest(documentId: string) {
  const versions = await getVersions(documentId)
  const latest = [...versions].sort((a, b) => b.version_number - a.version_number)[0]
  if (!latest?.id) {
    toast.error('This document has no uploaded content yet')
    return
  }
  const { url } = await getDownloadURL(documentId, latest.id)
  const a = document.createElement('a')
  a.href = url
  a.rel = 'noopener'
  a.click()
}

export function DocumentActionsMenu({
  doc,
  children,
  onOpen,
  triggerClassName,
  variant = 'card',
  extraActions,
  hideActions,
}: Props) {
  const [renameOpen, setRenameOpen] = useState(false)
  const [moveOpen, setMoveOpen] = useState(false)
  const [copyOpen, setCopyOpen] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [protectOpen, setProtectOpen] = useState(false)
  const [taskOpen, setTaskOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [manageAccessOpen, setManageAccessOpen] = useState(false)
  const del = useDeleteDocument()

  const onDownload = async () => {
    try { await downloadLatest(doc.id) }
    catch (err) { toast.error(readErrorMessage(err) ?? 'Could not download document') }
  }

  // Backend enforces legal-hold restrictions via IsLegalHoldBlocked in
  // services/document/internal/model/lifecycle.go. Delete and move are
  // blocked; update_title (rename) is explicitly allowed.
  const isOnHold = doc.lifecycle_state === 'legal_hold'

  const coreItems: MenuItem[] = [
    ...(onOpen
      ? ([
          { kind: 'action', key: 'open', label: 'Open', icon: <Eye className="h-4 w-4" />, onSelect: onOpen },
          { kind: 'separator', key: 'sep-open' },
        ] as MenuItem[])
      : []),
    { kind: 'action', key: 'rename', label: 'Rename', icon: <Pencil className="h-4 w-4" />, onSelect: () => setRenameOpen(true) },
    {
      kind: 'action', key: 'move', label: 'Move to folder…', icon: <FolderInput className="h-4 w-4" />,
      onSelect: () => setMoveOpen(true),
      ...(isOnHold ? { disabledHint: 'Document is under legal hold — move is not permitted' } : {}),
    },
    {
      kind: 'action', key: 'copy', label: 'Copy to folder…',
      icon: <Copy className="h-4 w-4" />,
      onSelect: () => setCopyOpen(true),
      ...(isOnHold ? { disabledHint: 'Document is under legal hold — copy is not permitted' } : {}),
    },
    { kind: 'action', key: 'download', label: 'Download', icon: <Download className="h-4 w-4" />, onSelect: () => { void onDownload() } },
    { kind: 'action', key: 'share', label: 'Share…', icon: <Share2 className="h-4 w-4" />, onSelect: () => setShareOpen(true) },
    { kind: 'action', key: 'protect-share', label: 'Protect & share…', icon: <Lock className="h-4 w-4" />, onSelect: () => setProtectOpen(true) },
    { kind: 'action', key: 'add-to-task', label: 'Add to task…', icon: <ListTodo className="h-4 w-4" />, onSelect: () => setTaskOpen(true) },
    {
      kind: 'action', key: 'manage-access', label: 'Manage access…',
      icon: <ShieldCheck className="h-4 w-4" />,
      onSelect: () => setManageAccessOpen(true),
    },
    ...(extraActions ?? []).map((a): MenuItem => ({ kind: 'action', ...a })),
    { kind: 'separator', key: 'sep' },
    {
      kind: 'action', key: 'delete', label: 'Delete', icon: <Trash2 className="h-4 w-4" />,
      onSelect: () => setConfirmDelete(true),
      destructive: true,
      ...(isOnHold ? { disabledHint: 'Document is under legal hold — delete is not permitted' } : {}),
    },
  ]

  const hidden = new Set(hideActions ?? [])
  const items = hidden.size === 0
    ? coreItems
    : coreItems.filter((it) => it.kind === 'separator' || !hidden.has(it.key))

  const handleSelect = (it: Extract<MenuItem, { kind: 'action' }>) => {
    if (it.disabledHint) {
      toast.message(it.disabledHint)
      return
    }
    it.onSelect()
  }

  // Let the menus close on select (Radix default) so their focus
  // trap releases before a dialog mounts. The action handlers
  // open the dialogs via setState which fires synchronously, so the
  // dialog mounts on the next render with focus management taking
  // over cleanly.
  // Items with a disabledHint don't use Radix's `disabled` prop —
  // disabled items lose pointer-events, so the user can't click to
  // discover WHY. Instead we keep them clickable but route the click
  // through handleSelect which toasts the hint. They're visually
  // dimmed via className so the affordance still reads as "inert".
  const dimClass = (it: Extract<MenuItem, { kind: 'action' }>) =>
    it.disabledHint ? 'opacity-60' : ''

  const renderDropdownItems = () => items.map((it) => it.kind === 'separator'
    ? <DropdownMenuSeparator key={it.key} />
    : (
      <DropdownMenuItem
        key={it.key}
        onSelect={() => handleSelect(it)}
        className={[
          it.destructive ? 'text-destructive focus:text-destructive' : '',
          dimClass(it),
        ].filter(Boolean).join(' ')}
        aria-disabled={!!it.disabledHint}
        data-testid={`document-action-${it.key}`}
      >
        {it.icon}<span>{it.label}</span>
      </DropdownMenuItem>
    ))

  const renderContextItems = () => items.map((it) => it.kind === 'separator'
    ? <ContextMenuSeparator key={it.key} />
    : (
      <ContextMenuItem
        key={it.key}
        onSelect={() => handleSelect(it)}
        className={[
          it.destructive ? 'text-destructive focus:text-destructive' : '',
          dimClass(it),
        ].filter(Boolean).join(' ')}
        aria-disabled={!!it.disabledHint}
        data-testid={`document-context-action-${it.key}`}
      >
        {it.icon}<span>{it.label}</span>
      </ContextMenuItem>
    ))

  const dropdown = (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className={
            variant === 'inline'
              ? 'h-8 w-8'
              // 40% opacity always, 100% on hover/focus — gives the
              // affordance a discoverable visual anchor even before
              // hover, instead of the previous hover-only reveal.
              : 'h-7 w-7 opacity-40 transition-opacity hover:opacity-100 focus-visible:opacity-100 group-hover:opacity-100 data-[state=open]:opacity-100'
          }
          // The card body is itself a navigable link. Stop bubbling
          // so opening the menu doesn't also navigate. NO
          // preventDefault here: Radix composes the trigger's click
          // handler with checkForDefaultPrevented, so preventing
          // default killed keyboard (Enter/Space) opening — pointer
          // still worked via pointerdown, masking the bug.
          onClick={(e) => e.stopPropagation()}
          aria-label="Document actions"
          data-testid={`document-actions-trigger-${doc.id}`}
        >
          <MoreVertical className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56" data-testid="document-actions-menu">
        {renderDropdownItems()}
      </DropdownMenuContent>
    </DropdownMenu>
  )

  // Every dialog the items open. Shared by both variants — this is the
  // logic the detail header used to duplicate.
  const dialogs = (
    <>
      <RenameDocumentDialog
        open={renameOpen}
        onOpenChange={setRenameOpen}
        documentId={doc.id}
        initialTitle={doc.title}
      />
      <MoveDocumentDialog
        open={moveOpen}
        onOpenChange={setMoveOpen}
        documentId={doc.id}
        workspaceId={doc.workspace_id}
        currentFolderId={doc.folder_id}
        mode="move"
      />
      <MoveDocumentDialog
        open={copyOpen}
        onOpenChange={setCopyOpen}
        documentId={doc.id}
        workspaceId={doc.workspace_id}
        currentFolderId={doc.folder_id}
        mode="copy"
      />
      <ShareDialog
        open={shareOpen}
        onOpenChange={setShareOpen}
        documentId={doc.id}
        documentTitle={doc.title}
      />
      <ProtectShareDialog
        open={protectOpen}
        onOpenChange={setProtectOpen}
        documentId={doc.id}
        documentTitle={doc.title}
      />
      <TaskCreateDialog
        open={taskOpen}
        onOpenChange={setTaskOpen}
        defaultDocument={{ document_id: doc.id, title: doc.title }}
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete document?"
        description={`"${doc.title}" will be moved to the disposed state. This cannot be undone from the UI.`}
        confirmLabel="Delete"
        destructive
        loading={del.isPending}
        onConfirm={() => del.mutate(doc.id, { onSuccess: () => setConfirmDelete(false) })}
      />
      <ManageAccessDialog
        open={manageAccessOpen}
        onOpenChange={setManageAccessOpen}
        resourceType="document"
        resourceId={doc.id}
        resourceTitle={doc.title}
        workspaceId={doc.workspace_id}
        folderId={doc.folder_id}
      />
    </>
  )

  if (variant === 'inline') {
    return (
      <>
        {dropdown}
        {dialogs}
      </>
    )
  }

  return (
    <div className="group relative">
      <ContextMenu>
        <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
        <ContextMenuContent data-testid="document-context-menu">
          {renderContextItems()}
        </ContextMenuContent>
      </ContextMenu>

      <div className={triggerClassName ?? 'absolute end-2 top-2'}>{dropdown}</div>

      {dialogs}
    </div>
  )
}
