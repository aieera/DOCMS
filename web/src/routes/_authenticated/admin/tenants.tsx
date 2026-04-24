import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Building2, Trash2, Undo2 } from 'lucide-react'

import { deprovisionTenant, listTenants, undoDeprovision, type Tenant } from '@/api/tenants'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { EmptyState } from '@/components/ui/EmptyState'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatRelativeTime } from '@/lib/formatters'

function lifecycleBadge(t: Tenant): { label: string; variant: string } {
  if (t.disposed_at) return { label: 'disposed', variant: 'disposed' }
  if (t.dispose_scheduled_at) return { label: 'soft-deleted', variant: 'in_review' }
  return { label: 'active', variant: 'active' }
}

function TenantsPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['control-plane', 'tenants'],
    queryFn: listTenants,
  })

  const deprovision = useMutation({
    mutationFn: (id: string) => deprovisionTenant(id),
    onSuccess: () => {
      toast.success('Tenant scheduled for disposal')
      qc.invalidateQueries({ queryKey: ['control-plane', 'tenants'] })
    },
    onError: () => toast.error('Deprovision failed'),
  })

  const undo = useMutation({
    mutationFn: (id: string) => undoDeprovision(id),
    onSuccess: () => {
      toast.success('Deprovision cancelled')
      qc.invalidateQueries({ queryKey: ['control-plane', 'tenants'] })
    },
    onError: () => toast.error('Cannot undo — tenant may already be disposed'),
  })

  return (
    <div>
      <PageHeader title="Tenants" description="Control-plane view of every organization" />
      {isLoading ? (
        <div className="space-y-2">{[1, 2, 3].map((i) => <Skeleton key={i} className="h-14" />)}</div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Building2 className="h-12 w-12" />}
          title="No tenants"
          description="No organizations have been provisioned yet."
        />
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-xs uppercase text-[var(--color-text-secondary)]">
            <tr>
              <th className="py-2">Name</th>
              <th>Slug</th>
              <th>Plan</th>
              <th>Region</th>
              <th>Created</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {data.map((t) => {
              const badge = lifecycleBadge(t)
              const softDeleted = !!t.dispose_scheduled_at && !t.disposed_at
              return (
                <tr key={t.id} className="border-t border-[var(--color-border)]">
                  <td className="py-2 font-medium">{t.name}</td>
                  <td className="font-mono text-xs">{t.slug}</td>
                  <td>{t.plan}</td>
                  <td>{t.primary_region}</td>
                  <td className="text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(t.created_at)}</td>
                  <td>
                    <Badge variant={badge.variant}>{badge.label}</Badge>
                    {softDeleted && t.dispose_scheduled_at && (
                      <div className="mt-0.5 text-[10px] text-[var(--color-text-secondary)]">
                        disposes {formatRelativeTime(t.dispose_scheduled_at)}
                      </div>
                    )}
                  </td>
                  <td className="text-right">
                    {t.disposed_at ? null : softDeleted ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={undo.isPending}
                        onClick={() => undo.mutate(t.id)}
                        title="Cancel deprovision"
                      >
                        <Undo2 className="h-4 w-4" /> Undo
                      </Button>
                    ) : (
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={deprovision.isPending}
                        onClick={() => {
                          if (confirm(`Deprovision ${t.name}? 30-day grace before hard dispose.`)) {
                            deprovision.mutate(t.id)
                          }
                        }}
                        title="Schedule 30-day soft delete"
                      >
                        <Trash2 className="h-4 w-4" /> Deprovision
                      </Button>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/tenants')({ component: TenantsPage })
