import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/shadcn/badge'
import { type ColumnDef } from '@tanstack/react-table'
import { formatDateTime, formatShortId } from '@/lib/formatters'
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

const columns: ColumnDef<AuditEntry, unknown>[] = [
  {
    accessorKey: 'created_at',
    header: 'Time',
    cell: ({ row }) => <span className="whitespace-nowrap text-xs text-muted-foreground">{formatDateTime(row.original.created_at)}</span>,
  },
  {
    accessorKey: 'actor_name',
    header: 'Actor',
    cell: ({ row }) => {
      const { actor_name, actor } = row.original
      if (actor_name) return <span className="text-sm font-medium">{actor_name}</span>
      if (actor) return <span className="font-mono text-xs text-muted-foreground" title={actor}>{formatShortId('usr', actor)}</span>
      return <span className="text-sm text-muted-foreground">system</span>
    },
  },
  {
    accessorKey: 'action',
    header: 'Action',
    // SD-11 retest: the raw event topic (dms.document.deleted.v1) is fine
    // in a CSV export, not as the primary Action label. Humanized label
    // up front; the machine name stays one hover away.
    cell: ({ row }) => (
      <Badge title={row.original.action}>{humanizeEventSummary(row.original.action)}</Badge>
    ),
  },
  {
    accessorKey: 'resource_type',
    header: 'Resource',
    cell: ({ row }) => {
      const { resource_type, resource_id } = row.original
      if (!resource_type && !resource_id) {
        return <span className="text-sm text-muted-foreground">—</span>
      }
      return <span className="text-sm">{resource_type || '—'}</span>
    },
  },
  {
    accessorKey: 'ip_address',
    header: 'IP',
    cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.ip_address || '—'}</span>,
  },
]

export function AuditLogTable({ entries, isLoading }: { entries: AuditEntry[]; isLoading?: boolean }) {
  return (
    <DataTable
      columns={columns}
      data={entries}
      isLoading={isLoading}
      emptyState="No activity recorded yet."
      density="compact"
    />
  )
}
