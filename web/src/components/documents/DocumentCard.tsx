import type { Document } from '@/types/api'
import { Badge } from '@/components/ui/Badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'
import { Link } from '@tanstack/react-router'

export function DocumentCard({ doc }: { doc: Document }) {
  return (
    <Link
      to="/workspaces/$workspaceId/documents/$documentId"
      params={{ workspaceId: doc.workspace_id, documentId: doc.id }}
      className="group flex flex-col rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 transition-shadow hover:shadow-md"
    >
      <div className="mb-3 flex items-center gap-3">
        {doc.has_thumbnail && doc.thumbnail_url ? (
          <img src={doc.thumbnail_url} alt="" className="h-10 w-10 rounded object-cover" />
        ) : (
          <div className="flex h-10 w-10 items-center justify-center rounded bg-slate-100 dark:bg-slate-700">
            <FileIcon mime={doc.mime_type} />
          </div>
        )}
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium group-hover:text-[var(--color-primary)]">{doc.title}</p>
          <p className="text-xs text-[var(--color-text-secondary)]">{formatFileSize(doc.size_bytes)}</p>
        </div>
      </div>
      <div className="flex items-center justify-between">
        <Badge variant={doc.lifecycle_state}>{doc.lifecycle_state}</Badge>
        <span className="text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(doc.created_at)}</span>
      </div>
    </Link>
  )
}
