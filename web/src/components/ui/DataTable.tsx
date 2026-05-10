import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table'
import { useState, type ReactNode } from 'react'
import { ArrowDown, ArrowUp, ArrowUpDown, Loader2 } from 'lucide-react'
import { cn } from '@/lib/cn'

interface DataTableProps<T> {
  columns: ColumnDef<T, unknown>[]
  data: T[]
  onRowClick?: (row: T) => void
  // Optional toolbar rendered above the table inside the same border —
  // typical content: search input, filters, bulk actions. Pass null
  // (or omit) for a plain table.
  toolbar?: ReactNode
  // Loading shows a one-row skeleton placeholder so the table doesn't
  // collapse to height 0 while data resolves.
  isLoading?: boolean
  // Empty rendered when data.length === 0 + !isLoading. Allows
  // per-route copy without callers having to wrap the table.
  emptyState?: ReactNode
  // Visual density. "compact" trims row padding for dense admin
  // tables (audit log, signers, etc).
  density?: 'comfortable' | 'compact'
}

export function DataTable<T>({
  columns,
  data,
  onRowClick,
  toolbar,
  isLoading,
  emptyState,
  density = 'comfortable',
}: DataTableProps<T>) {
  const [sorting, setSorting] = useState<SortingState>([])
  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  })

  const cellPad = density === 'compact' ? 'px-3 py-2' : 'px-4 py-3'
  const headPad = density === 'compact' ? 'px-3 py-2' : 'px-4 py-2.5'

  const rows = table.getRowModel().rows

  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card">
      {toolbar && (
        <div className="flex items-center justify-between gap-3 border-b border-border bg-card p-3">
          {toolbar}
        </div>
      )}
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-muted/40">
            {table.getHeaderGroups().map((hg) => (
              <tr key={hg.id}>
                {hg.headers.map((h) => {
                  const sort = h.column.getIsSorted()
                  const sortable = h.column.getCanSort()
                  return (
                    <th
                      key={h.id}
                      className={cn(
                        headPad,
                        'text-start text-xs font-medium uppercase tracking-wider text-muted-foreground',
                      )}
                    >
                      {h.isPlaceholder ? null : sortable ? (
                        <button
                          type="button"
                          className="inline-flex items-center gap-1 transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card"
                          onClick={h.column.getToggleSortingHandler()}
                        >
                          {flexRender(h.column.columnDef.header, h.getContext())}
                          {sort === 'asc' ? <ArrowUp className="h-3 w-3" /> : sort === 'desc' ? <ArrowDown className="h-3 w-3" /> : <ArrowUpDown className="h-3 w-3 opacity-50" />}
                        </button>
                      ) : (
                        flexRender(h.column.columnDef.header, h.getContext())
                      )}
                    </th>
                  )
                })}
              </tr>
            ))}
          </thead>
          <tbody>
            {isLoading && rows.length === 0 ? (
              <tr>
                <td colSpan={columns.length} className={cn(cellPad, 'text-center text-sm text-muted-foreground')}>
                  <span className="inline-flex items-center gap-2">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    Loading…
                  </span>
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={columns.length} className={cn(cellPad, 'text-center text-sm text-muted-foreground')}>
                  {emptyState ?? 'No results.'}
                </td>
              </tr>
            ) : (
              rows.map((row) => (
                <tr
                  key={row.id}
                  onClick={() => onRowClick?.(row.original)}
                  className={cn(
                    'border-t border-border transition-colors hover:bg-muted/40',
                    onRowClick && 'cursor-pointer',
                  )}
                >
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id} className={cellPad}>
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </td>
                  ))}
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
