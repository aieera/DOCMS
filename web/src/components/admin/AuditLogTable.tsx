import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/Badge'
import { type ColumnDef } from '@tanstack/react-table'
import { formatDateTime } from '@/lib/formatters'

interface AuditEntry {
  id: string
  actor_name: string
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
    cell: ({ row }) => <span className="text-sm font-medium">{row.original.actor_name}</span>,
  },
  {
    accessorKey: 'action',
    header: 'Action',
    cell: ({ row }) => <Badge>{row.original.action}</Badge>,
  },
  {
    accessorKey: 'resource_type',
    header: 'Resource',
    cell: ({ row }) => (
      <span className="text-sm">
        {row.original.resource_type}
        <span className="text-muted-foreground">/{row.original.resource_id.slice(0, 8)}</span>
      </span>
    ),
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
