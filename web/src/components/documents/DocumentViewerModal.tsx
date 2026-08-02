import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Maximize2, X } from 'lucide-react'

import { getDocument } from '@/api/documents'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { FileIcon } from '@/components/ui/FileIcon'
import {
  Dialog as DialogRoot,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/shadcn/dialog'
import { VisuallyHidden } from '@radix-ui/react-visually-hidden'
import { lifecycleStateLabel, formatFileSize } from '@/lib/formatters'
import { DocumentDetailBody } from '@/routes/_authenticated/workspaces/$workspaceId/documents/$documentId'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Document id to display. When null/undefined, the dialog renders no body. */
  documentId: string | null
  /** Workspace id the user came from. Used for the inner body's breadcrumb +
   * back-to-workspace links so the modal behaves the same as the full page. */
  workspaceId: string
}

/**
 * Overlay variant of the document detail surface.
 *
 * Reuses the same `<DocumentDetailBody />` the route renders, just wrapped
 * in a Dialog. All 9 tabs, all sidebar panels, every banner, every nested
 * dialog (ManageAccess, RetentionExempt, Share, Compare, VersionHistory
 * Sheet, ConfirmDialogs, etc.) continue to work — Radix portals stack
 * above this dialog correctly.
 *
 * Direct URL access (`/workspaces/.../documents/<id>`) still renders the
 * full-page route unchanged; this modal is purely the workspace-grid
 * shortcut. URL-driven open state means deep-links remain bookmarkable.
 */
export function DocumentViewerModal({ open, onOpenChange, documentId, workspaceId }: Props) {
  // Surface a header from the doc itself (icon + title + size + lifecycle
  // badge). Hits the same `['document', documentId]` cache key as the inner
  // body's useDocument hook, so React Query dedupes — one network call, two
  // readers. Skipped entirely when documentId is null.
  const { data: doc } = useQuery({
    queryKey: ['document', documentId],
    queryFn: () => getDocument(documentId!),
    enabled: open && !!documentId,
    staleTime: 30_000,
  })

  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogContent
        hideCloseButton
        className="flex h-[92vh] max-w-[94vw] flex-col gap-0 overflow-hidden rounded-xl p-0 sm:max-w-[94vw]"
        data-testid="document-viewer-modal"
      >
        {/* Radix requires a Title + Description for accessibility even when
            the visual header is custom. Hide them visually, expose to AT. */}
        <VisuallyHidden>
          <DialogTitle>{doc?.title ?? 'Document viewer'}</DialogTitle>
          <DialogDescription>
            Document details, preview, comments, versions, and actions.
          </DialogDescription>
        </VisuallyHidden>

        <header className="flex shrink-0 items-center gap-3 border-b border-border bg-card/60 px-5 py-3 backdrop-blur">
          {/* File-type icon — same family the workspace grid uses, so the
              modal opens with visual continuity from the card. */}
          {doc ? (
            <FileIcon mime={doc.mime_type} className="h-6 w-6 shrink-0" />
          ) : (
            <div className="h-6 w-6 shrink-0 animate-pulse rounded bg-muted" />
          )}

          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex min-w-0 items-center gap-2">
              <h2
                className="truncate text-sm font-semibold leading-none"
                data-testid="document-viewer-modal-title"
              >
                {doc?.title ?? 'Loading…'}
              </h2>
              {doc?.lifecycle_state && (
                <Badge variant={doc.lifecycle_state} className="shrink-0">
                  {lifecycleStateLabel(doc.lifecycle_state)}
                </Badge>
              )}
            </div>
            {doc && (
              <p className="mt-1 truncate text-xs text-muted-foreground">
                <code className="rounded bg-muted px-1 py-0.5 font-mono text-[10px]">
                  {doc.mime_type}
                </code>
                {doc.total_size_bytes ? (
                  <>
                    <span className="mx-1.5" aria-hidden>·</span>
                    {formatFileSize(doc.total_size_bytes)}
                  </>
                ) : null}
              </p>
            )}
          </div>

          <div className="flex shrink-0 items-center gap-1">
            {/* Open-as-page deep-link: power users want to pop the doc
                into its own tab for side-by-side work. Cmd/Ctrl+click
                opens a new tab; plain click navigates and closes the
                modal (TanStack Link handles both). */}
            {documentId && (
              <Button
                variant="ghost"
                size="icon"
                asChild
                aria-label="Open as full page"
                data-testid="document-viewer-modal-expand"
              >
                <Link
                  to="/workspaces/$workspaceId/documents/$documentId"
                  params={{ workspaceId, documentId }}
                  onClick={() => onOpenChange(false)}
                >
                  <Maximize2 className="h-4 w-4" />
                </Link>
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon"
              onClick={() => onOpenChange(false)}
              aria-label="Close document viewer"
              data-testid="document-viewer-modal-close"
            >
              <X className="h-4 w-4" />
            </Button>
          </div>
        </header>

        {/* Inner body. Scoped overflow-y-auto so the doc detail's internal
            scrollables (tab content + sidebar) behave correctly inside the
            fixed-height dialog. The body renders nothing meaningful until
            documentId is set — guards against flash on first open. */}
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto bg-background px-6 py-5">
          {documentId && workspaceId && (
            <DocumentDetailBody documentId={documentId} workspaceId={workspaceId} inModal />
          )}
        </div>
      </DialogContent>
    </DialogRoot>
  )
}
