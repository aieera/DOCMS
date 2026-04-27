// Trash — deleted documents the admin can restore (GAP-4).
//
// Backend: GET /api/v1/admin/documents/trash + POST /restore. Held
// docs cannot be restored (the backend returns 409 with a message
// pointing to the legal-hold release page).

import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Trash2, RotateCcw, FileText } from 'lucide-react'

import { listTrash, restoreDocument, type TrashItem } from '@/api/documents'
import { useAuthStore } from '@/store/authStore'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { EmptyState } from '@/components/ui/EmptyState'
import { Skeleton } from '@/components/ui/Skeleton'
import { PageHeader } from '@/components/shared/PageHeader'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'
import { useState } from 'react'

function TrashPage() {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const isAdmin = role === 'admin' || role === 'owner'

  const { data, isLoading } = useQuery({
    queryKey: ['trash'],
    queryFn: listTrash,
    enabled: isAdmin,
  })

  const [restoreTarget, setRestoreTarget] = useState<TrashItem | null>(null)
  const restore = useMutation({
    mutationFn: (id: string) => restoreDocument(id),
    onSuccess: () => {
      toast.success('Document restored')
      qc.invalidateQueries({ queryKey: ['trash'] })
      setRestoreTarget(null)
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Restore failed')
    },
  })

  if (!isAdmin) {
    return (
      <div>
        <PageHeader title="Trash" description="Deleted items — admin only" />
        <EmptyState
          icon={<Trash2 className="h-12 w-12" />}
          title="Admin access required"
          description="Only admins or owners can view and restore deleted documents."
        />
      </div>
    )
  }

  return (
    <div>
      <PageHeader
        title="Trash"
        description="Soft-deleted documents from the past 90 days. Restore returns them to their original workspace."
      />

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-16" />
          ))}
        </div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Trash2 className="h-12 w-12" />}
          title="Trash is empty"
          description="Documents your team deletes will appear here for 90 days before they're disposed permanently by retention policy."
        />
      ) : (
        <ul className="space-y-2">
          {data.map((item) => (
            <li
              key={item.id}
              data-testid={`trash-item-${item.id}`}
              className="flex items-start justify-between gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3"
            >
              <div className="flex min-w-0 items-start gap-2">
                <FileText className="mt-0.5 h-4 w-4 shrink-0 text-[var(--color-text-secondary)]" />
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-medium">{item.title || '(untitled)'}</span>
                    <Badge variant="archived">{formatFileSize(item.size_bytes)}</Badge>
                  </div>
                  <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
                    {item.workspace_name && <>{item.workspace_name} · </>}
                    deleted {formatRelativeTime(item.deleted_at)}
                  </p>
                </div>
              </div>
              <Button
                variant="outline"
                onClick={() => setRestoreTarget(item)}
                disabled={restore.isPending}
                data-testid={`restore-${item.id}`}
              >
                <RotateCcw className="h-4 w-4" /> Restore
              </Button>
            </li>
          ))}
        </ul>
      )}

      <ConfirmDialog
        open={restoreTarget !== null}
        onOpenChange={(v) => !v && setRestoreTarget(null)}
        title="Restore document?"
        description={
          restoreTarget
            ? `${restoreTarget.title || '(untitled)'} will return to its original workspace. If it's under a legal hold, restore is refused — release the hold first.`
            : ''
        }
        confirmLabel="Restore"
        loading={restore.isPending}
        onConfirm={() => restoreTarget && restore.mutate(restoreTarget.id)}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/trash')({ component: TrashPage })
