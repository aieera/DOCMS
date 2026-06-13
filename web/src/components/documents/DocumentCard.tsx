import type { Document } from '@/types/api'
import { Badge } from '@/components/ui/shadcn/badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { formatFileSize, formatRelativeTime, lifecycleStateLabel } from '@/lib/formatters'
import { Link } from '@tanstack/react-router'
import { DocumentActionsMenu } from './DocumentActionsMenu'
import { WorkflowStatusBadge } from '@/components/workflows/WorkflowStatusBadge'

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
        className="group flex flex-col rounded-2xl border border-border bg-card p-4 shadow-sm transition-shadow hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
        data-testid={`document-card-${doc.id}`}
      >
        <div className="mb-3 flex items-center gap-3">
          {doc.has_thumbnail && doc.thumbnail_url ? (
            <img src={doc.thumbnail_url} alt="" className="h-10 w-10 rounded-lg object-cover" />
          ) : (
            <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-muted text-muted-foreground">
              <FileIcon mime={doc.mime_type} />
            </div>
          )}
          <div className="min-w-0 flex-1 pe-8">
            <p className="truncate text-sm font-medium group-hover:text-primary" title={doc.title}>{doc.title}</p>
            <p className="text-xs text-muted-foreground">{formatFileSize(doc.total_size_bytes)}</p>
          </div>
        </div>
        <div className="flex items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-1">
            <Badge variant={doc.lifecycle_state}>{lifecycleStateLabel(doc.lifecycle_state)}</Badge>
            {!(doc as unknown as { current_version_id?: string }).current_version_id && (
              <Badge variant="warning" title="Document has no uploaded content yet">
                No content
              </Badge>
            )}
            {/* ADR 0115 — processing-failure badge, distinct from the
                no-file-uploaded "No content" case. Renders only once the
                list response carries processing_status; until then the
                doc-detail banner is the surface. */}
            {(() => {
              const ps = (doc as unknown as { processing_status?: string }).processing_status
              return ps === 'failed' || ps === 'partial' ? (
                <Badge variant="destructive" title="Intelligence processing failed — open the document to retry">
                  Processing failed
                </Badge>
              ) : null
            })()}
            {doc.workflow_instance && (
              <WorkflowStatusBadge status={doc.workflow_instance.status} size="sm" />
            )}
          </div>
          <span className="shrink-0 text-xs text-muted-foreground">{formatRelativeTime(doc.created_at)}</span>
        </div>
      </Link>
    </DocumentActionsMenu>
  )
}
