import { useRef, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Upload, Sparkles, FolderOpen, FileText, Users } from 'lucide-react'

import { useDocuments } from '@/hooks/useDocuments'
import { useUpload } from '@/hooks/useUpload'
import { getWorkspace } from '@/api/workspaces'
import { DocumentList } from '@/components/documents/DocumentList'
import { DocumentViewerModal } from '@/components/documents/DocumentViewerModal'
import { useNavigate } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Link } from '@tanstack/react-router'
import { Settings as SettingsIcon } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { WorkspaceAISettingsDialog } from '@/components/intelligence/WorkspaceAISettings'
import { UploadReviewDialog } from '@/components/documents/UploadReviewDialog'
import type { Document as ApiDocument } from '@/types/api'

// ADR 0102 — review threshold. At or below this size we open the
// per-file FilingSuggestionPanel review dialog. Larger batches skip
// the dialog entirely (auto-apply nothing, just upload) so the bulk
// UX stays fast — Phase 2 will auto-apply only high-confidence
// suggestions in that mode without showing the dialog.
const REVIEW_BATCH_MAX = 5

function WorkspacePage() {
  const { workspaceId } = Route.useParams()
  // ?doc=<uuid> drives the document viewer modal. URL-state means the
  // overlay is bookmarkable and survives back/forward. Direct URLs of
  // the form /workspaces/.../documents/<id> still hit the full-page
  // route — this modal is just the grid-side shortcut.
  const search = Route.useSearch() as { doc?: string }
  const navigate = useNavigate({ from: '/workspaces/$workspaceId/' })
  const closeViewer = () =>
    navigate({ search: (s: Record<string, unknown>) => ({ ...s, doc: undefined }) })
  const ws = useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => getWorkspace(workspaceId),
    staleTime: 60_000,
  })
  const { data, isLoading } = useDocuments(workspaceId)
  const { uploadFiles } = useUpload(workspaceId)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [aiOpen, setAiOpen] = useState(false)
  const [dragOver, setDragOver] = useState(false)

  const role = useAuthStore((s) => s.user?.role)
  // Workspace AI settings are admin-curated; matches the policy
  // service's owner|admin gate on PUT. Hide for non-admins so they
  // don't see a button that always 403s.
  const isAdmin = role === 'owner' || role === 'admin'

  // ADR 0102 — pending batch awaiting filing review. null = dialog
  // closed; an array = dialog open over those files.
  const [reviewFiles, setReviewFiles] = useState<File[] | null>(null)

  const startUpload = (files: File[]) => {
    if (!files.length) return
    if (files.length <= REVIEW_BATCH_MAX) {
      setReviewFiles(files)
      return
    }
    // Bulk path: skip the review dialog, just upload.
    uploadFiles(files)
  }

  const onPick = () => fileInputRef.current?.click()
  const onChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files
    if (!files || files.length === 0) return
    startUpload(Array.from(files))
    e.target.value = ''
  }

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(false)
    const files = Array.from(e.dataTransfer.files ?? [])
    if (files.length) startUpload(files)
  }

  return (
    <div
      onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
      onDragLeave={(e) => { if (e.currentTarget === e.target) setDragOver(false) }}
      onDrop={onDrop}
      className="relative space-y-6"
    >
      <input ref={fileInputRef} type="file" multiple className="hidden" onChange={onChange} />

      <PageHeader
        title={
          ws.isLoading ? (
            <Skeleton className="h-7 w-48" />
          ) : (
            <span className="flex items-center gap-2">
              <FolderOpen className="h-5 w-5 text-muted-foreground" />
              {ws.data?.name ?? 'Workspace'}
            </span>
          )
        }
        description={
          ws.data ? (
            <span className="flex flex-wrap items-center gap-x-4 gap-y-1">
              {ws.data.description && <span>{ws.data.description}</span>}
              <span className="flex items-center gap-1 text-xs">
                <FileText className="h-3.5 w-3.5" />
                {ws.data.document_count.toLocaleString()} {ws.data.document_count === 1 ? 'document' : 'documents'}
              </span>
              <span className="flex items-center gap-1 text-xs">
                <Users className="h-3.5 w-3.5" />
                {ws.data.member_count ?? 0} {(ws.data.member_count ?? 0) === 1 ? 'member' : 'members'}
              </span>
            </span>
          ) : ws.isLoading ? <Skeleton className="h-4 w-72" /> : undefined
        }
        actions={
          <>
            {isAdmin && (
              <Button variant="ghost" onClick={() => setAiOpen(true)} data-testid="open-ai-settings">
                <Sparkles className="h-4 w-4" /> AI settings
              </Button>
            )}
            {isAdmin && (
              <Button variant="ghost" asChild data-testid="open-ws-settings">
                <Link
                  to="/workspaces/$workspaceId/settings"
                  params={{ workspaceId }}
                  aria-label="Workspace settings"
                >
                  <SettingsIcon className="h-4 w-4" /> Settings
                </Link>
              </Button>
            )}
            <Button onClick={onPick} data-testid="open-upload">
              <Upload className="h-4 w-4" /> Upload
            </Button>
          </>
        }
      />

      {/* Backend ListDocumentsResponse uses `documents`, not `items` —
          the PaginatedResponse<T> type's `items` field is wrong for
          this endpoint. Read both for resilience. */}
      <DocumentList
        documents={((data as unknown as { documents?: unknown[]; items?: unknown[] })?.documents
          ?? (data as unknown as { items?: unknown[] })?.items
          ?? []) as ApiDocument[]}
        isLoading={isLoading}
      />

      <WorkspaceAISettingsDialog open={aiOpen} onOpenChange={setAiOpen} workspaceId={workspaceId} />

      {/* ADR 0102 — predictive filing review. Mounted only when a
          batch is pending so each open call gets a fresh useState. */}
      {reviewFiles && (
        <UploadReviewDialog
          open
          files={reviewFiles}
          workspaceId={workspaceId}
          onCancel={() => setReviewFiles(null)}
          onConfirm={(decisions) => {
            const files = reviewFiles
            setReviewFiles(null)
            uploadFiles(files, decisions)
          }}
        />
      )}

      {/* Document viewer modal — opens when ?doc=<id> is present. The
          modal hosts the FULL document detail surface (all 9 tabs +
          sidebar + every dialog the page can spawn). Close clears the
          search param. Direct-URL navigation to /workspaces/.../
          documents/<id> still renders the full page unchanged. */}
      <DocumentViewerModal
        open={!!search.doc}
        onOpenChange={(o) => { if (!o) closeViewer() }}
        documentId={search.doc ?? null}
        workspaceId={workspaceId}
      />

      {/* Full-page drop overlay shown only while a drag is active.
          The visual matches the upload button so users connect the
          dots: dropping anywhere → same flow as clicking Upload. */}
      {dragOver && (
        <div
          className="pointer-events-none fixed inset-0 z-40 flex items-center justify-center bg-background/80 backdrop-blur-sm"
          aria-hidden
        >
          <div className="flex flex-col items-center gap-3 rounded-xl border-2 border-dashed border-foreground bg-card px-10 py-8 text-center shadow-xl">
            <span className="flex h-12 w-12 items-center justify-center rounded-full bg-foreground text-background">
              <Upload className="h-6 w-6" />
            </span>
            <p className="text-base font-semibold">Drop to upload</p>
            <p className="max-w-xs text-sm text-muted-foreground">
              Files will be added to <strong className="text-foreground">{ws.data?.name ?? 'this workspace'}</strong>.
            </p>
          </div>
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/')({
  component: WorkspacePage,
  // ?doc=<uuid> opens DocumentViewerModal as an overlay; URL-driven so
  // back-button and bookmarks behave naturally. Anything else in search
  // passes through (e.g. future ?folder= or ?filter= params).
  validateSearch: (raw: Record<string, unknown>): { doc?: string } => ({
    doc: typeof raw.doc === 'string' && raw.doc.length > 0 ? raw.doc : undefined,
  }),
})
