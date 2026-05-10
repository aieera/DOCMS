import { useQuery } from '@tanstack/react-query'
import { getVersions } from '@/api/documents'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'
import { Button } from '@/components/ui/shadcn/button'
import { Download, RotateCcw } from 'lucide-react'

export function VersionHistory({ documentId }: { documentId: string }) {
  const { data, isLoading } = useQuery({ queryKey: ['versions', documentId], queryFn: () => getVersions(documentId) })

  if (isLoading) return <div className="space-y-2">{[1, 2, 3].map((i) => <Skeleton key={i} className="h-14" />)}</div>

  return (
    <div className="space-y-2">
      {data?.map((v) => (
        <div key={v.id} className="flex items-start gap-3 rounded-md border border-[var(--color-border)] p-3">
          <Avatar name={v.created_by_name} size="sm" />
          <div className="min-w-0 flex-1">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">v{v.version_number}</span>
              <span className="text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(v.created_at)}</span>
            </div>
            {v.change_summary && <p className="mt-0.5 text-xs text-[var(--color-text-secondary)]">{v.change_summary}</p>}
            <div className="mt-1 flex items-center gap-2">
              <span className="text-xs text-[var(--color-text-secondary)]">{formatFileSize(v.size_bytes)}</span>
              <Button variant="ghost" size="sm" className="h-6 px-1.5"><Download className="h-3 w-3" /></Button>
              <Button variant="ghost" size="sm" className="h-6 px-1.5"><RotateCcw className="h-3 w-3" /></Button>
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}
