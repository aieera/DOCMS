import { useEffect, useMemo, useRef, useState } from 'react'
import { createFileRoute, Link, useSearch } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import {
  AlertCircle,
  CheckCircle2,
  CheckSquare,
  ChevronDown,
  Clock,
  Download,
  Eraser,
  FileText,
  GitBranch,
  GitCompareArrows,
  History,
  MessageSquare,
  MoreHorizontal,
  Network,
  RefreshCw,
  Share2,
  UserCog,
} from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'

import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { useDocument } from '@/hooks/useDocuments'
import { useDocumentDetailGQL, useActivityForDocument } from '@/hooks/useDocumentDetailGQL'
import { useAuthStore } from '@/store/authStore'
import { getOCR, rerunOCR, type OCRStatus } from '@/api/ocr'
import { getDownloadURL } from '@/api/documents'
import { getWorkspace } from '@/api/workspaces'
import { listEntities, type Entity } from '@/api/ner'
import { PDFLayoutViewer } from '@/components/viewer/PDFLayoutViewer'
import { DocumentPreview } from '@/components/viewer/DocumentPreview'
import { CoauthorEditor } from '@/components/viewer/CoauthorEditor'
import { ImageAnnotationLayer } from '@/components/viewer/ImageAnnotationLayer'
import { VideoAnnotationLayer } from '@/components/viewer/VideoAnnotationLayer'
import { CommentsPanel } from '@/components/documents/CommentsPanel'
import { RealtimePresence } from '@/components/documents/RealtimePresence'
import { CollaborativeEditor } from '@/components/documents/CollaborativeEditor'
import { RelationshipsGraph } from '@/components/documents/RelationshipsGraph'
import { WorkflowTab } from '@/components/workflows/WorkflowTab'
import { WorkflowStatusBadge } from '@/components/workflows/WorkflowStatusBadge'
import { AuditVisualization } from '@/components/documents/AuditVisualization'
import { SignaturesPanel } from '@/components/documents/SignaturesPanel'
import { ShareDialog } from '@/components/documents/ShareDialog'
import { CompareDialog } from '@/components/documents/CompareDialog'
import { VersionHistory } from '@/components/documents/VersionHistory'
import { RetentionExemptToggle } from '@/components/documents/RetentionExemptToggle'
import { ManageAccessDialog } from '@/components/documents/ManageAccessDialog'
import { Pencil } from 'lucide-react'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as ShadcnSelect } from '@/components/ui/shadcn/select'
import { useUpdateDocument } from '@/hooks/useDocuments'
import { RerunOcrButton } from '@/components/intelligence/RerunOcrButton'
import { OcrTextReview } from '@/components/intelligence/OcrTextReview'
import { DeclareRecordButton } from '@/components/records/DeclareRecordButton'
import { DocumentIntegrity } from '@/components/records/DocumentIntegrity'
import { getMetadataSchema } from '@/api/metadataSchema'
import type { Document } from '@/types/api'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/shadcn/sheet'
import { Badge } from '@/components/ui/shadcn/badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Spinner } from '@/components/ui/Spinner'
import { Skeleton } from '@/components/ui/Skeleton'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/EmptyState'
import { ErrorBoundary } from '@/components/shared/ErrorBoundary'
import { fieldLabel } from '@/lib/schemaFields'
import { formatFileSize, formatDateTime, formatRelativeTime, lifecycleStateLabel } from '@/lib/formatters'
import { cn } from '@/lib/cn'
import { TaskCreateDialog } from '@/components/tasks/TaskCreateDialog'
import { TagSuggestionsPanel } from '@/components/intelligence/TagSuggestionsPanel'
import { RouteSuggestionBanner } from '@/components/intelligence/RouteSuggestionBanner'
import { CompliancePanel } from '@/components/intelligence/CompliancePanel'
import { ComplianceBadge } from '@/components/intelligence/ComplianceBadge'
import { ProcessingFailureBanner } from '@/components/documents/ProcessingFailureBanner'
import { DocQAChat } from '@/components/intelligence/DocQAChat'
import { LanguageBadge } from '@/components/intelligence/LanguageBadge'
import { TranslationPanel } from '@/components/intelligence/TranslationPanel'
import { OcrQualityBadge } from '@/components/intelligence/OcrQualityBadge'
import { OcrQualityPanel } from '@/components/intelligence/OcrQualityPanel'
import { CorrectClassificationButton } from '@/components/intelligence/CorrectClassificationButton'
import { EntitiesPanel } from '@/components/intelligence/EntitiesPanel'
import { HighlightedText } from '@/components/intelligence/HighlightedText'
import { RedactionReviewPanel } from '@/components/intelligence/RedactionReviewPanel'
import { MatchedClausesPanel } from '@/components/documents/MatchedClausesPanel'
import { DocumentTasksPanel } from '@/components/tasks/DocumentTasksPanel'

type TabKey = 'preview' | 'text' | 'qa' | 'compliance' | 'entities' | 'relationships' | 'workflow' | 'comments' | 'signatures' | 'redaction' | 'activity'

// Each view belongs to one of five groups; the tab bar renders those groups as
// top-level nav and reveals a group's views as sub-tabs. `group` here is the
// single source of truth — TAB_GROUPS below derives its members by filtering.
const TABS: { key: TabKey; label: string; icon: typeof FileText; group: TabGroupKey }[] = [
  { key: 'preview', label: 'Preview', icon: FileText, group: 'document' },
  { key: 'text', label: 'Text', icon: FileText, group: 'document' },
  { key: 'redaction', label: 'Redaction', icon: Eraser, group: 'document' },
  { key: 'qa', label: 'Q&A', icon: MessageSquare, group: 'analysis' },
  { key: 'entities', label: 'Entities', icon: FileText, group: 'analysis' },
  // ADR 0099 — contract intelligence graph.
  { key: 'relationships', label: 'Relationships', icon: Network, group: 'analysis' },
  { key: 'compliance', label: 'Compliance', icon: AlertCircle, group: 'compliance' },
  // Document-associated workflow (templates attached to this doc).
  { key: 'workflow', label: 'Workflow', icon: GitBranch, group: 'collaboration' },
  { key: 'comments', label: 'Comments', icon: MessageSquare, group: 'collaboration' },
  { key: 'signatures', label: 'Signatures', icon: Pencil, group: 'collaboration' },
  { key: 'activity', label: 'Activity', icon: History, group: 'history' },
]

type TabGroupKey = 'document' | 'analysis' | 'compliance' | 'collaboration' | 'history'

// Top-level groups, in display order. Single-view groups (Compliance, History)
// open their view directly with no sub-tab row.
const TAB_GROUPS: { key: TabGroupKey; label: string; icon: typeof FileText; tabs: typeof TABS }[] = [
  { key: 'document', label: 'Document', icon: FileText, tabs: TABS.filter((t) => t.group === 'document') },
  { key: 'analysis', label: 'Analysis', icon: Network, tabs: TABS.filter((t) => t.group === 'analysis') },
  { key: 'compliance', label: 'Compliance', icon: AlertCircle, tabs: TABS.filter((t) => t.group === 'compliance') },
  { key: 'collaboration', label: 'Collaboration', icon: MessageSquare, tabs: TABS.filter((t) => t.group === 'collaboration') },
  { key: 'history', label: 'History', icon: History, tabs: TABS.filter((t) => t.group === 'history') },
]

function DocumentDetailPage() {
  const { documentId, workspaceId } = Route.useParams()
  return <DocumentDetailBody documentId={documentId} workspaceId={workspaceId} />
}

// Renders the full document-detail surface (banners + tabs + sidebar)
// without owning route params. Reused by:
//   - DocumentDetailPage (route component) — passes params from URL
//   - DocumentViewerModal — passes the doc id from a search param so
//     the workspace grid can open the same surface as an overlay
// Nothing about the body's logic differs between modal and full-page
// modes; the modal supplies a fixed-height container so the inner
// `overflow-y-auto`s land correctly.
export function DocumentDetailBody({
  documentId,
  workspaceId,
  inModal = false,
}: {
  documentId: string
  workspaceId: string
  // When rendered inside DocumentViewerModal the modal supplies its own
  // header bar (icon + title + lifecycle badge + close). Set true to
  // suppress the body's `<DocumentHeader />` so the title doesn't render
  // twice in the same viewport. Default false keeps the full-page route
  // unchanged.
  inModal?: boolean
}) {
  const { data: doc, isLoading } = useDocument(documentId)
  // Prime the workspace cache so the breadcrumb resolves the workspace
  // UUID to its name. The breadcrumb component reads
  // `['workspace', uuid]` from the query cache; without this query the
  // doc-detail route shows a truncated UUID for the workspace crumb.
  // 5-minute staleTime — workspace names change rarely.
  useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => getWorkspace(workspaceId),
    enabled: !!workspaceId,
    staleTime: 5 * 60_000,
  })
  // ADR 0074 — single GraphQL call for the multi-join surface
  // (versions + comments + annotations + workflow instances +
  // permissions). Replaces the 5+ REST calls the deep components
  // would otherwise fan out, and feeds the Activity tab. The hook
  // is fire-and-forget at the page level: deep components keep
  // their REST queries (and stay live for mutations / polling); the
  // GraphQL fetch primes the cache so the network tab shows the
  // collapsed shape on first paint.
  const gql = useDocumentDetailGQL(documentId)
  const [tab, setTab] = useState<TabKey>('preview')
  const userRole = useAuthStore((s) => s.user?.role)
  useEffect(() => {
    if (gql.isError) {
      const msg = gql.error instanceof Error ? gql.error.message : String(gql.error)
      console.warn(`[graphql-aggregate] ${documentId}: ${msg} — deep tabs falling back to REST`)
    }
  }, [gql.isError, gql.error, documentId])
  // ADR 0079 — bulk-apply redaction gate. Compliance officer also
  // counts because they're the typical reviewers.
  const isAdminCaller =
    userRole === 'owner' || userRole === 'admin' || userRole === 'compliance_officer'

  if (isLoading) return <DocumentSkeleton />
  if (!doc) {
    return (
      <EmptyState
        icon={<FileText className="h-8 w-8" />}
        title="Document not found"
        description="It may have been deleted, moved to another workspace, or you might not have permission to view it."
        actionLabel="Back to workspace"
        onAction={() => { window.history.back() }}
      />
    )
  }

  const versionId = (doc as unknown as { current_version_id?: string }).current_version_id

  return (
    <div
      className={cn(
        'flex flex-col gap-6',
        // Full-page: lock to the viewport so the PAGE never scrolls — the
        // preview pane and the rail scroll internally instead. lg+ only; on
        // small screens the single column flows and the page scrolls as
        // normal. Mirrors the workspace browser's shell pattern. In the modal
        // the dialog body owns scrolling, so no lock is applied there.
        !inModal && 'lg:min-h-0 lg:flex-1 lg:overflow-hidden',
      )}
    >
      {!inModal && <DocumentHeader doc={doc} workspaceId={workspaceId} documentId={documentId} versionId={versionId} onNavigate={setTab} />}
      {/* Modal mode: replace the rich header with a compact metadata strip so
          uploader + intelligence badges + primary actions aren't lost. */}
      {inModal && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-muted-foreground">
          <span title={uploaderTooltip(doc)}>{uploaderLabel(doc)}</span>
          <span aria-hidden>·</span>
          <span>uploaded {formatDateTime(doc.created_at)}</span>
          <div className="ms-auto flex flex-wrap items-center gap-2">
            <LanguageBadge documentId={documentId} />
            <BadgeLink onNavigate={setTab} tab="compliance" title="View compliance findings">
              <ComplianceBadge documentId={documentId} />
            </BadgeLink>
            <BadgeLink onNavigate={setTab} tab="text" title="View extracted text (OCR)">
              <OcrQualityBadge documentId={documentId} />
            </BadgeLink>
            <RealtimePresence documentId={documentId} />
            <DocumentActions doc={doc} documentId={documentId} versionId={versionId} />
          </div>
        </div>
      )}

      {/* ADR 0074 — GraphQL aggregate fires per page and feeds the
          deeper tabs; on failure REST takes over silently. The
          previously-visible admin diagnostics banner cluttered every
          document page since errors are common while graphql-gateway
          upstream wiring is partial. Error stays in the devtools
          console for QA; the loading pill is kept as a transient cue. */}
      {isAdminCaller && gql.isLoading && (
        <p className="text-xs text-muted-foreground" data-testid="gql-status-loading">
          Loading aggregated detail via GraphQL…
        </p>
      )}

      {/* ADR 0115 — processing-failure banner. Shows only when a file
          was uploaded but the intelligence pipeline failed/partially
          failed, with a per-stage reason and (for admins) a Retry. */}
      {versionId && (
        <ProcessingFailureBanner documentId={documentId} canRetry={isAdminCaller} />
      )}

      {/* No-content prompt: when the document row exists but no file
          has been uploaded yet, surface an upload CTA at the top so
          the user can act immediately rather than discovering the
          gap from the empty preview tab. */}
      {!versionId && (
        <Card
          className="flex flex-wrap items-center justify-between gap-3 border-warning/40 bg-warning/5 p-4"
          data-testid="no-content-banner"
        >
          <div className="flex items-start gap-2">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
            <div className="text-sm">
              <p className="font-medium text-foreground">No file uploaded</p>
              <p className="text-muted-foreground">
                This document record has no content yet. Upload a file to enable preview, OCR, and search.
              </p>
            </div>
          </div>
          <Button asChild>
            <Link
              to="/workspaces/$workspaceId"
              params={{ workspaceId }}
              data-testid="no-content-upload-cta"
            >
              <FileText className="me-1 h-4 w-4" /> Go to workspace to upload
            </Link>
          </Button>
        </Card>
      )}

      {/* OCR failure banner: surface failed status from the doc
          header so users don't have to open the Raw text tab to
          discover the failure. The Re-run button is the same
          mutation the OCRPanel exposes deeper in the page. */}
      {versionId && (
        <OCRFailureBanner documentId={documentId} versionId={versionId} />
      )}

      {/* Surfaces legal-hold lock state so users understand why delete /
          move are disabled in the actions menu. */}
      <LegalHoldBanner doc={doc} />

      {/* Records + integrity moved into the sidebar (compact-toolbar
          redesign): they were two full-width header bands that pushed
          the actual document content below the fold on every open. */}

      {/* ADR 0053 — banner appears only when smart_route produced
          pending suggestions for this doc. Self-hides otherwise. */}
      <RouteSuggestionBanner documentId={documentId} />

      <div
        className={cn(
          'grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px] xl:grid-cols-[minmax(0,1fr)_360px]',
          // Full-page lock: the grid fills the remaining height and its single
          // row is bounded (minmax(0,1fr)) so the two columns can scroll
          // internally instead of growing the page. Skipped in the modal.
          !inModal && 'lg:min-h-0 lg:flex-1 lg:[grid-template-rows:minmax(0,1fr)]',
        )}
      >
        {/* key={tab} remounts the pane on tab change so each view opens
            scrolled to the top rather than inheriting the previous scroll. */}
        <div
          key={tab}
          className={cn(
            'flex min-w-0 min-h-[calc(100vh-12rem)] flex-col space-y-4',
            !inModal && 'lg:min-h-0 lg:overflow-y-auto lg:pe-1',
          )}
        >
          <DocumentTabs tab={tab} onChange={setTab} />

          <TabPanel current={tab} value="preview">
            <DocumentPreview
              documentId={documentId}
              versionId={versionId}
              mimeType={doc.mime_type}
              title={doc.title}
            />
          </TabPanel>

          <TabPanel current={tab} value="text">
            <OCRPanel
              documentId={documentId}
              versionId={versionId}
              uploadedAt={doc.created_at}
              mimeType={doc.mime_type}
            />
          </TabPanel>

          <TabPanel current={tab} value="qa">
            <DocQAChat documentId={documentId} />
          </TabPanel>

          <TabPanel current={tab} value="compliance">
            <CompliancePanel documentId={documentId} />
          </TabPanel>

          <TabPanel current={tab} value="entities">
            <EntitiesPanel documentId={documentId} versionId={versionId} />
          </TabPanel>

          {/* ADR 0099 — contract intelligence graph. Self-empties for
              non-contract docs (no edges → empty-state message). */}
          <TabPanel current={tab} value="relationships">
            <RelationshipsGraph documentId={documentId} workspaceId={workspaceId} />
          </TabPanel>

          {/* Document-associated workflow — templates attached to this
              specific doc. Renders empty-state + Start picker when no
              active instance exists, or the timeline + actions when
              one is running. canCancel: tenant admin/owner or the
              user who initiated the workflow. */}
          <TabPanel current={tab} value="workflow">
            <WorkflowTab documentId={documentId} canCancel={isAdminCaller} />
          </TabPanel>

          {/* Comments + Signatures moved out of the sidebar into their own
              tabs (2026-08-01 sidebar de-weighting) — they're full feature
              surfaces, not at-a-glance metadata. */}
          <TabPanel current={tab} value="comments">
            <CommentsPanel documentId={documentId} />
          </TabPanel>

          <TabPanel current={tab} value="signatures">
            <SignaturesPanel documentId={documentId} />
          </TabPanel>

          <TabPanel current={tab} value="redaction">
            <RedactionReviewPanel
              documentId={documentId}
              versionId={versionId}
              isAdminCaller={isAdminCaller}
            />
          </TabPanel>

          <TabPanel current={tab} value="activity">
            {/* ADR 0074 — chronological feed (existing). ADR 0103 —
                Insights toggle adds the Sankey + heatmap + bars views
                on top of the same audit_events data. */}
            <ActivityTabContent documentId={documentId} />
          </TabPanel>

          {/* OCR quality lives below the merged Text/Layout content
              because it quotes per-page scores users compare against
              the actual text. Self-hides when the scorer hasn't run. */}
          {tab === 'text' && (
            <OcrQualityPanel documentId={documentId} />
          )}

          {/* Recognition heat-map + per-page manual correction + engine
              re-run (auto/printed/handwriting). Admin-gated: it edits the
              recognised text and triggers expensive re-OCR. */}
          {tab === 'text' && isAdminCaller && versionId && (
            <div className="mt-4 rounded-lg border border-border p-3">
              <h3 className="mb-2 text-sm font-semibold">Recognition heat-map &amp; correction</h3>
              <OcrTextReview
                documentId={documentId}
                versionId={versionId}
                canCorrect={isAdminCaller}
                canRerun={isAdminCaller}
              />
            </div>
          )}
        </div>

        <DocumentSidebar
          doc={doc}
          documentId={documentId}
          versionId={versionId}
          isAdminCaller={isAdminCaller}
          inModal={inModal}
        />
      </div>
    </div>
  )
}

// ---- Header --------------------------------------------------------------

function DocumentHeader({
  doc,
  workspaceId,
  documentId,
  versionId,
  onNavigate,
}: {
  doc: {
    title: string
    mime_type: string
    created_by_name?: string
    created_at: string
    workflow_instance?: Document['workflow_instance']
  }
  workspaceId: string
  documentId: string
  versionId?: string
  onNavigate?: (tab: TabKey) => void
}) {
  return (
    <div className="space-y-3">
      <Link
        to="/workspaces/$workspaceId"
        params={{ workspaceId }}
        className="group inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-primary hover:underline"
      >
        <DirectionalIcon name="ChevronLeft" className="h-3.5 w-3.5 transition-transform group-hover:-translate-x-0.5" />
        Back to workspace
      </Link>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <span className="flex h-12 w-12 shrink-0 items-center justify-center rounded-lg border border-border bg-muted">
          <FileIcon mime={doc.mime_type} className="h-6 w-6" />
        </span>
        <div className="min-w-0 flex-1">
          <h1
            className="break-words text-2xl font-semibold tracking-tight line-clamp-2"
            title={doc.title}
          >
            {doc.title}
          </h1>
          <p className="mt-1 text-xs text-muted-foreground">
            {/* Fallback chain: display name → email → "Deleted user".
                "Unknown" was misleading — the user record either exists
                (use it) or has been removed (say so explicitly). */}
            <span
              title={uploaderTooltip(doc)}
              className={uploaderLabel(doc) === 'Deleted user' ? 'underline decoration-dotted underline-offset-2' : undefined}
            >
              {uploaderLabel(doc)}
            </span>
            {' · uploaded '}{formatDateTime(doc.created_at)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {doc.workflow_instance && (
            <WorkflowStatusBadge status={doc.workflow_instance.status} />
          )}
          <LanguageBadge documentId={documentId} />
          <BadgeLink onNavigate={onNavigate} tab="compliance" title="View compliance findings">
            <ComplianceBadge documentId={documentId} />
          </BadgeLink>
          <BadgeLink onNavigate={onNavigate} tab="text" title="View extracted text (OCR)">
            <OcrQualityBadge documentId={documentId} />
          </BadgeLink>
          <RealtimePresence documentId={documentId} />
          <DocumentActions doc={doc} documentId={documentId} versionId={versionId} />
        </div>
      </div>
    </div>
  )
}

// Wraps a status badge so it deep-links to the tab that explains it (e.g.
// "Critical" → Compliance, "OCR: Good" → Text). No-op passthrough when no
// navigation handler is supplied. The wrapped badges already self-hide when
// they have no data, so this renders nothing in that case.
function BadgeLink({
  onNavigate,
  tab,
  title,
  children,
}: {
  onNavigate?: (tab: TabKey) => void
  tab: TabKey
  title: string
  children: React.ReactNode
}) {
  if (!onNavigate) return <>{children}</>
  return (
    <button
      type="button"
      onClick={() => onNavigate(tab)}
      title={title}
      data-testid={`badge-link-${tab}`}
      className="rounded-full transition-opacity hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
    >
      {children}
    </button>
  )
}

// Self-contained primary-action cluster for the header: a labelled Download
// button (the one action people reach for) plus a ⋯ menu for the rest, and it
// owns the dialogs those items open. Lives beside the filename so actions sit
// with the document they act on — the sidebar no longer carries an icon card.
function DocumentActions({ doc, documentId, versionId }: { doc: any; documentId: string; versionId?: string }) {
  const [taskOpen, setTaskOpen] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [compareOpen, setCompareOpen] = useState(false)
  const [manageAccessOpen, setManageAccessOpen] = useState(false)

  // Cookie-authenticated decrypt-stream alias: one URL for every mime type;
  // the `download` attribute hints the original filename.
  const handleDownload = () => {
    if (!versionId) return
    const a = document.createElement('a')
    a.href = `/api/v1/documents/${documentId}/versions/${versionId}/download`
    a.download = doc.title ?? 'document'
    document.body.appendChild(a)
    a.click()
    a.remove()
  }

  return (
    <div className="flex items-center gap-1">
      <Button
        size="sm"
        variant="outline"
        onClick={handleDownload}
        disabled={!versionId}
        data-testid="action-download"
      >
        <Download className="h-4 w-4" /> Download
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="ghost" className="h-8 w-8 p-0" aria-label="More actions" data-testid="action-more">
            <MoreHorizontal className="h-4 w-4" aria-hidden />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-52">
          <DropdownMenuItem onClick={() => setShareOpen(true)}>
            <Share2 className="me-2 h-4 w-4" aria-hidden /> Share
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setCompareOpen(true)}>
            <GitCompareArrows className="me-2 h-4 w-4" aria-hidden /> Compare with…
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setManageAccessOpen(true)}>
            <UserCog className="me-2 h-4 w-4" aria-hidden /> Manage access
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setTaskOpen(true)}>
            <CheckSquare className="me-2 h-4 w-4" aria-hidden /> Create task
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <TaskCreateDialog
        open={taskOpen}
        onOpenChange={setTaskOpen}
        defaultDocument={{ document_id: documentId, title: doc?.title ?? '' }}
      />
      <ShareDialog
        open={shareOpen}
        onOpenChange={setShareOpen}
        documentId={documentId}
        documentTitle={doc.title}
      />
      <CompareDialog
        open={compareOpen}
        onOpenChange={setCompareOpen}
        baseDocumentId={documentId}
        baseDocumentTitle={doc.title ?? 'document'}
      />
      <ManageAccessDialog
        open={manageAccessOpen}
        onOpenChange={setManageAccessOpen}
        resourceType="document"
        resourceId={documentId}
        resourceTitle={doc.title ?? 'this document'}
        workspaceId={doc.workspace_id}
        folderId={doc.folder_id}
      />
    </div>
  )
}

// ---- Tabs ---------------------------------------------------------------

function DocumentTabs({ tab, onChange }: { tab: TabKey; onChange: (k: TabKey) => void }) {
  // Two-level navigation: five top-level groups, each revealing its own views
  // as sub-tabs. Collapses the 11 flat tabs into an organized hierarchy so no
  // view hides silently off the edge and the way back to the document (the
  // Document group) is always visible. `tab` still drives content; the active
  // group is derived from it.
  const activeGroup = TAB_GROUPS.find((g) => g.tabs.some((t) => t.key === tab)) ?? TAB_GROUPS[0]
  return (
    <div className="space-y-2">
      {/* Top-level groups */}
      <div
        role="tablist"
        aria-label="Document sections"
        className="inline-flex min-w-full gap-1 overflow-x-auto rounded-md bg-muted/60 p-1 [mask-image:linear-gradient(to_right,black_0,black_calc(100%-1.5rem),transparent)] [&::-webkit-scrollbar]:hidden"
      >
        {TAB_GROUPS.map(({ key, label, icon: Icon, tabs }) => {
          const active = key === activeGroup.key
          return (
            <button
              key={key}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => onChange(tabs[0].key)}
              data-testid={`tabgroup-${key}`}
              className={cn(
                'inline-flex items-center gap-1.5 whitespace-nowrap rounded-sm px-3 py-1.5 text-xs font-medium transition-all',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
                active
                  ? 'bg-background text-foreground shadow-sm'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {label}
            </button>
          )
        })}
      </div>

      {/* Sub-tabs for the active group (only when it has more than one view). */}
      {activeGroup.tabs.length > 1 && (
        <div
          role="tablist"
          aria-label={`${activeGroup.label} views`}
          className="flex flex-wrap gap-1 px-0.5"
        >
          {activeGroup.tabs.map(({ key, label, icon: Icon }) => {
            const active = tab === key
            return (
              <button
                key={key}
                type="button"
                role="tab"
                aria-selected={active}
                onClick={() => onChange(key)}
                data-testid={`tab-${key}`}
                className={cn(
                  'inline-flex items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-xs font-medium transition-colors',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
                  active
                    ? 'bg-primary/10 text-primary'
                    : 'text-muted-foreground hover:bg-muted hover:text-foreground',
                )}
              >
                <Icon className="h-3.5 w-3.5" />
                {label}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function TabPanel({
  current,
  value,
  children,
}: {
  current: TabKey
  value: TabKey
  children: React.ReactNode
}) {
  if (current !== value) return null
  // role=tabpanel + aria-controls pairing for screen readers; the
  // rendered content drives its own surface so the wrapper stays
  // unstyled. Each panel gets its OWN error boundary: a failure in one
  // tab must not blank the shell (or the other tabs).
  return (
    <div
      role="tabpanel"
      id={`panel-${value}`}
      aria-labelledby={`tab-${value}`}
      tabIndex={0}
      className="space-y-3 focus-visible:outline-none"
    >
      <ErrorBoundary variant="panel" label={TABS.find((t) => t.key === value)?.label ?? value}>
        {children}
      </ErrorBoundary>
    </div>
  )
}

// ---- Sidebar -------------------------------------------------------------

// Collapsible rail section with its own header and a per-section open/closed
// state persisted to localStorage (keyed globally, so the user's layout
// choices stick across documents and reloads). Used for the sections whose
// body we author inline here; self-contained panels (which already carry
// their own card + header and self-hide when empty) render directly below.
function RailSection({
  id,
  title,
  defaultOpen = false,
  children,
}: {
  id: string
  title: string
  defaultOpen?: boolean
  children: React.ReactNode
}) {
  const storageKey = `doc-rail:${id}`
  const [open, setOpen] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem(storageKey)
      return v == null ? defaultOpen : v === '1'
    } catch {
      return defaultOpen
    }
  })
  const toggle = () => {
    setOpen((prev) => {
      const next = !prev
      try {
        localStorage.setItem(storageKey, next ? '1' : '0')
      } catch {
        /* storage unavailable (private mode) — state stays in-memory */
      }
      return next
    })
  }
  const headingId = `rail-heading-${id}`
  return (
    <Card className="overflow-hidden" role="region" aria-labelledby={headingId}>
      {/* Real heading element: screen-reader users navigate this rail by
          heading, and a styled <div> is invisible to that. Sentence case
          at 13px — the old 12px uppercase + wide tracking made field
          names like `amount_usd` read as an error string. */}
      <h2 id={headingId} className="m-0">
        <button
          type="button"
          onClick={toggle}
          aria-expanded={open}
          aria-controls={`rail-body-${id}`}
          data-testid={`rail-section-${id}`}
          className="flex w-full items-center justify-between gap-2 px-4 py-3 text-left text-[13px] font-semibold text-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        >
          <span>{title}</span>
          <ChevronDown className={cn('h-4 w-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
        </button>
      </h2>
      {open && (
        <div id={`rail-body-${id}`} className="border-t border-border px-4 pb-4 pt-3">
          {/* Per-widget isolation: one failing rail widget must not take
              down the shell (the taskOpen crash did exactly that). */}
          <ErrorBoundary variant="panel" label={title}>
            {children}
          </ErrorBoundary>
        </div>
      )}
    </Card>
  )
}

function DocumentSidebar({
  doc,
  documentId,
  versionId,
  isAdminCaller,
  inModal = false,
}: {
  doc: any
  documentId: string
  versionId?: string
  isAdminCaller?: boolean
  // Modal mode: the dialog body is the single scroll container, so the
  // sidebar must NOT bring its own sticky/max-height scroll region —
  // nested scrollbars inside the dialog read as broken layout.
  inModal?: boolean
}) {
  // `?doctype=note|wiki` lets a freshly-created note open the collaborative
  // editor before its first markdown version exists (the gateway GET doesn't
  // yet return doc_type). Loose read so it's harmless in the modal context.
  const { doctype: composeDocType } = useSearch({ strict: false }) as { doctype?: 'note' | 'wiki' }
  // Version history is the one dialog still opened from the rail (the Details
  // section); the rest of the actions moved to the header (DocumentActions).
  const [versionsOpen, setVersionsOpen] = useState(false)

  return (
    <aside
      className={cn(
        'space-y-4',
        // Full-page: the rail is a bounded grid cell that scrolls internally
        // (the page shell no longer scrolls). Was sticky+max-height before the
        // viewport-lock. Modal keeps natural flow.
        !inModal && 'lg:min-h-0 lg:overflow-y-auto lg:pe-1',
      )}
    >

      {/* Rail sections — individually collapsible, each remembering its own
          open/closed state; ordered by how often they're used. Sections we
          author inline use RailSection; self-contained intelligence panels
          (their own card + header, self-hiding when empty) render directly. */}

      {/* Details — lifecycle, type, size, version count, mime. */}
      <RailSection id="details" title="Details" defaultOpen>
        <dl className="space-y-2 text-sm">
          <Row label="Status">
            <Badge variant={doc.lifecycle_state}>{lifecycleStateLabel(doc.lifecycle_state)}</Badge>
          </Row>
          <Row label="Type">
            <span className="flex items-center justify-between gap-2">
              <span>{doc.document_class || 'Unclassified'}</span>
              <CorrectClassificationButton
                documentId={documentId}
                currentCategory={doc.document_class || ''}
              />
            </span>
          </Row>
          <Row label="Size">{formatFileSize(doc.total_size_bytes)}</Row>
          <Row label="Versions">
            {/* The count is the history affordance — dedupes the separate
                "Version history" button that duplicated this row. Comments is a
                tab (Collaboration), so its dead rail link is gone. */}
            <button
              type="button"
              onClick={() => setVersionsOpen(true)}
              data-testid="open-version-history"
              className="inline-flex items-center gap-1 font-medium text-primary hover:underline"
            >
              {doc.version_count ?? '—'}
              <History className="h-3.5 w-3.5" aria-hidden />
            </button>
          </Row>
          <Row label="MIME"><code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">{doc.mime_type}</code></Row>
        </dl>
      </RailSection>

      {/* Tags — one home: applied tags + AI suggestions (self-hides its list). */}
      <RailSection id="tags" title="Tags" defaultOpen>
        {(doc.tags?.length ?? 0) > 0 ? (
          <div className="flex flex-wrap gap-1">
            {(doc.tags ?? []).map((t: string) => <Badge key={t}>{t}</Badge>)}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">No tags applied.</p>
        )}
        <div className="mt-3">
          <TagSuggestionsPanel documentId={documentId} />
        </div>
      </RailSection>

      {/* Custom fields — self-hides when the tenant has no metadata schema. */}
      <CustomFieldsSidebarSlot doc={doc as Document} />

      {/* Tasks linked to this document. */}
      <DocumentTasksPanel documentId={documentId} documentTitle={doc?.title ?? ''} />

      {/* Records & integrity — declare-as-record + Merkle proof / WORM lock. */}
      <RailSection id="records" title="Records & integrity">
        <DeclareRecordButton documentId={documentId} canManage={!!isAdminCaller} />
        <div className="mt-3">
          <DocumentIntegrity documentId={documentId} canManage={!!isAdminCaller} />
        </div>
      </RailSection>

      {/* Matched clauses — self-hides when none. */}
      <MatchedClausesPanel documentId={documentId} />

      {/* Language & translation. */}
      {versionId && <TranslationPanel documentId={documentId} versionId={versionId} />}

      {/* Retention exemption — self-hides when not applicable. */}
      <RetentionExemptSidebarSlot doc={doc} />

      {/* Contextual editors — render only for the relevant mime types. */}
      {/* ADR 0065 — co-authoring entrypoint (Office types). */}
      {versionId && (
        <CoauthorEditor
          documentId={documentId}
          versionId={versionId}
          mimeType={doc.mime_type ?? ''}
          fileName={doc.title ?? 'document'}
          downloadUrl={`/api/v1/documents/${documentId}/versions/${versionId}/download`}
          canEdit={true}
        />
      )}
      {/* ADR 0067 — image + video annotation layers. */}
      {versionId && doc.mime_type?.startsWith('image/') && (
        <ImageAnnotationLayer
          documentId={documentId}
          versionId={versionId}
          imageUrl={`/api/v1/documents/${documentId}/versions/${versionId}/download`}
          canCreate={true}
        />
      )}
      {versionId && doc.mime_type?.startsWith('video/') && (
        <VideoAnnotationLayer
          documentId={documentId}
          versionId={versionId}
          videoUrl={`/api/v1/documents/${documentId}/versions/${versionId}/download`}
          canCreate={true}
        />
      )}
      {/* §17.4 / note — live collaborative editing for text/note/wiki docs. */}
      {(doc.mime_type?.startsWith('text/') ||
        doc.doc_type === 'note' || doc.doc_type === 'wiki' ||
        composeDocType === 'note' || composeDocType === 'wiki') && (
        <Card className="p-4">
          <h3 className="mb-3 text-[13px] font-semibold text-foreground">
            {doc.doc_type === 'wiki' || composeDocType === 'wiki' ? 'Wiki' : 'Note'}
          </h3>
          <CollaborativeEditor documentId={documentId} />
        </Card>
      )}

      {/* Version history dialog — opened from the Details section. Other
          dialogs (share/compare/task/manage-access) live in the header's
          DocumentActions; live presence moved to the header too. */}
      <Sheet open={versionsOpen} onOpenChange={setVersionsOpen}>
        <SheetContent side="right" className="w-[440px] sm:max-w-md">
          <SheetHeader>
            <SheetTitle>Version history</SheetTitle>
          </SheetHeader>
          <div className="mt-4 max-h-[calc(100vh-120px)] overflow-y-auto">
            <VersionHistory documentId={documentId} />
          </div>
        </SheetContent>
      </Sheet>
    </aside>
  )
}

// Cross-references the tenant's custom-metadata JSON Schema (admin
// surface at /admin/metadata-schema) against this document's
// custom_metadata payload. Renders one row per declared property,
// shows '—' for unset values, and marks required fields with *.
// Whole card self-hides when the schema has zero properties.
function CustomFieldsSidebarSlot({ doc }: { doc: Document }) {
  const { data: schema } = useQuery({
    queryKey: ['admin', 'metadata-schema'],
    queryFn: getMetadataSchema,
    // Schema is tenant-wide and rarely changes during a session;
    // 5-min staleness keeps the sidebar snappy without going stale
    // immediately after the admin saves the schema in another tab.
    staleTime: 5 * 60_000,
  })
  const [showEmpty, setShowEmpty] = useState(false)
  const [editOpen, setEditOpen] = useState(false)

  const props = (schema as { properties?: Record<string, { description?: string; type?: string }> } | undefined)?.properties ?? {}
  const required = new Set(
    ((schema as { required?: string[] } | undefined)?.required ?? []),
  )
  const entries = Object.entries(props)
  if (entries.length === 0) return null

  const values = doc.custom_metadata ?? {}
  const isFilled = (key: string) => {
    const raw = values[key]
    return raw != null && raw !== '' && !(Array.isArray(raw) && raw.length === 0)
  }

  // Default: show required + filled. Empty optional fields collapse
  // behind a "Show empty (N)" toggle so tenants with 20+ schema
  // fields don't render a giant noisy rail of em-dashes for every
  // document. Required-but-empty fields stay visible because they
  // need attention (the * indicator carries weight).
  const alwaysShown = entries.filter(([key]) => required.has(key) || isFilled(key))
  const collapsed = entries.filter(([key]) => !required.has(key) && !isFilled(key))
  const visible = showEmpty ? entries : alwaysShown

  return (
    <Card className="p-4" data-testid="custom-fields-sidebar">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-[13px] font-semibold text-foreground">Custom fields</h3>
        <button
          type="button"
          onClick={() => setEditOpen(true)}
          aria-label="Edit custom fields"
          title="Edit custom fields"
          className="inline-flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="edit-custom-fields"
        >
          <Pencil className="h-3.5 w-3.5" />
        </button>
      </div>
      <EditCustomFieldsSheet
        doc={doc}
        schema={schema as MetadataSchema}
        open={editOpen}
        onOpenChange={setEditOpen}
      />
      <dl className="mt-3 space-y-2 text-sm">
        {visible.map(([key, spec]) => {
          const raw = values[key]
          const display = raw == null || raw === ''
            ? <span className="text-muted-foreground">—</span>
            : typeof raw === 'object'
              ? <code className="break-all font-mono text-xs">{JSON.stringify(raw)}</code>
              : <span>{String(raw)}</span>
          return (
            <Row key={key} label={
              <>
                {/* JSON Schema `title` is the label; `description` is
                    helper text. Using description here leaked schema
                    placeholders ("e.g. INV-2026-0042") into the label
                    slot. Falls back to a tidied key, never the raw one. */}
                <span title={spec?.description || undefined}>{fieldLabel(key, spec)}</span>
                {required.has(key) && <span aria-label="required" className="ms-0.5 text-destructive">*</span>}
              </>
            }>
              {display}
            </Row>
          )
        })}
      </dl>
      {collapsed.length > 0 && (
        <button
          type="button"
          onClick={() => setShowEmpty((s) => !s)}
          className="mt-3 w-full rounded-md border border-dashed border-border px-2 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          data-testid="toggle-empty-custom-fields"
        >
          {showEmpty
            ? `Hide ${collapsed.length} empty optional field${collapsed.length === 1 ? '' : 's'}`
            : `Show ${collapsed.length} empty optional field${collapsed.length === 1 ? '' : 's'}`}
        </button>
      )}
    </Card>
  )
}

// ---- Edit custom fields sheet -------------------------------------------

interface MetadataPropertySpec {
  description?: string
  type?: 'string' | 'number' | 'integer' | 'boolean' | 'array' | 'object'
  format?: string
  enum?: string[]
  items?: { type?: string }
  minimum?: number
  maximum?: number
}

interface MetadataSchema {
  type?: 'object'
  properties?: Record<string, MetadataPropertySpec>
  required?: string[]
}

function EditCustomFieldsSheet({
  doc,
  schema,
  open,
  onOpenChange,
}: {
  doc: Document
  schema: MetadataSchema | undefined
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  // Seed once on open so the user can cancel without losing their
  // edits if they re-open. Re-seed when the upstream doc changes
  // (e.g. NDJSON import landed while the sheet was closed).
  const seed = useMemo<Record<string, unknown>>(
    () => ({ ...(doc.custom_metadata ?? {}) }),
    [doc.custom_metadata, open],
  )
  const [values, setValues] = useState<Record<string, unknown>>(seed)
  useEffect(() => { if (open) setValues(seed) }, [open, seed])

  const qc = useQueryClient()
  const update = useUpdateDocument()
  const props = schema?.properties ?? {}
  const required = useMemo(() => new Set(schema?.required ?? []), [schema])
  const entries = Object.entries(props)

  const isEmpty = (v: unknown) =>
    v == null || v === '' || (Array.isArray(v) && v.length === 0)

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (update.isPending) return
    const missing = Array.from(required).filter((k) => isEmpty(values[k]))
    if (missing.length) {
      const label = (k: string) => props[k]?.description || k
      toast.error(
        missing.length === 1
          ? `${label(missing[0])} is required`
          : `${missing.length} required fields missing`,
      )
      return
    }
    // Strip empty optional fields so the saved payload stays clean —
    // the backend treats absent and null identically.
    const clean: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(values)) {
      if (!isEmpty(v)) clean[k] = v
    }
    update.mutate(
      { id: doc.id, body: { custom_metadata: clean } },
      {
        onSuccess: () => {
          toast.success('Custom fields saved')
          qc.invalidateQueries({ queryKey: ['document', doc.id] })
          onOpenChange(false)
        },
        onError: (err: unknown) => {
          const msg = err instanceof Error ? err.message : "Couldn't save custom fields"
          toast.error(msg)
        },
      },
    )
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Edit custom fields</SheetTitle>
        </SheetHeader>
        {entries.length === 0 ? (
          <p className="mt-6 text-sm text-muted-foreground">
            No custom fields defined in the tenant metadata schema.
          </p>
        ) : (
          <form onSubmit={onSubmit} className="mt-4 flex min-h-0 flex-1 flex-col">
            <div className="flex-1 space-y-3 overflow-y-auto pe-1">
              {entries.map(([key, spec]) => (
                <FieldRenderer
                  key={key}
                  name={key}
                  spec={spec}
                  required={required.has(key)}
                  value={values[key]}
                  onChange={(v) => setValues((s) => ({ ...s, [key]: v }))}
                />
              ))}
            </div>
            <div className="sticky bottom-0 mt-3 flex justify-end gap-2 border-t border-border bg-background pt-3">
              <Button
                type="button"
                variant="ghost"
                onClick={() => onOpenChange(false)}
                disabled={update.isPending}
              >
                Cancel
              </Button>
              <Button type="submit" loading={update.isPending} data-testid="save-custom-fields">
                Save
              </Button>
            </div>
          </form>
        )}
      </SheetContent>
    </Sheet>
  )
}

function FieldRenderer({
  name,
  spec,
  required,
  value,
  onChange,
}: {
  name: string
  spec: MetadataPropertySpec
  required: boolean
  value: unknown
  onChange: (v: unknown) => void
}) {
  const label = (
    <span className="flex items-center gap-1 text-sm font-medium">
      {spec.description || name}
      {required && <span className="text-destructive" aria-label="required">*</span>}
    </span>
  )

  // Enum → labeled select. Add a leading "—" so the user can clear
  // an optional enum back to empty.
  if (Array.isArray(spec.enum)) {
    const NONE = '__none__'
    const opts = [
      ...(required ? [] : [{ value: NONE, label: '— None —' }]),
      ...spec.enum.map((v) => ({ value: v, label: v })),
    ]
    return (
      <ShadcnSelect
        label={spec.description || name}
        value={typeof value === 'string' ? value : NONE}
        onValueChange={(v) => onChange(v === NONE ? '' : v)}
        options={opts}
      />
    )
  }

  // Boolean → checkbox with inline label.
  if (spec.type === 'boolean') {
    return (
      <label className="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm">
        <input
          type="checkbox"
          className="h-4 w-4 rounded border-border"
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
        {label}
      </label>
    )
  }

  // Number / integer.
  if (spec.type === 'number' || spec.type === 'integer') {
    return (
      <Input
        label={spec.description || name}
        type="number"
        step={spec.type === 'integer' ? 1 : 'any'}
        min={spec.minimum}
        max={spec.maximum}
        value={value == null ? '' : String(value)}
        onChange={(e) => {
          const raw = e.target.value
          if (raw === '') onChange(undefined)
          else {
            const n = Number(raw)
            if (!Number.isNaN(n)) onChange(spec.type === 'integer' ? Math.trunc(n) : n)
          }
        }}
        required={required}
      />
    )
  }

  // Array of strings → comma-separated tags input.
  if (spec.type === 'array') {
    return (
      <Input
        label={(spec.description || name) + ' (comma-separated)'}
        value={Array.isArray(value) ? (value as unknown[]).map(String).join(', ') : ''}
        onChange={(e) =>
          onChange(
            e.target.value
              .split(',')
              .map((s) => s.trim())
              .filter(Boolean),
          )
        }
        placeholder="a, b, c"
      />
    )
  }

  // String with optional format.
  const inputType =
    spec.format === 'date' ? 'date' :
    spec.format === 'email' ? 'email' :
    spec.format === 'uri' || spec.format === 'url' ? 'url' :
    'text'

  return (
    <Input
      label={spec.description || name}
      type={inputType}
      value={typeof value === 'string' ? value : ''}
      onChange={(e) => onChange(e.target.value)}
      required={required}
    />
  )
}

function RetentionExemptSidebarSlot({ doc }: { doc: Document | unknown }) {
  // The sidebar receives doc as `any` to keep the existing prop
  // contract; narrow here so the toggle has the typed shape it needs.
  // useAuthStore is already loaded by the parent route so this is a
  // cheap selector call.
  const role = useAuthStore((s) => s.user?.role)
  // Same gate the redaction admin and OCR rerun controls use elsewhere
  // in this page: owner / admin / compliance_officer get the manage
  // surface.
  const canManage = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  if (!doc || typeof doc !== 'object') return null
  return (
    <RetentionExemptToggle
      doc={doc as Document}
      canManage={!!canManage}
    />
  )
}

function Row({ label, children }: { label: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      {/* Sentence case: 12px uppercase + wide tracking made values
          like `amount_usd` read as an error string, and slowed scanning. */}
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 truncate text-end text-sm">{children}</dd>
    </div>
  )
}

// ---- Loading skeleton ----------------------------------------------------

function DocumentSkeleton() {
  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <Skeleton className="h-3 w-32" />
        <div className="flex items-start gap-4">
          <Skeleton className="h-12 w-12 rounded-lg" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-7 w-2/3" />
            <Skeleton className="h-3 w-48" />
          </div>
        </div>
      </div>
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px] xl:grid-cols-[minmax(0,1fr)_360px]">
        <div className="space-y-3">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-72 w-full" />
        </div>
        <div className="space-y-3">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-48 w-full" />
        </div>
      </div>
    </div>
  )
}

// ---- OCR + Layout panels (kept from the original; only chrome touched) --

// OCRFailureBanner is a header-level banner that surfaces an OCR
// failure as soon as the doc detail loads, with a one-click Re-run
// button. Self-hides in every other status (pending/running/
// completed/unknown) so it doesn't clutter the page during the
// happy path. Subscribes to the same query the OCRPanel uses so
// React Query dedupes the fetch — only one network call, two
// readers.
function OCRFailureBanner({ documentId, versionId }: { documentId: string; versionId: string }) {
  const role = useAuthStore((s) => s.user?.role)
  const canRerun = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  const { data } = useQuery({
    queryKey: ['ocr', documentId, versionId],
    queryFn: () => getOCR(documentId, versionId),
  })
  if ((data?.status ?? 'unknown') !== 'failed') return null
  return (
    <Card
      className="flex flex-wrap items-center justify-between gap-3 border-destructive/40 bg-destructive/5 p-4"
      data-testid="ocr-failure-banner"
    >
      <div className="flex items-start gap-2">
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
        <div className="text-sm">
          <p className="font-medium text-foreground">OCR failed</p>
          <p className="text-muted-foreground">
            Text extraction did not complete. Re-run to try again — use Auto for the default pipeline or force Surya if Auto skipped layout analysis.
          </p>
        </div>
      </div>
      <RerunOcrButton
        documentId={documentId}
        versionId={versionId}
        canRerun={canRerun}
        variant="outline"
        size="default"
        testId="ocr-failure-rerun"
      />
    </Card>
  )
}

// LegalHoldBanner renders when the document's lifecycle_state is
// 'legal_hold'. It mirrors the backend enforcement in
// services/document/internal/model/lifecycle.go (IsLegalHoldBlocked).
function LegalHoldBanner({ doc }: { doc: Document }) {
  if (doc.lifecycle_state !== 'legal_hold') return null
  return (
    <Card
      className="flex items-start gap-3 border-amber-500/40 bg-amber-500/5 p-4"
      data-testid="legal-hold-banner"
    >
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
      <div className="text-sm">
        <p className="font-semibold text-foreground">Legal hold active</p>
        <p className="mt-0.5 text-muted-foreground">
          This document is under legal hold. Delete and move operations are blocked until the hold is released by a compliance officer.
          Editing the title, adding comments, and downloading remain available.
        </p>
      </div>
    </Card>
  )
}

// STUCK_OCR_MINUTES — threshold above which a "running" OCR status
// is considered stuck. Surya's slowest path is ~30s/page; even a
// 200-page scan finishes inside 30 min. Anything still "running"
// past that is either a worker drop or a poisoned event, and we
// surface a prominent recovery affordance instead of the soft
// "OCR is running…" message.
const STUCK_OCR_MINUTES = 30

function OCRPanel({ documentId, versionId, uploadedAt, mimeType }: { documentId: string; versionId?: string; uploadedAt?: string; mimeType: string }) {
  // `?page=N` from a citation deep-link → open the layout viewer on that
  // page. strict:false so this is harmless when rendered in the modal
  // (a different route with no `page` search).
  const { page: deepLinkPage } = useSearch({ strict: false }) as { page?: number }
  const role = useAuthStore((s) => s.user?.role)
  const canRerun = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  const isPdf = mimeType === 'application/pdf'
  const qc = useQueryClient()
  const [highlight, setHighlight] = useState(true)
  // ADR follow-up — Layout merged into Text. The same OCR response
  // backs both views; the toggle switches the renderer, not the
  // dataset. Disabled when boxes are empty (text-PDF fast path);
  // hidden entirely for non-PDF mimes where there's no page raster
  // to overlay onto.
  // Open in the layout (page-image) renderer when a citation deep-links
  // to a specific page, so the page anchor is meaningful; otherwise text.
  const [showLayout, setShowLayout] = useState(Boolean(deepLinkPage))
  const lastStatus = useRef<OCRStatus | null>(null)
  // Tracks the wall-clock time of the most-recent successful Re-run
  // click. The stuck banner uses uploadedAt to detect "OCR has been
  // running too long," but a fresh re-queue resets the clock without
  // updating uploadedAt — so we explicitly suppress the banner for
  // a grace window after the user re-queues.
  const [lastRerunAt, setLastRerunAt] = useState<number | null>(null)

  const { data, isLoading, refetch } = useQuery({
    queryKey: ['ocr', documentId, versionId],
    queryFn: () => getOCR(documentId, versionId!),
    enabled: Boolean(versionId),
    refetchInterval: (query) => {
      const status = (query.state.data?.status ?? 'unknown') as OCRStatus
      return status === 'running' || status === 'pending' ? 5000 : false
    },
  })

  const entitiesQuery = useQuery({
    queryKey: ['entities', documentId, 'for-raw-text'],
    queryFn: () => listEntities(documentId, { limit: 1000 }),
    enabled: Boolean(versionId) && (highlight || showLayout),
  })
  // Lazy: only fetch the signed PDF URL once the user opts into the
  // overlay. Keeps the default text-only view free of an extra round
  // trip the user may not need.
  const dl = useQuery({
    queryKey: ['download-url', documentId, versionId],
    queryFn: () => getDownloadURL(documentId, versionId!),
    enabled: Boolean(versionId) && isPdf && showLayout,
    staleTime: 60 * 1000,
    retry: 1,
  })
  // Engine choice ('surya') is an implementation detail kept inside
  // the mutation; the user-facing surface ('Run full layout analysis')
  // doesn't mention it. If the OCR pipeline swaps engines later this
  // call site changes one string; no copy update needed.
  const runFullLayout = useAppMutation({
    mutationFn: () => rerunOCR(documentId, versionId!, { forceEngine: 'surya' }),
    onSuccess: () => {
      toast.success('Running full layout analysis — boxes will appear when complete')
      qc.invalidateQueries({ queryKey: ['ocr', documentId, versionId] })
    },
    onError: () => toast.error('Layout analysis rerun failed'),
  })

  useEffect(() => {
    const cur = (data?.status ?? 'unknown') as OCRStatus
    const prev = lastStatus.current
    lastStatus.current = cur
    if (prev === null || prev === cur) return
    if (prev === 'unknown' && cur === 'pending') return
    switch (cur) {
      case 'running':
        toast.loading('OCR processing — first run downloads models (~5 min)', { id: `ocr-${versionId}`, duration: 10000 }); break
      case 'completed':
        toast.success(`OCR completed${data?.total_pages ? ` — ${data.total_pages} page${data.total_pages === 1 ? '' : 's'} extracted` : ''}`, { id: `ocr-${versionId}` }); break
      case 'failed':
        toast.error('OCR failed — check the runbook or re-run', { id: `ocr-${versionId}` }); break
      case 'pending':
        toast(`OCR queued — waiting for the worker`, { id: `ocr-${versionId}`, icon: '⏳' }); break
    }
  }, [data?.status, data?.total_pages, versionId])

  if (!versionId) {
    return (
      <Card className="p-8 text-center text-sm text-muted-foreground">
        No version available — upload a file to enable OCR.
      </Card>
    )
  }
  if (isLoading) {
    return <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
  }

  const status = data?.status ?? 'unknown'
  const pages = data?.pages ?? []
  const avgConf = data?.avg_confidence ?? 0
  const totalBoxes = pages.reduce((acc, p) => {
    const raw = p.bounding_boxes
    const arr = Array.isArray(raw)
      ? raw
      : Array.isArray((raw as { lines?: unknown[] })?.lines)
        ? (raw as { lines: unknown[] }).lines
        : []
    return acc + arr.length
  }, 0)
  const canShowLayout = isPdf && totalBoxes > 0
  // Auto-snap back to text when boxes disappear (engine swap, re-run
  // mid-view). Prevents the overlay rendering against an empty dataset.
  if (showLayout && !canShowLayout) {
    // No useEffect — direct setState on the descending edge is safe
    // because the conditional is below the bail-out and React batches.
    setShowLayout(false)
  }

  return (
    <div className="space-y-3">
      <Card className="flex flex-wrap items-center justify-between gap-3 p-3">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <StatusBadge status={status} />
          {pages.length > 0 && (
            <>
              <span className="text-xs text-muted-foreground">
                {pages.length} page{pages.length === 1 ? '' : 's'}
              </span>
              {avgConf > 0 && (
                <span
                  className="text-xs text-muted-foreground"
                  title="OCR engine character-recognition confidence. The OCR-quality table below shows a composite quality score (char + layout/skew/language), so the two figures differ by design."
                >
                  · avg {(avgConf * 100).toFixed(1)}% char confidence
                </span>
              )}
              {pages[0]?.engine && (
                <span className="text-xs text-muted-foreground">· {pages[0].engine}</span>
              )}
            </>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="ghost" size="sm" onClick={() => refetch()}>
            <RefreshCw className="h-3 w-3" />
          </Button>
          {isPdf && (
            <label
              className={`flex items-center gap-1.5 text-xs ${canShowLayout ? 'cursor-pointer text-muted-foreground' : 'cursor-not-allowed text-muted-foreground/50'}`}
              title={
                canShowLayout
                  ? 'Overlay layout bounding boxes on the page image'
                  : 'No layout boxes available — run full layout analysis to generate them'
              }
            >
              <input
                type="checkbox"
                checked={showLayout}
                disabled={!canShowLayout}
                onChange={(e) => setShowLayout(e.target.checked)}
                className="rounded border-input"
                data-testid="ocr-show-layout"
              />
              Show layout boxes
            </label>
          )}
          <label className="flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={highlight}
              onChange={(e) => setHighlight(e.target.checked)}
              className="rounded border-input"
            />
            Highlight entities
          </label>
          <RerunOcrButton
            documentId={documentId}
            versionId={versionId}
            canRerun={canRerun}
            testId="ocr-panel-rerun"
            onRerunSuccess={() => setLastRerunAt(Date.now())}
          />
        </div>
      </Card>

      {/* Layout-skipped notice (text-PDF fast path). Owns the
          'Run full layout analysis' action. Self-hides once boxes
          exist. Out of the controls row so the explanation reads
          LTR without crowding the toolbar. */}
      {isPdf && totalBoxes === 0 && pages.length > 0 && (
        <div
          className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-warning"
          data-testid="layout-skipped-notice"
        >
          <span>
            Layout analysis was skipped — this PDF already had a clean text layer, so we used the fast path instead. Click <strong>Run full layout analysis</strong> to detect headers, tables, and bounding boxes (~1&nbsp;min).
          </span>
          {canRerun && (
            <Button
              variant="default"
              size="sm"
              onClick={() => runFullLayout.mutate()}
              disabled={runFullLayout.isPending}
              title="Detect headers, tables, and bounding boxes for this PDF."
            >
              {runFullLayout.isPending ? <Spinner className="h-3 w-3" /> : <RefreshCw className="h-3 w-3" />}
              Run full layout analysis
            </Button>
          )}
        </div>
      )}

      {pages.length === 0 ? (
        (() => {
          // Stuck detection: running status + uploaded long enough ago
          // that the worker should have either finished or failed.
          // Falls back to the neutral copy if we don't know upload
          // time, which keeps the legacy behavior intact.
          const elapsedMin = uploadedAt
            ? Math.floor((Date.now() - new Date(uploadedAt).getTime()) / 60_000)
            : null
          // Grace window: if the user just clicked Re-run, suppress
          // the stuck banner for 60s so the worker has time to actually
          // process before we re-declare the doc stuck.
          const recentlyRerun = lastRerunAt != null && Date.now() - lastRerunAt < 60_000
          const isStuck =
            status === 'running' &&
            elapsedMin != null &&
            elapsedMin > STUCK_OCR_MINUTES &&
            !recentlyRerun
          if (isStuck) {
            return (
              <Card
                className="flex items-start gap-2 border-warning/40 bg-warning/5 p-4 text-sm"
                data-testid="ocr-stuck"
              >
                <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
                <div>
                  <p className="font-medium">OCR appears to be stuck</p>
                  <p className="text-muted-foreground">
                    OCR started {formatRelativeTime(uploadedAt!)} and hasn&apos;t reported back.
                    Use <strong className="text-foreground">Re-run OCR</strong> above to requeue.
                  </p>
                </div>
              </Card>
            )
          }
          // Show a transient "queued" state for the grace window so
          // the user gets visible confirmation that their click did
          // something, not just a status flip back to "running" with
          // the original 5-day-old timestamp.
          if (recentlyRerun && (status === 'running' || status === 'pending')) {
            return (
              <Card className="flex items-center gap-2 p-4 text-sm" data-testid="ocr-rerun-queued">
                <Spinner className="h-4 w-4" />
                <span>Re-queued. Worker is processing — text will appear here when complete.</span>
              </Card>
            )
          }
          return (
            <Card className="p-8 text-center text-sm text-muted-foreground">
              {status === 'pending' && 'OCR has not started yet.'}
              {status === 'running' && (
                <>
                  OCR is running — text will appear here when complete.
                  {elapsedMin != null && (
                    <div className="mt-1 text-xs text-muted-foreground">
                      Started {formatRelativeTime(uploadedAt!)}
                    </div>
                  )}
                </>
              )}
              {status === 'failed' && 'OCR failed. Check the runbook or re-run.'}
              {status === 'completed' && 'OCR completed but no text was extracted.'}
              {status === 'unknown' && 'No OCR results available.'}
            </Card>
          )
        })()
      ) : showLayout ? (
        // Layout overlay branch — same OCR data, different renderer.
        // Loading state guards against rendering before the lazy
        // download URL resolves; PDFLayoutViewer itself handles the
        // PDF-fetch error case.
        dl.isLoading ? (
          <div className="flex justify-center py-12" data-testid="ocr-layout-loading">
            <Spinner className="h-6 w-6" />
          </div>
        ) : !dl.data?.url ? (
          <Card className="p-8 text-center text-sm text-muted-foreground">
            Could not load document URL — switch back to text or retry.
          </Card>
        ) : (
          <Card className="p-3">
            <PDFLayoutViewer
              url={dl.data.url}
              pages={pages}
              initialPage={deepLinkPage}
              entities={highlight ? entitiesQuery.data?.entities : undefined}
            />
          </Card>
        )
      ) : (
        <div className="space-y-2">
          {pages.map((p, idx) => {
            const pageEntities = highlight
              ? entitiesForPage(entitiesQuery.data?.entities ?? [], pages, idx)
              : []
            return (
              <details
                key={p.id}
                className="rounded-lg border border-border bg-card open:bg-muted/40"
                open={pages.length <= 3}
              >
                <summary className="cursor-pointer px-3 py-2 text-sm font-medium">
                  Page {p.page_number}
                  <span className="ms-2 text-xs font-normal text-muted-foreground">
                    · {(p.confidence * 100).toFixed(1)}% char conf
                    {p.processing_time_ms ? ` · ${p.processing_time_ms}ms` : ''}
                    {p.language ? ` · ${p.language}` : ''}
                    {highlight && pageEntities.length > 0 && (
                      <span className="ms-2">· {pageEntities.length} entit{pageEntities.length === 1 ? 'y' : 'ies'}</span>
                    )}
                  </span>
                </summary>
                {!p.text_content ? (
                  <div className="border-t border-border p-3 text-xs italic text-muted-foreground">No text on this page</div>
                ) : highlight ? (
                  <div className="border-t border-border p-3 text-xs">
                    <HighlightedText text={p.text_content} entities={pageEntities} />
                  </div>
                ) : (
                  <pre className="whitespace-pre-wrap break-words border-t border-border p-3 text-xs">
                    {p.text_content}
                  </pre>
                )}
              </details>
            )
          })}
        </div>
      )}
    </div>
  )
}

function entitiesForPage(all: Entity[], pages: { text_content: string }[], pageIndex: number): Entity[] {
  let cursor = 0
  for (let i = 0; i < pageIndex; i++) {
    cursor += (pages[i].text_content?.length ?? 0) + 2
  }
  const pageStart = cursor
  const pageEnd = pageStart + (pages[pageIndex].text_content?.length ?? 0)
  return all
    .filter((e) => e.start_offset >= pageStart && e.end_offset <= pageEnd)
    .map((e) => ({ ...e, start_offset: e.start_offset - pageStart, end_offset: e.end_offset - pageStart }))
}

// uploaderLabel produces the human-readable name for the document
// uploader. The Document type carries created_by_name (display name)
// + created_by (uuid). When the user has been deleted the join in
// the API response leaves both empty; older API versions emitted
// "Unknown" which is unhelpful — be explicit.
function uploaderLabel(doc: { created_by_name?: string; created_by_email?: string; created_by?: string }): string {
  const name = doc.created_by_name?.trim()
  if (name) return name
  const email = doc.created_by_email?.trim()
  if (email) return email
  if (doc.created_by) return 'Deleted user'
  return 'Unknown user'
}

// uploaderTooltip explains the "Deleted user" label so it doesn't
// read as a bug. Other labels get a quieter tooltip with the raw
// user id for support purposes.
function uploaderTooltip(doc: { created_by_name?: string; created_by_email?: string; created_by?: string }): string {
  // End-user copy only — raw UUIDs are internal and don't belong in
  // a tooltip. Support tools can look the id up from the audit log
  // if they ever need it. Short single-line strings so native
  // tooltips render cleanly across platforms.
  if (uploaderLabel(doc) === 'Deleted user') {
    return 'Uploaded by a user whose account has been removed from this tenant.'
  }
  if (uploaderLabel(doc) === 'Unknown user') {
    return 'Uploader metadata missing — predates user tracking.'
  }
  return ''
}

// ActivityTabContent — ADR 0103 wrapper that lets the user switch
// between the chronological feed (existing ADR 0074 view) and the
// new "Insights" viz (Sankey + heatmap + bars).
function ActivityTabContent({ documentId }: { documentId: string }) {
  const [mode, setMode] = useState<'feed' | 'insights'>('feed')
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-1 border-b border-border pb-2 text-xs">
        <button
          onClick={() => setMode('feed')}
          className={`rounded-full border px-2 py-0.5 ${mode === 'feed' ? 'border-foreground' : 'border-border text-muted-foreground'}`}
        >
          Feed
        </button>
        <button
          onClick={() => setMode('insights')}
          className={`rounded-full border px-2 py-0.5 ${mode === 'insights' ? 'border-foreground' : 'border-border text-muted-foreground'}`}
        >
          Insights
        </button>
      </div>
      {mode === 'feed' ? <ActivityFeed documentId={documentId} /> : <AuditVisualization documentId={documentId} />}
    </div>
  )
}

// ActivityFeed renders the chronological event stream (versions,
// comments, workflow transitions, signatures, audit hits) for one
// document. Backed by the ADR 0074 ActivityForDocument GraphQL
// operation — the merge happens server-side so this component is a
// thin renderer.
function ActivityFeed({ documentId }: { documentId: string }) {
  const { data, isLoading, isError, error, refetch, isFetching } = useActivityForDocument(documentId)
  if (isLoading) return <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
  if (isError) {
    return (
      <Card className="flex items-start gap-3 p-4 text-sm" data-testid="activity-error">
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
        <div className="min-w-0 flex-1">
          <p className="font-medium">Could not load activity</p>
          <p className="text-muted-foreground">{error instanceof Error ? error.message : String(error)}</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
          {isFetching ? 'Retrying…' : 'Retry'}
        </Button>
      </Card>
    )
  }
  const nodes = data?.nodes ?? []
  if (nodes.length === 0) {
    return (
      <Card className="p-8 text-center text-sm text-muted-foreground" data-testid="activity-empty">
        No activity yet.
      </Card>
    )
  }
  return (
    <ol className="space-y-2" data-testid="activity-feed">
      {nodes.map((evt) => (
        <li key={evt.id} className="flex items-start gap-3 rounded-md border border-border bg-card p-3">
          <Clock className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
              <span className="font-medium text-foreground">{evt.summary}</span>
              <span className="text-xs text-muted-foreground">{formatDateTime(evt.occurredAt)}</span>
            </div>
            <p className="mt-0.5 text-xs text-muted-foreground">
              <code className="rounded bg-muted px-1 font-mono text-[10px]">{evt.kind}</code>
              {evt.actorName ? ` · ${evt.actorName}` : ''}
            </p>
          </div>
        </li>
      ))}
    </ol>
  )
}

function StatusBadge({ status }: { status: OCRStatus }) {
  const map: Record<OCRStatus, { label: string; icon: typeof Clock; tone: string }> = {
    pending:   { label: 'Pending',   icon: Clock,        tone: 'text-muted-foreground' },
    running:   { label: 'Running',   icon: RefreshCw,    tone: 'text-info' },
    completed: { label: 'Completed', icon: CheckCircle2, tone: 'text-success' },
    failed:    { label: 'Failed',    icon: AlertCircle,  tone: 'text-destructive' },
    unknown:   { label: 'Unknown',   icon: Clock,        tone: 'text-muted-foreground' },
  }
  const { label, icon: Icon, tone } = map[status]
  return (
    <span className={cn('inline-flex items-center gap-1 text-xs font-medium', tone)}>
      <Icon className={cn('h-3.5 w-3.5', status === 'running' && 'animate-spin')} />
      OCR {label}
    </span>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/documents/$documentId')({
  // `?page=N` deep-links straight to a page in the PDF layout viewer.
  // Citations on the Ask page link with this so a [1:p4] marker lands
  // on page 4 instead of page 1. Invalid/absent → undefined (page 1).
  validateSearch: (search: Record<string, unknown>): { page?: number; doctype?: 'note' | 'wiki' } => {
    const raw = search.page
    const n = typeof raw === 'string' || typeof raw === 'number' ? Number(raw) : NaN
    const out: { page?: number; doctype?: 'note' | 'wiki' } = {}
    if (Number.isFinite(n) && n >= 1) out.page = Math.floor(n)
    // `?doctype=note|wiki` lets "New note" open the collaborative editor on a
    // brand-new note before its first markdown version exists (the gateway
    // GET doesn't yet return doc_type). Survives refetch because it's in the URL.
    if (search.doctype === 'note' || search.doctype === 'wiki') out.doctype = search.doctype
    return out
  },
  component: DocumentDetailPage,
})
