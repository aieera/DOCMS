import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/Badge'
import { type ColumnDef } from '@tanstack/react-table'
import { formatDateTime } from '@/lib/formatters'
import { Button } from '@/components/ui/Button'
import { Download } from 'lucide-react'

interface AuditEntry { id: string; actor_name: string; action: string; resource_type: string; resource_id: string; created_at: string; ip_address?: string }

const columns: ColumnDef<AuditEntry, unknown>[] = [
  { accessorKey: 'created_at', header: 'Time', cell: ({ row }) => <span className="text-xs">{formatDateTime(row.original.created_at)}</span> },
  { accessorKey: 'actor_name', header: 'Actor', cell: ({ row }) => <span className="text-sm font-medium">{row.original.actor_name}</span> },
  { accessorKey: 'action', header: 'Action', cell: ({ row }) => <Badge>{row.original.action}</Badge> },
  { accessorKey: 'resource_type', header: 'Resource', cell: ({ row }) => <span className="text-sm">{row.original.resource_type}/{row.original.resource_id.slice(0, 8)}</span> },
  { accessorKey: 'ip_address', header: 'IP', cell: ({ row }) => <span className="font-mono text-xs">{row.original.ip_address || '—'}</span> },
]

export function AuditLogTable({ entries, onExport }: { entries: AuditEntry[]; onExport?: () => void }) {
  return (
    <div>
      {onExport && (
        <div className="mb-3 flex justify-end">
          <Button variant="outline" size="sm" onClick={onExport}><Download className="h-4 w-4" /> Export CSV</Button>
        </div>
      )}
      <DataTable columns={columns} data={entries} />
    </div>
  )
}
