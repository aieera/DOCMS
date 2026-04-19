import { flexRender, getCoreRowModel, getSortedRowModel, useReactTable, type ColumnDef, type SortingState } from '@tanstack/react-table'
import { useState } from 'react'
import { ArrowUpDown } from 'lucide-react'
import { cn } from '@/lib/cn'

interface DataTableProps<T> { columns: ColumnDef<T, unknown>[]; data: T[]; onRowClick?: (row: T) => void }

export function DataTable<T>({ columns, data, onRowClick }: DataTableProps<T>) {
  const [sorting, setSorting] = useState<SortingState>([])
  const table = useReactTable({ data, columns, state: { sorting }, onSortingChange: setSorting, getCoreRowModel: getCoreRowModel(), getSortedRowModel: getSortedRowModel() })

  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
      <table className="w-full text-sm">
        <thead className="bg-slate-50 dark:bg-slate-800/50">
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id}>
              {hg.headers.map((h) => (
                <th key={h.id} className="px-4 py-2.5 text-start text-xs font-medium text-[var(--color-text-secondary)]">
                  {h.isPlaceholder ? null : (
                    <button className="inline-flex items-center gap-1" onClick={h.column.getToggleSortingHandler()}>
                      {flexRender(h.column.columnDef.header, h.getContext())}
                      {h.column.getCanSort() && <ArrowUpDown className="h-3 w-3" />}
                    </button>
                  )}
                </th>
              ))}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.map((row) => (
            <tr key={row.id} onClick={() => onRowClick?.(row.original)} className={cn('border-t border-[var(--color-border)] transition-colors hover:bg-slate-50 dark:hover:bg-slate-800/30', onRowClick && 'cursor-pointer')}>
              {row.getVisibleCells().map((cell) => (
                <td key={cell.id} className="px-4 py-2.5">{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
