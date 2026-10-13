import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Eye, Link2, Lock, ShieldOff, Trash2 } from 'lucide-react'

import {
  listAdminShareLinks,
  revokeAdminShareLink,
  revokeAllShareLinksForDocument,
  type AdminShareLink,
} from '@/api/shareLinksAdmin'
import { listUserDirectory } from '@/api/auth'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { formatDateTime, formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

// Relative time with the exact timestamp on hover — auditors want both.
// warnPast: amber when the moment has passed (used for Expires, where a
// past date means the link is already dead).
function TimeCell({ iso, fallback, warnPast = false }: { iso?: string | null; fallback?: string; warnPast?: boolean }) {
  const { t } = useTranslation('common')
  if (!iso) return <span className="text-muted-foreground">{fallback}</span>
  const past = warnPast && new Date(iso) < new Date()
  return (
    <span
      title={formatDateTime(iso)}
      className={cn('text-muted-foreground', past && 'font-medium text-warning-strong')}
    >
      {past ? t('share_links.expired', { when: formatRelativeTime(iso) }) : formatRelativeTime(iso)}
    </span>
  )
}

type Filter = 'active' | 'all'

interface Grouped {
  documentId: string
  title: string
  links: AdminShareLink[]
}

function ShareLinksPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const [filter, setFilter] = useState<Filter>('active')
  const [pendingRevokeAll, setPendingRevokeAll] = useState<Grouped | null>(null)
  const [pendingRevoke, setPendingRevoke] = useState<AdminShareLink | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'share-links', filter],
    queryFn: () => listAdminShareLinks({ status: filter }),
  })

  // Resolve created_by UUIDs → names for the "By" column.
  const { data: directory } = useQuery({
    queryKey: ['user-directory'],
    queryFn: () => listUserDirectory(),
    staleTime: 5 * 60_000,
  })
  const nameOf = useMemo(() => {
    const m = new Map<string, string>()
    for (const u of directory ?? []) m.set(u.id, u.display_name || u.email)
    return (id?: string) => (id ? m.get(id) ?? `${id.slice(0, 8)}…` : '—')
  }, [directory])

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

  const revoke = useAppMutation({
    mutationFn: (id: string) => revokeAdminShareLink(id),
    onSuccess: () => {
      toast.success(t('share_links.revoked_toast'))
      qc.invalidateQueries({ queryKey: ['admin', 'share-links'] })
      setPendingRevoke(null)
    },
    onError: () => toast.error(t('share_links.revoke_failed')),
  })

  const revokeAll = useAppMutation({
    mutationFn: (docId: string) => revokeAllShareLinksForDocument(docId),
    onSuccess: (r) => {
      toast.success(`Revoked ${r.revoked} link${r.revoked === 1 ? '' : 's'}`)
      qc.invalidateQueries({ queryKey: ['admin', 'share-links'] })
      setPendingRevokeAll(null)
    },
    onError: () => toast.error(t('share_links.revoke_all_failed')),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('share_links.title')}
        description={t('share_links.description')}
      />

      <SegmentedFilter value={filter} onChange={setFilter} />

      {isLoading ? (
        <Skeleton className="h-32" />
      ) : grouped.length === 0 ? (
        <EmptyState
          icon={<Link2 className="h-6 w-6" />}
          title={filter === 'active' ? t('share_links.empty_active') : t('share_links.empty_all')}
          description={t('share_links.empty_desc')}
        />
      ) : (
        <ul className="space-y-3">
          {grouped.map((g) => (
            <li key={g.documentId}>
              <Card className="overflow-hidden p-0">
                <div className="flex items-start justify-between gap-3 border-b border-border p-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <Link2 className="h-4 w-4 shrink-0 text-muted-foreground" />
                      <span className="truncate text-sm font-medium">{g.title}</span>
                      <Badge>{t('share_links.link_count', { count: g.links.length })}</Badge>
                    </div>
                    <code className="mt-1 block truncate font-mono text-xs text-muted-foreground">{g.documentId}</code>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setPendingRevokeAll(g)}
                    disabled={revokeAll.isPending}
                  >
                    <ShieldOff className="h-4 w-4" /> {t('share_links.revoke_all')}
                  </Button>
                </div>
                <div className="overflow-x-auto">
                  <table className="w-full text-xs">
                    <thead className="bg-muted/40">
                      <tr>
                        {[t('share_links.col_status'), t('share_links.col_permissions'), t('share_links.col_views'),
                      t('share_links.col_expires'), t('share_links.col_accessed'),
                      t('share_links.col_created'), t('share_links.col_by'), ''].map((h) => (
                          <th key={h} className="px-3 py-2 text-start font-medium uppercase tracking-wider text-muted-foreground">
                            {h}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {g.links.map((l) => (
                        <tr key={l.id} className="border-t border-border transition-colors hover:bg-muted/30">
                          <td className="px-3 py-2">
                            <div className="flex items-center gap-1.5">
                              <Badge variant={l.is_active ? 'active' : 'archived'}>{l.is_active ? t('share_links.status_active') : t('share_links.status_revoked')}</Badge>
                              {l.password_protected && (
                                <Lock className="h-3 w-3 text-muted-foreground" aria-label={t('share_links.password_protected')} />
                              )}
                            </div>
                          </td>
                          <td className="px-3 py-2">
                            <div className="flex flex-wrap gap-1">
                              {l.permissions.length === 0
                                ? <span className="text-muted-foreground">—</span>
                                : l.permissions.map((p) => (
                                    <Badge key={p} variant="outline" className="text-[10px]">{t(`share_links.perm_${p}`, { defaultValue: p })}</Badge>
                                  ))}
                            </div>
                          </td>
                          <td className="px-3 py-2">
                            <span className="inline-flex items-center gap-1 text-muted-foreground tabular-nums">
                              <Eye className="h-3 w-3" />
                              {l.view_count}{l.max_views > 0 && ` / ${l.max_views}`}
                            </span>
                          </td>
                          <td className="px-3 py-2"><TimeCell iso={l.expires_at} fallback={t('share_links.never')} warnPast /></td>
                          <td className="px-3 py-2"><TimeCell iso={l.accessed_at} fallback={t('share_links.never')} /></td>
                          <td className="px-3 py-2"><TimeCell iso={l.created_at} /></td>
                          <td className="max-w-[140px] truncate px-3 py-2 text-muted-foreground" title={nameOf(l.created_by)}>
                            {nameOf(l.created_by)}
                          </td>
                          <td className="px-3 py-2 text-end">
                            {l.is_active && (
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => setPendingRevoke(l)}
                                disabled={revoke.isPending}
                                aria-label={t('share_links.revoke_aria', { title: g.title })}
                                title={t('share_links.revoke_one')}
                              >
                                <Trash2 className="h-3 w-3 text-destructive" />
                              </Button>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Card>
            </li>
          ))}
        </ul>
      )}

      {/* Single-link revoke — was a one-click hard kill; recipients get
          410 immediately, so it deserves the same confirm as bulk. */}
      <ConfirmDialog
        open={!!pendingRevoke}
        onOpenChange={(o) => !o && setPendingRevoke(null)}
        title={t('share_links.revoke_one_title')}
        description={
          pendingRevoke
            ? `The link to "${pendingRevoke.document_title || pendingRevoke.document_id}" stops working immediately — anyone holding it gets 410 Gone. This cannot be undone.`
            : ''
        }
        confirmLabel={t('share_links.revoke_one_confirm')}
        destructive
        loading={revoke.isPending}
        onConfirm={() => pendingRevoke && revoke.mutate(pendingRevoke.id)}
      />

      <ConfirmDialog
        open={!!pendingRevokeAll}
        onOpenChange={(o) => !o && setPendingRevokeAll(null)}
        title={pendingRevokeAll
          ? t('share_links.revoke_all_title', { title: pendingRevokeAll.title })
          : t('share_links.revoke_all')}
        description={
          pendingRevokeAll
            ? t('share_links.revoke_all_desc', {
                count: pendingRevokeAll.links.filter((l) => l.is_active).length,
              })
            : ''
        }
        confirmLabel={t('share_links.revoke_all')}
        destructive
        loading={revokeAll.isPending}
        onConfirm={() => pendingRevokeAll && revokeAll.mutate(pendingRevokeAll.documentId)}
      />
    </div>
  )
}

function SegmentedFilter({ value, onChange }: { value: Filter; onChange: (v: Filter) => void }) {
  const { t } = useTranslation('common')
  const opts: { value: Filter; label: string }[] = [
    { value: 'active', label: t('share_links.filter_active') },
    { value: 'all', label: t('share_links.filter_all') },
  ]
  return (
    <div className="inline-flex gap-1 rounded-md bg-muted/60 p-1 text-xs" role="tablist">
      {opts.map((o) => {
        const active = value === o.value
        return (
          <button
            key={o.value}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(o.value)}
            className={cn(
              'rounded-sm px-3 py-1.5 transition-all',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              active ? 'bg-background text-foreground shadow-neu-sm font-medium' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/share-links')({ component: ShareLinksPage })
