import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ArchiveRestore, FileText, Lock, Trash2 } from 'lucide-react'

import { useAppMutation } from '@/hooks/useAppMutation'
import { useMyTrash, myTrashItems, useTrashLocations } from '@/hooks/useTrash'
import { invalidateDocuments, invalidateTrash } from '@/hooks/queryInvalidation'
import { readErrorMessage } from '@/api/client'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/shadcn/badge'
import { Spinner } from '@/components/ui/Spinner'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { clearFromMyTrash, restoreMyTrash, type TrashEntry } from '@/api/trash'
import { TrashLocation } from './TrashLocation'
import { formatDateTime, formatFileSize } from '@/lib/formatters'

// My Trash — what THIS user deleted, for every role.
//
// Before this existed, deleting a document put it somewhere only an
// administrator could see, so a member who deleted their own file had no
// way to get it back and no way to tell whether it still existed.
//
// The second action here is deliberately "Remove from my trash", NOT
// "Delete permanently": it clears the row from this list and nothing else.
// The bytes stay, the admin Trash keeps listing it, and an admin can still
// restore it. Members have no route that destroys content — so the copy
// must not promise that it does. Saying "permanently" here would be a lie
// that stops people asking an admin to recover something recoverable.
/** When workspaceId is set, only deletions from that workspace are shown
 *  (the workspace browser's Trash link scopes the page this way). */
export function MyTrashSection({ workspaceId }: { workspaceId?: string } = {}) {
  const qc = useQueryClient()
  const [clearTarget, setClearTarget] = useState<TrashEntry | null>(null)

  const trash = useMyTrash()

  // BUG-10: the middle key here used to be ['trash'] — a root NO query
  // in the app ever used, so restoring from this list left the admin
  // table on the SAME page showing the row it had just moved.
  const invalidate = () => {
    void invalidateTrash(qc)
    void invalidateDocuments(qc)
  }

  const restore = useAppMutation({
    mutationFn: (id: string) => restoreMyTrash(id),
    onSuccess: () => {
      invalidate()
      toast.success('Restored')
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't restore that document"),
  })

  const clear = useAppMutation({
    mutationFn: (id: string) => clearFromMyTrash(id),
    onSuccess: () => {
      invalidate()
      setClearTarget(null)
      toast.success('Removed from your trash', {
        description: 'An administrator can still recover it if you need it back.',
      })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't remove that item"),
  })

  const allItems = myTrashItems(trash.data?.pages)
  const items = workspaceId ? allItems.filter((i) => i.workspace_id === workspaceId) : allItems
  const locationOf = useTrashLocations(items)

  return (
    <section data-testid="my-trash-section">
      <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold text-muted-foreground">
        <Trash2 className="h-4 w-4" /> My trash
        {items.length > 0 && <span className="font-normal">({items.length})</span>}
      </h2>

      {trash.isLoading ? (
        <Card className="flex items-center justify-center p-8">
          <Spinner />
        </Card>
      ) : items.length === 0 ? (
        <Card className="p-6 text-center text-sm text-muted-foreground">
          You haven&rsquo;t deleted anything. Items you delete appear here so you can put them back.
        </Card>
      ) : (
        <Card className="overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="border-b border-border bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th scope="col" className="px-4 py-2 text-start font-medium">Title</th>
                  {/* Where Restore puts it back. Without this the button
                      was a blind action. */}
                  <th scope="col" className="px-4 py-2 text-start font-medium">Original location</th>
                  <th scope="col" className="px-4 py-2 text-start font-medium">Size</th>
                  <th scope="col" className="px-4 py-2 text-start font-medium">Deleted</th>
                  <th scope="col" className="px-4 py-2 text-end font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {items.map((entry) => (
                  <tr key={entry.id} data-testid={`my-trash-row-${entry.id}`}>
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
                      </div>
                    </td>
                    <td className="max-w-[18rem] px-4 py-2 text-muted-foreground">
                      <TrashLocation location={locationOf(entry)} />
                    </td>
                    <td className="px-4 py-2 text-muted-foreground">
                      {formatFileSize(entry.total_size_bytes)}
                    </td>
                    <td className="px-4 py-2 text-muted-foreground">
                      {entry.deleted_at ? formatDateTime(entry.deleted_at) : '—'}
                    </td>
                    <td className="whitespace-nowrap px-4 py-2">
                      <div className="flex justify-end gap-1">
                        <Button
                          size="icon"
                          variant="ghost"
                          className="h-9 w-9"
                          title="Restore"
                          aria-label={`Restore ${entry.title}`}
                          disabled={restore.isPending}
                          onClick={() => restore.mutate(entry.id)}
                        >
                          <ArchiveRestore className="h-4 w-4" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          className="h-9 w-9 text-destructive hover:text-destructive"
                          title="Remove from my trash"
                          aria-label={`Remove ${entry.title} from my trash`}
                          disabled={clear.isPending}
                          onClick={() => setClearTarget(entry)}
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
        </Card>
      )}

      {trash.hasNextPage && (
        <div className="mt-3 flex justify-center">
          <Button
            variant="outline"
            size="sm"
            disabled={trash.isFetchingNextPage}
            onClick={() => trash.fetchNextPage()}
          >
            {trash.isFetchingNextPage ? 'Loading…' : 'Load more'}
          </Button>
        </div>
      )}

      {/* A plain confirm, not the typed "DELETE" dialog the admin purge
          uses — nothing is destroyed here, so demanding that ceremony
          would misrepresent the risk. */}
      <ConfirmDialog
        open={clearTarget !== null}
        onOpenChange={(open) => !open && setClearTarget(null)}
        title="Remove from your trash?"
        description={
          clearTarget
            ? `"${clearTarget.title}" will no longer appear in your trash. It is not erased — an administrator can still recover it.`
            : ''
        }
        confirmLabel="Remove"
        destructive
        onConfirm={() => clearTarget && clear.mutate(clearTarget.id)}
      />
    </section>
  )
}
