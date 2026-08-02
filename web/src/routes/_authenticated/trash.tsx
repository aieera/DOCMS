import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { ArchiveRestore, FileText, FolderClosed, Lock, Trash2 } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/shadcn/badge'
import { TypedConfirmDialog } from '@/components/ui/shadcn/typed-confirm-dialog'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import {
  listTrash,
  listTrashedFolders,
  purgeFromTrash,
  purgeFolderFromTrash,
  emptyTrash,
  restoreFolderFromTrash,
  restoreFromTrash,
  listEmptyFolders,
  cleanupEmptyFolders,
  type TrashedFolder,
  type TrashEntry,
} from '@/api/trash'
import { MyTrashSection } from '@/components/trash/MyTrashSection'
import { formatDateTime, formatFileSize } from '@/lib/formatters'
import { readErrorMessage } from '@/api/client'
import { useAuthStore } from '@/store/authStore'

// Trash page — tenant-wide list of soft-deleted documents. Admin/owner
// only (the GET endpoint role-gates server-side; we mirror it on the
// frontend so the navbar tile + page itself fail closed for member
// roles instead of showing a 403 toast). Two actions:
//   - Restore  → clears deleted_at (POST .../restore)
//   - Delete permanently → MinIO DeleteObject + DB hard-delete
//     (DELETE .../trash/{id}). Gated behind TypedConfirmDialog so a
//     misclick can't shred a tenant's worst-case-irreplaceable doc.
function TrashPage() {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const canManage = role === 'owner' || role === 'admin'
  const [purgeTarget, setPurgeTarget] = useState<TrashEntry | null>(null)
  const [folderPurgeTarget, setFolderPurgeTarget] = useState<TrashedFolder | null>(null)
  const [confirmEmptyTrash, setConfirmEmptyTrash] = useState(false)

  // Infinite query so "Load more" APPENDS pages — the previous
  // useQuery-keyed-by-token version replaced page 1 with page 2,
  // silently dropping earlier rows past 50 trashed docs.
  const trash = useInfiniteQuery({
    queryKey: ['admin-trash'],
    queryFn: ({ pageParam }) => listTrash(pageParam || undefined),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_page_token ?? undefined,
    enabled: canManage,
  })

  // Soft-deleted folders surface from FIX-5 (cascade delete). Each
  // row represents a cohort-root; restore brings every descendant
  // folder + every document in the cohort back together.
  const folderTrash = useQuery({
    queryKey: ['admin-trash-folders'],
    queryFn: listTrashedFolders,
    enabled: canManage,
  })

  const restoreFolder = useAppMutation({
    mutationFn: (id: string) => restoreFolderFromTrash(id),
    onSuccess: () => {
      toast.success('Folder restored')
      qc.invalidateQueries({ queryKey: ['admin-trash-folders'] })
      // Document trash also changes — the cohort's documents come
      // back too, so they leave the docs trash table.
      qc.invalidateQueries({ queryKey: ['admin-trash'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't restore folder"),
  })

  const restore = useAppMutation({
    mutationFn: (id: string) => restoreFromTrash(id),
    onSuccess: () => {
      toast.success('Document restored')
      qc.invalidateQueries({ queryKey: ['admin-trash'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't restore"),
  })
  const purge = useAppMutation({
    mutationFn: (id: string) => purgeFromTrash(id),
    onSuccess: () => {
      toast.success('Document permanently deleted')
      setPurgeTarget(null)
      qc.invalidateQueries({ queryKey: ['admin-trash'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Permanent delete failed'),
  })

  const purgeFolder = useAppMutation({
    mutationFn: (id: string) => purgeFolderFromTrash(id),
    onSuccess: (res) => {
      setFolderPurgeTarget(null)
      toast.success(
        `Permanently deleted ${res.folders_deleted} folder${res.folders_deleted === 1 ? '' : 's'}` +
          (res.documents_deleted > 0
            ? ` and ${res.documents_deleted} document${res.documents_deleted === 1 ? '' : 's'}`
            : ''),
      )
      qc.invalidateQueries({ queryKey: ['admin-trash-folders'] })
      qc.invalidateQueries({ queryKey: ['admin-trash'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Permanent delete failed'),
  })

  const emptyAll = useAppMutation({
    mutationFn: () => emptyTrash(),
    onSuccess: (res) => {
      setConfirmEmptyTrash(false)
      const purged = `Purged ${res.purged_folders} folder${res.purged_folders === 1 ? '' : 's'} and ${res.purged_documents} document${res.purged_documents === 1 ? '' : 's'}`
      if (res.skipped.length > 0) {
        toast.warning(
          `${purged}. ${res.skipped.length} item${res.skipped.length === 1 ? '' : 's'} skipped (legal hold or active retention).`,
        )
      } else {
        toast.success(purged)
      }
      qc.invalidateQueries({ queryKey: ['admin-trash-folders'] })
      qc.invalidateQueries({ queryKey: ['admin-trash'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't empty trash"),
  })

  // Empty-folder cleanup (admin maintenance). The dry-run scan runs on
  // load so the operator sees the count; cleanup soft-deletes them into
  // the folder trash above (restorable + audited).
  const [confirmCleanup, setConfirmCleanup] = useState(false)
  const emptyScan = useQuery({
    queryKey: ['empty-folders-scan'],
    queryFn: () => listEmptyFolders(200),
    enabled: canManage,
  })
  const cleanup = useAppMutation({
    mutationFn: () => cleanupEmptyFolders(2000),
    onSuccess: (res) => {
      setConfirmCleanup(false)
      toast.success(
        `Removed ${res.deleted} empty folder${res.deleted === 1 ? '' : 's'}` +
          (res.more_remaining ? ' (more remain — run again)' : ''),
      )
      qc.invalidateQueries({ queryKey: ['empty-folders-scan'] })
      qc.invalidateQueries({ queryKey: ['admin-trash-folders'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't clean up empty folders"),
  })

  // Members get their own Trash — the items THEY deleted, which they can
  // restore themselves. Previously this page told them to go ask an admin.
  if (!canManage) {
    return (
      <div className="space-y-6">
        <PageHeader title="Trash" description="Items you deleted — restore them or clear them from this list" />
        <MyTrashSection />
      </div>
    )
  }

  const items = trash.data?.pages.flatMap((p) => p.items) ?? []
  const folders = folderTrash.data ?? []
  const docsEmpty = !trash.isLoading && items.length === 0
  const foldersEmpty = !folderTrash.isLoading && folders.length === 0
  const trashEmpty = docsEmpty && foldersEmpty
  const emptyCount = emptyScan.data?.count ?? 0
  const emptyTruncated = emptyScan.data?.truncated ?? false
  return (
    <div className="space-y-6">
      <PageHeader
        title="Trash"
        description="Restore deleted items, or delete them permanently. Permanent deletion cannot be undone."
        actions={
          <Button
            size="sm"
            variant="destructive"
            disabled={trashEmpty || emptyAll.isPending}
            onClick={() => setConfirmEmptyTrash(true)}
            data-testid="empty-trash"
          >
            <Trash2 className="me-1.5 h-3.5 w-3.5" />
            {emptyAll.isPending ? 'Emptying…' : 'Empty trash'}
          </Button>
        }
      />

      {/* Admins are users too: their own deletions come first, then the
          tenant-wide surface below. */}
      <MyTrashSection />

      {/* ---- Maintenance: empty-folder cleanup. Compact single line;
             hidden entirely when there's nothing to clean. ---------- */}
      {emptyCount > 0 && (
        <div
          data-testid="empty-folder-cleanup"
          className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground"
        >
          <span>
            {emptyCount}
            {emptyTruncated ? '+' : ''} empty folder
            {emptyCount === 1 ? '' : 's'} in your workspaces (no documents or subfolders).
          </span>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 text-xs"
            disabled={cleanup.isPending}
            onClick={() => setConfirmCleanup(true)}
            data-testid="cleanup-empty-folders"
          >
            {cleanup.isPending ? 'Cleaning…' : 'Clean up'}
          </Button>
        </div>
      )}

      {/* ---- Folders ---------------------------------------------- */}
      {!foldersEmpty && (
        <section data-testid="trash-folders-section">
          <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold text-muted-foreground">
            <FolderClosed className="h-4 w-4" /> Folders
          </h2>
          {folderTrash.isLoading ? (
            <div className="flex justify-center py-6"><Spinner className="h-5 w-5" /></div>
          ) : (
            <Card className="overflow-hidden">
              {/* Same overflow-x-auto treatment as the documents table
                  below — without it the Card clips the Actions column
                  at narrow viewports with no scroll affordance. */}
              <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="border-b border-border bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                  <tr>
                    <th scope="col" className="px-4 py-2 text-start font-medium">Folder</th>
                    <th scope="col" className="px-4 py-2 text-start font-medium">Workspace</th>
                    <th scope="col" className="px-4 py-2 text-start font-medium">Contents</th>
                    <th scope="col" className="px-4 py-2 text-start font-medium">Deleted</th>
                    <th scope="col" className="px-4 py-2 text-end font-medium">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {folders.map((f) => (
                    <TrashFolderRow
                      key={f.id}
                      f={f}
                      pending={restoreFolder.isPending && restoreFolder.variables === f.id}
                      onRestore={() => restoreFolder.mutate(f.id)}
                      onPurge={() => setFolderPurgeTarget(f)}
                    />
                  ))}
                </tbody>
              </table>
              </div>
            </Card>
          )}
        </section>
      )}

      {/* ---- Documents. Section hides entirely when empty (same as
             Folders) — one shared empty-state when the whole trash is
             empty keeps the page from stacking placeholder cards. --- */}
      {trash.isLoading ? (
        <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
      ) : trashEmpty ? (
        <EmptyState
          icon={<Trash2 className="h-12 w-12" />}
          title="Trash is empty"
          description="Folders and documents you delete will appear here, ready to restore."
        />
      ) : docsEmpty ? null : (
      <section>
        <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold text-muted-foreground">
          <FileText className="h-4 w-4" /> Documents
        </h2>
        <Card className="overflow-hidden">
          {/* overflow-x-auto lets the table scroll instead of the Card
              clipping the Actions column ("Delete perman…") when the
              title column eats the available width at narrow viewports. */}
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-border bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-4 py-2 text-start font-medium">Title</th>
                <th scope="col" className="px-4 py-2 text-start font-medium">Size</th>
                <th scope="col" className="px-4 py-2 text-start font-medium">Deleted by</th>
                <th scope="col" className="px-4 py-2 text-start font-medium">Deleted</th>
                <th scope="col" className="px-4 py-2 text-end font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {items.map((entry) => (
                <tr key={entry.id} data-testid={`trash-row-${entry.id}`}>
                  <td className="px-4 py-2">
                    <div className="flex min-w-0 items-center gap-2">
                      <FileText className="h-4 w-4 shrink-0 text-muted-foreground" />
                      <Link
                        to="/workspaces/$workspaceId"
                        params={{ workspaceId: entry.workspace_id }}
                        className="truncate font-medium hover:underline"
                        title={entry.title}
                      >
                        {entry.title}
                      </Link>
                      {entry.lifecycle_state === 'legal_hold' && (
                        <Badge variant="outline" className="shrink-0 font-normal">
                          <Lock className="me-0.5 h-3 w-3" /> hold
                        </Badge>
                      )}
                      {/* The deleter has cleared this from their own trash
                          and believes it gone. It is still recoverable —
                          only the purge below destroys anything. */}
                      {entry.user_cleared && (
                        <Badge
                          variant="secondary"
                          className="shrink-0 font-normal"
                          title="The person who deleted this has cleared it from their own trash. It is still recoverable from here."
                        >
                          cleared by user
                        </Badge>
                      )}
                    </div>
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">{formatFileSize(entry.total_size_bytes)}</td>
                  <td className="px-4 py-2 text-muted-foreground">
                    {entry.deleted_by_name || entry.created_by_name || '—'}
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">
                    {entry.deleted_at ? formatDateTime(entry.deleted_at) : '—'}
                  </td>
                  <td className="px-4 py-2 whitespace-nowrap">
                    <div className="flex justify-end gap-1">
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-8 w-8"
                        title="Restore"
                        aria-label={`Restore ${entry.title}`}
                        onClick={() => restore.mutate(entry.id)}
                        disabled={restore.isPending && restore.variables === entry.id}
                        data-testid={`trash-restore-${entry.id}`}
                      >
                        <ArchiveRestore className="h-4 w-4" />
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-8 w-8 text-destructive hover:bg-destructive/10 hover:text-destructive"
                        title="Delete permanently"
                        aria-label={`Permanently delete ${entry.title}`}
                        onClick={() => setPurgeTarget(entry)}
                        data-testid={`trash-purge-${entry.id}`}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>

          {trash.hasNextPage && (
            <div className="flex justify-center border-t border-border p-3">
              <Button
                size="sm"
                variant="ghost"
                onClick={() => trash.fetchNextPage()}
                disabled={trash.isFetchingNextPage}
              >
                {trash.isFetchingNextPage ? 'Loading…' : 'Load more'}
              </Button>
            </div>
          )}
        </Card>
      </section>
      )}

      <ConfirmDialog
        open={confirmCleanup}
        onOpenChange={setConfirmCleanup}
        title="Clean up empty folders?"
        description={`This soft-deletes ${emptyScan.data?.count ?? 0}${emptyScan.data?.truncated ? '+' : ''} folders that contain no documents and no subfolders. They move to the folder Trash above and can be restored. The action is audited.`}
        confirmLabel="Clean up"
        loading={cleanup.isPending}
        onConfirm={() => cleanup.mutate()}
      />

      {purgeTarget && (
        <TypedConfirmDialog
          open={true}
          onOpenChange={(open) => { if (!open) setPurgeTarget(null) }}
          title="Permanently delete document?"
          description={`This will delete "${purgeTarget.title}" from object storage and remove every version, blob, OCR result, and chunk associated with it. This cannot be undone. Type the document title to confirm.`}
          expectedValue={purgeTarget.title}
          inputLabel="Document title"
          confirmLabel="Delete permanently"
          destructive
          loading={purge.isPending}
          onConfirm={() => purge.mutate(purgeTarget.id)}
          confirmTestId="trash-purge-confirm"
        />
      )}

      {folderPurgeTarget && (
        <TypedConfirmDialog
          open={true}
          onOpenChange={(open) => { if (!open) setFolderPurgeTarget(null) }}
          title="Permanently delete folder?"
          description={
            `This will permanently delete "${folderPurgeTarget.name}", every subfolder deleted with it` +
            (folderPurgeTarget.cohort_docs > 0
              ? `, and the ${folderPurgeTarget.cohort_docs} document${folderPurgeTarget.cohort_docs === 1 ? '' : 's'} in it`
              : '') +
            ' — including their files in object storage. This cannot be undone. Type the folder name to confirm.'
          }
          expectedValue={folderPurgeTarget.name}
          inputLabel="Folder name"
          confirmLabel="Delete permanently"
          destructive
          loading={purgeFolder.isPending}
          onConfirm={() => purgeFolder.mutate(folderPurgeTarget.id)}
          confirmTestId="trash-folder-purge-confirm"
        />
      )}

      <TypedConfirmDialog
        open={confirmEmptyTrash}
        onOpenChange={setConfirmEmptyTrash}
        title="Empty trash?"
        description={`This permanently deletes everything in the trash — ${folders.length} folder${folders.length === 1 ? '' : 's'} and ${items.length}${trash.hasNextPage ? '+' : ''} document${items.length === 1 ? '' : 's'} — including their files in object storage. Items under legal hold or active retention are skipped. This cannot be undone. Type EMPTY to confirm.`}
        expectedValue="EMPTY"
        inputLabel="Confirmation"
        confirmLabel="Empty trash"
        destructive
        loading={emptyAll.isPending}
        onConfirm={() => emptyAll.mutate()}
        confirmTestId="empty-trash-confirm"
      />
    </div>
  )
}

// TrashFolderRow renders one cohort-root soft-deleted folder with two
// actions: cohort restore (hidden for legacy no-cohort rows, which
// have no restore scope) and permanent delete (available for every
// row — the cohort purge endpoint handles legacy rows by subtree,
// which is the only way to remove them).
function TrashFolderRow({
  f,
  pending,
  onRestore,
  onPurge,
}: {
  f: TrashedFolder
  pending: boolean
  onRestore: () => void
  onPurge: () => void
}) {
  const isPrivate = f.visibility === 'private'
  return (
    <tr data-testid={`trash-folder-row-${f.id}`}>
      <td className="px-4 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <FolderClosed className="h-4 w-4 shrink-0 text-muted-foreground" />
          <p className="truncate font-medium" title={f.name}>{f.name}</p>
          {isPrivate && <Lock className="h-3 w-3 shrink-0 text-muted-foreground" aria-label="Private" />}
        </div>
      </td>
      <td className="px-4 py-2 text-muted-foreground">
        <Link
          to="/workspaces/$workspaceId"
          params={{ workspaceId: f.workspace_id }}
          className="hover:underline"
        >
          {f.workspace_name}
        </Link>
      </td>
      <td className="px-4 py-2 text-muted-foreground">
        {f.cohort_docs > 0 ? `${f.cohort_docs} doc${f.cohort_docs === 1 ? '' : 's'}` : 'empty'}
      </td>
      <td className="px-4 py-2 text-muted-foreground">
        {f.deleted_at ? formatDateTime(f.deleted_at) : '—'}
      </td>
      <td className="px-4 py-2 whitespace-nowrap">
        <div className="flex justify-end gap-1">
          {f.restorable && (
            <Button
              size="icon"
              variant="ghost"
              className="h-8 w-8"
              title="Restore folder and its contents"
              aria-label={`Restore ${f.name}`}
              onClick={onRestore}
              disabled={pending}
              loading={pending}
              data-testid={`trash-folder-restore-${f.id}`}
            >
              {!pending && <ArchiveRestore className="h-4 w-4" />}
            </Button>
          )}
          <Button
            size="icon"
            variant="ghost"
            className="h-8 w-8 text-destructive hover:bg-destructive/10 hover:text-destructive"
            title="Delete permanently"
            aria-label={`Permanently delete ${f.name}`}
            onClick={onPurge}
            data-testid={`trash-folder-purge-${f.id}`}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  )
}

export const Route = createFileRoute('/_authenticated/trash')({ component: TrashPage })
