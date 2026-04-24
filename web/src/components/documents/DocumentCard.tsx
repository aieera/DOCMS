import type { Document } from '@/types/api'
import { Badge } from '@/components/ui/Badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'
import { isPreviewable } from '@/lib/preview'
import { Link } from '@tanstack/react-router'

export function DocumentCard({ doc }: { doc: Document }) {
  // "Generating preview" only appears for docs we *expect* to get a
  // preview for (PDF/image today). Text docs never preview, so no
  // chip — matches the preview worker's supported-mime list.
  const generating = !doc.has_thumbnail && isPreviewable(doc.mime_type)

  return (
    <Link
      to="/workspaces/$workspaceId/documents/$documentId"
      params={{ workspaceId: doc.workspace_id, documentId: doc.id }}
      className="group flex flex-col rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 transition-shadow hover:shadow-md"
    >
      <div className="mb-3 flex items-center gap-3">
        {doc.has_thumbnail && doc.thumbnail_url ? (
          <img
            src={doc.thumbnail_url}
            alt=""
            loading="lazy"
            className="h-10 w-10 rounded object-cover"
          />
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
      <div className="flex items-center justify-between gap-2">
        <Badge variant={doc.lifecycle_state}>{doc.lifecycle_state}</Badge>
        {generating && (
          <span className="rounded-full bg-amber-100 px-2 py-0.5 text-[10px] font-medium text-amber-900 dark:bg-amber-900/30 dark:text-amber-200">
            Generating preview…
          </span>
        )}
        <span className="ml-auto text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(doc.created_at)}</span>
      </div>
    </Link>
  )
}
