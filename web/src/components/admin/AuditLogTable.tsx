import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/shadcn/badge'
import { type ColumnDef } from '@tanstack/react-table'
import { formatDateTime, formatShortId, resourceTypeLabel } from '@/lib/formatters'
import { humanizeEventSummary } from '@/lib/eventSummary'

interface AuditEntry {
  id: string
  /** actor UUID. Always populated. */
  actor?: string
  /** human-readable name (email). Empty for service-internal writers
   *  (gRPC tenant interceptor + API-key auth don't populate Email on
   *  ctx, so the outbox row + downstream audit_events row land with
   *  actor_name=""). Fall back to a short-id on the actor UUID so the
   *  column is never visually empty when an actor IS recorded. */
  actor_name?: string
  action: string
  resource_type: string
  resource_id: string
  created_at: string
  ip_address?: string
}

function useAuditColumns(): ColumnDef<AuditEntry, unknown>[] {
  const { t } = useTranslation('common')
  return useMemo(() => [
  {
    accessorKey: 'created_at',
    header: t('adm.aud.col_time'),
    cell: ({ row }) => <span className="whitespace-nowrap text-xs text-muted-foreground">{formatDateTime(row.original.created_at)}</span>,
  },
  {
    accessorKey: 'actor_name',
    header: t('adm.aud.col_actor'),
    cell: ({ row }) => {
      const { actor_name, actor } = row.original
      if (actor_name) return <span className="text-sm font-medium">{actor_name}</span>
      if (actor) return <span className="font-mono text-xs text-muted-foreground" title={actor}>{formatShortId('usr', actor)}</span>
      return <span className="text-sm text-muted-foreground">{t('adm.aud.system')}</span>
    },
  },
  {
    accessorKey: 'action',
    header: t('adm.aud.col_action'),
    // SD-11 retest: the raw event topic (dms.document.deleted.v1) is fine
    // in a CSV export, not as the primary Action label. Humanized label
    // up front; the machine name stays one hover away.
    cell: ({ row }) => (
      <Badge title={row.original.action}>{humanizeEventSummary(row.original.action)}</Badge>
    ),
  },
  {
    accessorKey: 'resource_type',
    header: t('adm.aud.col_resource'),
    cell: ({ row }) => {
      const { resource_type, resource_id } = row.original
      if (!resource_type && !resource_id) {
        return <span className="text-sm text-muted-foreground">—</span>
      }
      return <span className="text-sm">{resourceTypeLabel(resource_type) || '—'}</span>
    },
  },
  {
    accessorKey: 'ip_address',
    header: t('adm.aud.col_ip'),
    cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.ip_address || '—'}</span>,
  },
  ], [t])
}

export function AuditLogTable({ entries, isLoading }: { entries: AuditEntry[]; isLoading?: boolean }) {
  const { t } = useTranslation('common')
  const columns = useAuditColumns()
  return (
    <DataTable
      columns={columns}
      data={entries}
      isLoading={isLoading}
      emptyState={t('adm.aud.empty')}
      density="compact"
    />
  )
}
