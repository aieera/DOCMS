import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
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
  restoreFolderFromTrash,
  restoreFromTrash,
  listEmptyFolders,
  cleanupEmptyFolders,
  type TrashedFolder,
  type TrashEntry,
} from '@/api/trash'
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
  const [pageToken, setPageToken] = useState<string | undefined>()
  const [purgeTarget, setPurgeTarget] = useState<TrashEntry | null>(null)

  const trash = useQuery({
    queryKey: ['admin-trash', pageToken],
    queryFn: () => listTrash(pageToken),
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

  if (!canManage) {
    return (
      <div>
        <PageHeader title="Trash" description="Deleted items — restore or permanently delete" />
        <Card className="p-8 text-center text-sm text-muted-foreground">
          Trash is an administrator surface. Contact your workspace admin to restore a deleted document.
        </Card>
      </div>
    )
  }

  const items = trash.data?.items ?? []
  const folders = folderTrash.data ?? []
  const docsEmpty = !trash.isLoading && items.length === 0
  const foldersEmpty = !folderTrash.isLoading && folders.length === 0
  return (
    <div className="space-y-8">
      <PageHeader
        title="Trash"
        description="Soft-deleted folders and documents across the tenant. Restoring a folder brings its entire cascade back together; permanent delete removes files from object storage and cannot be undone."
      />

      {/* ---- Maintenance: empty-folder cleanup ------------------- */}
      <section data-testid="empty-folder-cleanup">
        <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold text-muted-foreground">
          <FolderClosed className="h-4 w-4" /> Maintenance
        </h2>
        <Card className="flex flex-wrap items-center justify-between gap-3 p-4">
          <div className="min-w-0">
            <p className="text-sm font-medium">Empty folders</p>
            <p className="text-xs text-muted-foreground">
              {emptyScan.isLoading
                ? 'Scanning…'
                : emptyScan.data
                  ? emptyScan.data.count === 0
                    ? 'No empty folders found.'
                    : `${emptyScan.data.count}${emptyScan.data.truncated ? '+' : ''} empty folder${emptyScan.data.count === 1 ? '' : 's'} (no documents, no subfolders). Cleanup is soft — they move to Trash here and can be restored.`
                  : 'Could not scan.'}
            </p>
          </div>
          <Button
            variant="outline"
            disabled={!emptyScan.data || emptyScan.data.count === 0 || cleanup.isPending}
            onClick={() => setConfirmCleanup(true)}
            data-testid="cleanup-empty-folders"
          >
            {cleanup.isPending ? 'Cleaning…' : 'Clean up empty folders'}
          </Button>
        </Card>
      </section>

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
                    />
                  ))}
                </tbody>
              </table>
              </div>
            </Card>
          )}
        </section>
      )}

      {/* ---- Documents ------------------------------------------- */}
      <section>
        <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold text-muted-foreground">
          <FileText className="h-4 w-4" /> Documents
        </h2>
      {trash.isLoading ? (
        <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
      ) : docsEmpty && foldersEmpty ? (
        <EmptyState
          icon={<Trash2 className="h-12 w-12" />}
          title="Trash is empty"
          description="Folders and documents you delete will appear here, ready to restore."
        />
      ) : docsEmpty ? (
        <Card className="p-6 text-center text-sm text-muted-foreground">
          No soft-deleted documents.
        </Card>
      ) : (
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
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2">
                      <FileText className="h-4 w-4 shrink-0 text-muted-foreground" />
                      <div className="min-w-0">
                        <p className="truncate font-medium" title={entry.title}>{entry.title}</p>
                        <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
                          {entry.lifecycle_state && (
                            <Badge variant="outline" className="font-normal">{entry.lifecycle_state}</Badge>
                          )}
                          {entry.mime_type && <code className="font-mono">{entry.mime_type}</code>}
                          <Link
                            to="/workspaces/$workspaceId"
                            params={{ workspaceId: entry.workspace_id }}
                            className="text-primary hover:underline"
                          >
                            View workspace
                          </Link>
                        </div>
                      </div>
                    </div>
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">{formatFileSize(entry.total_size_bytes)}</td>
                  <td className="px-4 py-3 text-muted-foreground">{entry.created_by_name || '—'}</td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {entry.deleted_at ? formatDateTime(entry.deleted_at) : '—'}
                  </td>
                  <td className="px-4 py-3 whitespace-nowrap">
                    <div className="flex justify-end gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => restore.mutate(entry.id)}
                        disabled={restore.isPending && restore.variables === entry.id}
                        data-testid={`trash-restore-${entry.id}`}
                      >
                        <ArchiveRestore className="me-1 h-3.5 w-3.5" />
                        Restore
                      </Button>
                      <Button
                        size="sm"
                        variant="destructive"
                        onClick={() => setPurgeTarget(entry)}
                        data-testid={`trash-purge-${entry.id}`}
                      >
                        <Trash2 className="me-1 h-3.5 w-3.5" />
                        Delete permanently
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>

          {trash.data?.next_page_token && (
            <div className="flex justify-center border-t border-border p-3">
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setPageToken(trash.data?.next_page_token)}
              >
                Load more
              </Button>
            </div>
          )}
        </Card>
      )}
      </section>

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
    </div>
  )
}

// TrashFolderRow renders one cohort-root soft-deleted folder. Carries
// its own "Confirm restore" affordance via the button; cascade
// restore is the only action — permanent purge for folders happens
// via the cascade FK chain when the workspace is deleted, so we
// deliberately don't expose a "Delete permanently" button here.
function TrashFolderRow({
  f,
  pending,
  onRestore,
}: {
  f: TrashedFolder
  pending: boolean
  onRestore: () => void
}) {
  const isPrivate = f.visibility === 'private'
  return (
    <tr data-testid={`trash-folder-row-${f.id}`}>
      <td className="px-4 py-3">
        <div className="flex items-center gap-2">
          <FolderClosed className="h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0">
            <div className="flex items-center gap-1.5">
              <p className="truncate font-medium" title={f.name}>{f.name}</p>
              {isPrivate && (
                <Badge variant="outline" className="gap-0.5 font-normal">
                  <Lock className="h-3 w-3" /> Private
                </Badge>
              )}
            </div>
          </div>
        </div>
      </td>
      <td className="px-4 py-3 text-muted-foreground">
        <Link
          to="/workspaces/$workspaceId"
          params={{ workspaceId: f.workspace_id }}
          className="text-primary hover:underline"
        >
          {f.workspace_name}
        </Link>
      </td>
      <td className="px-4 py-3 text-muted-foreground">
        {f.cohort_docs > 0
          ? `${f.cohort_docs} document${f.cohort_docs === 1 ? '' : 's'} in cohort`
          : 'empty'}
      </td>
      <td className="px-4 py-3 text-muted-foreground">
        {f.deleted_at ? formatDateTime(f.deleted_at) : '—'}
      </td>
      <td className="px-4 py-3">
        <div className="flex justify-end gap-2">
          {f.restorable ? (
            <Button
              size="sm"
              variant="outline"
              onClick={onRestore}
              disabled={pending}
              loading={pending}
              data-testid={`trash-folder-restore-${f.id}`}
            >
              <ArchiveRestore className="me-1 h-3.5 w-3.5" />
              Restore
            </Button>
          ) : (
            <span
              className="text-xs text-muted-foreground"
              title="Soft-deleted before cohort tracking was added — restore by hand from the workspace if needed."
            >
              No cohort
            </span>
          )}
        </div>
      </td>
    </tr>
  )
}

export const Route = createFileRoute('/_authenticated/trash')({ component: TrashPage })
