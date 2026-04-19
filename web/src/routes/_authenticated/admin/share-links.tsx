import { useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Link2, Trash2, Lock, Eye, ShieldOff } from 'lucide-react'

import {
  listAdminShareLinks,
  revokeAdminShareLink,
  revokeAllShareLinksForDocument,
  type AdminShareLink,
} from '@/api/shareLinksAdmin'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatRelativeTime } from '@/lib/formatters'

type Filter = 'active' | 'all'

interface Grouped {
  documentId: string
  title: string
  links: AdminShareLink[]
}

function ShareLinksPage() {
  const qc = useQueryClient()
  const [filter, setFilter] = useState<Filter>('active')

  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'share-links', filter],
    queryFn: () => listAdminShareLinks({ status: filter }),
  })

  const grouped = useMemo<Grouped[]>(() => {
    if (!data) return []
    const byDoc = new Map<string, Grouped>()
    for (const l of data) {
      const g = byDoc.get(l.document_id) ?? {
        documentId: l.document_id,
        title: l.document_title || l.document_id,
        links: [],
      }
      g.links.push(l)
      byDoc.set(l.document_id, g)
    }
    return Array.from(byDoc.values())
  }, [data])

  const revoke = useMutation({
    mutationFn: (id: string) => revokeAdminShareLink(id),
    onSuccess: () => {
      toast.success('Link revoked')
      qc.invalidateQueries({ queryKey: ['admin', 'share-links'] })
    },
    onError: () => toast.error('Revoke failed'),
  })

  const revokeAll = useMutation({
    mutationFn: (docId: string) => revokeAllShareLinksForDocument(docId),
    onSuccess: (r) => {
      toast.success(`Revoked ${r.revoked} link${r.revoked === 1 ? '' : 's'}`)
      qc.invalidateQueries({ queryKey: ['admin', 'share-links'] })
    },
    onError: () => toast.error('Revoke-all failed'),
  })

  return (
    <div>
      <PageHeader
        title="Share Links"
        description="Active share links across every document in this tenant."
      />

      <div className="mb-4 flex gap-2">
        {(['active', 'all'] as const).map((f) => (
          <button
            key={f}
            onClick={() => setFilter(f)}
            className={`rounded-md px-3 py-1 text-sm ${
              filter === f
                ? 'bg-[var(--color-primary)] text-white'
                : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-secondary)]'
            }`}
          >
            {f === 'active' ? 'Active only' : 'All (including expired/revoked)'}
          </button>
        ))}
      </div>

      {isLoading ? (
        <Skeleton className="h-32" />
      ) : grouped.length === 0 ? (
        <EmptyState
          icon={<Link2 className="h-12 w-12" />}
          title={filter === 'active' ? 'No active share links' : 'No share links'}
          description="Share links created from document detail pages will appear here."
        />
      ) : (
        <ul className="space-y-3">
          {grouped.map((g) => (
            <li
              key={g.documentId}
              className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)]"
            >
              <div className="flex items-start justify-between border-b border-[var(--color-border)] p-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <Link2 className="h-4 w-4 shrink-0" />
                    <span className="truncate font-medium">{g.title}</span>
                    <Badge variant="default">
                      {g.links.length} link{g.links.length === 1 ? '' : 's'}
                    </Badge>
                  </div>
                  <div className="mt-1 text-xs font-mono text-[var(--color-text-secondary)]">
                    {g.documentId}
                  </div>
                </div>
                <Button
                  onClick={() => {
                    if (
                      window.confirm(
                        `Revoke all ${g.links.filter((l) => l.is_active).length} active links on "${g.title}"?`,
                      )
                    )
                      revokeAll.mutate(g.documentId)
                  }}
                  disabled={revokeAll.isPending}
                >
                  <ShieldOff className="h-4 w-4" /> Revoke all
                </Button>
              </div>

              <table className="w-full text-xs">
                <thead className="bg-slate-50 dark:bg-slate-800/50">
                  <tr className="text-left">
                    <th className="px-3 py-1.5">Status</th>
                    <th className="px-3 py-1.5">Perms</th>
                    <th className="px-3 py-1.5">Views</th>
                    <th className="px-3 py-1.5">Expires</th>
                    <th className="px-3 py-1.5">Last accessed</th>
                    <th className="px-3 py-1.5">Created</th>
                    <th className="px-3 py-1.5"></th>
                  </tr>
                </thead>
                <tbody>
                  {g.links.map((l) => (
                    <tr key={l.id} className="border-t border-[var(--color-border)]">
                      <td className="px-3 py-1.5">
                        <div className="flex items-center gap-1">
                          <Badge variant={l.is_active ? 'active' : 'archived'}>
                            {l.is_active ? 'Active' : 'Revoked'}
                          </Badge>
                          {l.password_protected && (
                            <Lock className="h-3 w-3 text-[var(--color-text-secondary)]" />
                          )}
                        </div>
                      </td>
                      <td className="px-3 py-1.5">
                        <span className="font-mono">{l.permissions.join('+') || '—'}</span>
                      </td>
                      <td className="px-3 py-1.5">
                        <span className="inline-flex items-center gap-1">
                          <Eye className="h-3 w-3" />
                          {l.view_count}
                          {l.max_views > 0 && ` / ${l.max_views}`}
                        </span>
                      </td>
                      <td className="px-3 py-1.5">
                        {l.expires_at ? formatRelativeTime(l.expires_at) : 'never'}
                      </td>
                      <td className="px-3 py-1.5">
                        {l.accessed_at ? formatRelativeTime(l.accessed_at) : 'never'}
                      </td>
                      <td className="px-3 py-1.5">{formatRelativeTime(l.created_at)}</td>
                      <td className="px-3 py-1.5 text-right">
                        {l.is_active && (
                          <Button
                            onClick={() => revoke.mutate(l.id)}
                            disabled={revoke.isPending}
                          >
                            <Trash2 className="h-3 w-3" />
                          </Button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/share-links')({ component: ShareLinksPage })
