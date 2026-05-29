import type { Document } from '@/types/api'
import { Badge } from '@/components/ui/shadcn/badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { formatFileSize, formatRelativeTime, lifecycleStateLabel } from '@/lib/formatters'
import { Link } from '@tanstack/react-router'
import { DocumentActionsMenu } from './DocumentActionsMenu'

export function DocumentCard({ doc }: { doc: Document }) {
  return (
    <DocumentActionsMenu doc={doc}>
      {/* Card click opens the document viewer as a modal overlay over
          the workspace grid. URL becomes ?doc=<id> so the overlay is
          bookmarkable + back-button friendly. The direct-URL route
          /workspaces/.../documents/<id> still works for deep-links
          arriving from outside (task→doc nav, email links, etc.). */}
      <Link
        to="/workspaces/$workspaceId"
        params={{ workspaceId: doc.workspace_id }}
        search={{ doc: doc.id }}
        className="group flex flex-col rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 transition-shadow hover:shadow-md"
        data-testid={`document-card-${doc.id}`}
      >
        <div className="mb-3 flex items-center gap-3">
          {doc.has_thumbnail && doc.thumbnail_url ? (
            <img src={doc.thumbnail_url} alt="" className="h-10 w-10 rounded object-cover" />
          ) : (
            <div className="flex h-10 w-10 items-center justify-center rounded bg-slate-100 dark:bg-slate-700">
              <FileIcon mime={doc.mime_type} />
            </div>
          )}
          <div className="min-w-0 flex-1 pe-8">
            <p className="truncate text-sm font-medium group-hover:text-[var(--color-primary)]" title={doc.title}>{doc.title}</p>
            <p className="text-xs text-[var(--color-text-secondary)]">{formatFileSize(doc.size_bytes)}</p>
          </div>
        </div>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1">
            <Badge variant={doc.lifecycle_state}>{lifecycleStateLabel(doc.lifecycle_state)}</Badge>
            {!(doc as unknown as { current_version_id?: string }).current_version_id && (
              <span
                title="Document has no uploaded content yet"
                className="rounded-full bg-amber-100 px-2 py-0.5 text-xs text-amber-800 dark:bg-amber-900 dark:text-amber-200"
              >
                No content
              </span>
            )}
          </div>
          <span className="text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(doc.created_at)}</span>
        </div>
      </Link>
    </DocumentActionsMenu>
  )
}
