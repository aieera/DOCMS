import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ArchiveRestore, FileText, Trash2 } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/shadcn/badge'
import { TypedConfirmDialog } from '@/components/ui/shadcn/typed-confirm-dialog'
import { listTrash, purgeFromTrash, restoreFromTrash, type TrashEntry } from '@/api/trash'
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

  const restore = useMutation({
    mutationFn: (id: string) => restoreFromTrash(id),
    onSuccess: () => {
      toast.success('Document restored')
      qc.invalidateQueries({ queryKey: ['admin-trash'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't restore"),
  })
  const purge = useMutation({
    mutationFn: (id: string) => purgeFromTrash(id),
    onSuccess: () => {
      toast.success('Document permanently deleted')
      setPurgeTarget(null)
      qc.invalidateQueries({ queryKey: ['admin-trash'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Permanent delete failed'),
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
  return (
    <div>
      <PageHeader
        title="Trash"
        description="Soft-deleted documents across the tenant. Restore returns the document to its workspace; permanent delete removes the file from object storage and cannot be undone."
      />

      {trash.isLoading ? (
        <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<Trash2 className="h-12 w-12" />}
          title="Trash is empty"
          description="Documents you delete will appear here, ready to restore."
        />
      ) : (
        <Card className="overflow-hidden">
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
                  <td className="px-4 py-3">
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

export const Route = createFileRoute('/_authenticated/trash')({ component: TrashPage })
