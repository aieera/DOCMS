import { useEffect, useRef, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import {
  AlertCircle,
  CheckCircle2,
  CheckSquare,
  Clock,
  Download,
  Eraser,
  FileText,
  History,
  LayoutGrid,
  MessageSquare,
  RefreshCw,
  Share,
} from 'lucide-react'

import { useDocument } from '@/hooks/useDocuments'
import { useAuthStore } from '@/store/authStore'
import { getOCR, rerunOCR, type OCRStatus } from '@/api/ocr'
import { getDownloadURL } from '@/api/documents'
import { listEntities, type Entity } from '@/api/ner'
import { PDFLayoutViewer } from '@/components/viewer/PDFLayoutViewer'
import { CoauthorEditor } from '@/components/viewer/CoauthorEditor'
import { ImageAnnotationLayer } from '@/components/viewer/ImageAnnotationLayer'
import { VideoAnnotationLayer } from '@/components/viewer/VideoAnnotationLayer'
import { CommentsPanel } from '@/components/documents/CommentsPanel'
import { SignaturesPanel } from '@/components/documents/SignaturesPanel'
import { Badge } from '@/components/ui/shadcn/badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Spinner } from '@/components/ui/Spinner'
import { Skeleton } from '@/components/ui/Skeleton'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/EmptyState'
import { formatFileSize, formatDateTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'
import { CreateTaskDialog } from '@/routes/_authenticated/tasks'
import { TagSuggestionsPanel } from '@/components/intelligence/TagSuggestionsPanel'
import { RouteSuggestionBanner } from '@/components/intelligence/RouteSuggestionBanner'
import { CompliancePanel } from '@/components/intelligence/CompliancePanel'
import { ComplianceBadge } from '@/components/intelligence/ComplianceBadge'
import { DocQAChat } from '@/components/intelligence/DocQAChat'
import { LanguageBadge } from '@/components/intelligence/LanguageBadge'
import { TranslationPanel } from '@/components/intelligence/TranslationPanel'
import { OcrQualityBadge } from '@/components/intelligence/OcrQualityBadge'
import { OcrQualityPanel } from '@/components/intelligence/OcrQualityPanel'
import { CorrectClassificationButton } from '@/components/intelligence/CorrectClassificationButton'
import { EntitiesPanel } from '@/components/intelligence/EntitiesPanel'
import { HighlightedText } from '@/components/intelligence/HighlightedText'
import { RedactionReviewPanel } from '@/components/intelligence/RedactionReviewPanel'

type TabKey = 'preview' | 'text' | 'layout' | 'qa' | 'compliance' | 'entities' | 'redaction'

const TABS: { key: TabKey; label: string; icon: typeof FileText }[] = [
  { key: 'preview', label: 'Preview', icon: FileText },
  { key: 'text', label: 'Raw text', icon: FileText },
  { key: 'layout', label: 'Layout', icon: LayoutGrid },
  { key: 'qa', label: 'Q&A', icon: MessageSquare },
  { key: 'compliance', label: 'Compliance', icon: AlertCircle },
  { key: 'entities', label: 'Entities', icon: FileText },
  { key: 'redaction', label: 'Redaction', icon: Eraser },
]

function DocumentDetailPage() {
  const { documentId, workspaceId } = Route.useParams()
  const { data: doc, isLoading } = useDocument(documentId)
  const [tab, setTab] = useState<TabKey>('preview')
  const userRole = useAuthStore((s) => s.user?.role)
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
    <div className="space-y-6">
      <DocumentHeader doc={doc} workspaceId={workspaceId} documentId={documentId} />

      {/* ADR 0053 — banner appears only when smart_route produced
          pending suggestions for this doc. Self-hides otherwise. */}
      <RouteSuggestionBanner documentId={documentId} />

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_360px]">
        <div className="min-w-0 space-y-4">
          <DocumentTabs tab={tab} onChange={setTab} />

          <TabPanel current={tab} value="preview">
            <Card className="flex flex-col items-center justify-center gap-2 p-12 text-center text-sm text-muted-foreground">
              <FileIcon mime={doc.mime_type} className="h-12 w-12" />
              <p className="text-base font-medium text-foreground">{doc.title}</p>
              <p>Document viewer renders here for PDF, image, and video previews.</p>
            </Card>
          </TabPanel>

          <TabPanel current={tab} value="text">
            <OCRPanel documentId={documentId} versionId={versionId} />
          </TabPanel>

          <TabPanel current={tab} value="layout">
            <LayoutTab documentId={documentId} versionId={versionId} mimeType={doc.mime_type} />
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

          <TabPanel current={tab} value="redaction">
            <RedactionReviewPanel
              documentId={documentId}
              versionId={versionId}
              isAdminCaller={isAdminCaller}
            />
          </TabPanel>

          {/* OCR quality lives below the layout/text content because it
              quotes per-page scores users compare against the actual
              text. Self-hides when the scorer hasn't run. */}
          {(tab === 'text' || tab === 'layout') && (
            <OcrQualityPanel documentId={documentId} />
          )}
        </div>

        <DocumentSidebar
          doc={doc}
          documentId={documentId}
          versionId={versionId}
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
}: {
  doc: { title: string; mime_type: string; created_by_name?: string; created_at: string }
  workspaceId: string
  documentId: string
}) {
  return (
    <div className="space-y-3">
      <Link
        to="/workspaces/$workspaceId"
        params={{ workspaceId }}
        className="inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
      >
        ← Back to workspace
      </Link>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <span className="flex h-12 w-12 shrink-0 items-center justify-center rounded-lg border border-border bg-muted">
          <FileIcon mime={doc.mime_type} className="h-6 w-6" />
        </span>
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-2xl font-semibold tracking-tight">{doc.title}</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            {doc.created_by_name ?? 'Unknown'} · uploaded {formatDateTime(doc.created_at)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <LanguageBadge documentId={documentId} />
          <ComplianceBadge documentId={documentId} />
          <OcrQualityBadge documentId={documentId} />
        </div>
      </div>
    </div>
  )
}

// ---- Tabs ---------------------------------------------------------------

function DocumentTabs({ tab, onChange }: { tab: TabKey; onChange: (k: TabKey) => void }) {
  // Horizontal scrollable tab bar that uses a muted background "pill"
  // surface — keeps consistent with shadcn's Tabs default look. The
  // active tab gets the card surface so it visually rises above the
  // pill bar.
  return (
    <div className="overflow-x-auto" role="tablist" aria-label="Document views">
      <div className="inline-flex min-w-full gap-1 rounded-md bg-muted/60 p-1">
        {TABS.map(({ key, label, icon: Icon }) => {
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
  // role=tabpanel for screen readers; the rendered content drives its
  // own surface so wrapper stays unstyled.
  return (
    <div role="tabpanel" aria-labelledby={`tab-${value}`} className="space-y-3">
      {children}
    </div>
  )
}

// ---- Sidebar -------------------------------------------------------------

function DocumentSidebar({
  doc,
  documentId,
  versionId,
}: {
  doc: any
  documentId: string
  versionId?: string
}) {
  const [taskOpen, setTaskOpen] = useState(false)
  return (
    <aside className="space-y-4">
      {/* Primary actions — most-used commands surfaced as full-width
          buttons so they're tap-friendly and never pushed below the
          fold by intelligence panels. */}
      <Card className="space-y-2 p-3">
        <Button variant="outline" size="sm" className="w-full justify-start">
          <Download className="h-4 w-4" /> Download
        </Button>
        <Button variant="outline" size="sm" className="w-full justify-start">
          <Share className="h-4 w-4" /> Share
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="w-full justify-start"
          onClick={() => setTaskOpen(true)}
          data-testid="create-task-from-doc"
        >
          <CheckSquare className="h-4 w-4" /> Create task
        </Button>
      </Card>
      {taskOpen && (
        <CreateTaskDialog
          linkedDocumentId={documentId}
          onClose={() => setTaskOpen(false)}
          onCreated={() => { /* topbar tasks badge polls every 30s */ }}
        />
      )}

      {/* ADR 0065 — co-authoring entrypoint. Renders only for
          Office mime types and only when the configured editor is
          reachable; falls back to "Open in desktop app" otherwise. */}
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

      {/* ADR 0067 — image + video annotation layers. Mime-based
          dispatch; PDF stays on the existing PDFLayoutViewer +
          highlight/note/stamp/drawing flow. */}
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

      {/* ADR 0066 — comments side panel. */}
      <CommentsPanel documentId={documentId} />

      {/* ADR 0070 / 0071 / 0072 — signatures panel. */}
      <SignaturesPanel documentId={documentId} />

      {/* Details — fixed metadata block. Surfaces lifecycle, type,
          size, version count, mime; tags appear inline below if any. */}
      <Card className="p-4">
        <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Details
        </h3>
        <dl className="mt-3 space-y-2 text-sm">
          <Row label="Status">
            <Badge variant={doc.lifecycle_state}>{doc.lifecycle_state}</Badge>
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
          <Row label="Size">{formatFileSize(doc.size_bytes)}</Row>
          <Row label="Versions">{doc.version_count}</Row>
          <Row label="MIME"><code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">{doc.mime_type}</code></Row>
        </dl>
        {(doc.tags?.length ?? 0) > 0 && (
          <div className="mt-4 border-t border-border pt-3">
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Tags</p>
            <div className="mt-2 flex flex-wrap gap-1">
              {(doc.tags ?? []).map((t: string) => <Badge key={t}>{t}</Badge>)}
            </div>
          </div>
        )}
        <div className="mt-4 flex flex-col gap-1 border-t border-border pt-3">
          <Button variant="ghost" size="sm" className="w-full justify-start">
            <History className="h-4 w-4" /> Version history
          </Button>
          <Button variant="ghost" size="sm" className="w-full justify-start">
            <MessageSquare className="h-4 w-4" /> Comments
          </Button>
        </div>
      </Card>

      {/* Intelligence panels — each component self-hides when it has
          nothing to render, so the sidebar stays compact for docs
          that haven't reached the relevant pipeline stage yet. */}
      <TagSuggestionsPanel documentId={documentId} />
      {versionId && (
        <TranslationPanel documentId={documentId} versionId={versionId} />
      )}
    </aside>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <dt className="text-xs uppercase tracking-wider text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 truncate text-right text-sm">{children}</dd>
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
      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_360px]">
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

function OCRPanel({ documentId, versionId }: { documentId: string; versionId?: string }) {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const canRerun = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  const [highlight, setHighlight] = useState(true)
  const lastStatus = useRef<OCRStatus | null>(null)

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
    enabled: Boolean(versionId) && highlight,
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

  const rerun = useMutation({
    mutationFn: () => rerunOCR(documentId, versionId!),
    onSuccess: () => { toast.success('OCR re-queued'); qc.invalidateQueries({ queryKey: ['ocr', documentId, versionId] }) },
    onError: () => toast.error('Re-run failed'),
  })

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
                <span className="text-xs text-muted-foreground">
                  · avg {(avgConf * 100).toFixed(1)}% confidence
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
          <label className="flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={highlight}
              onChange={(e) => setHighlight(e.target.checked)}
              className="rounded border-input"
            />
            Highlight entities
          </label>
          {canRerun && (
            <Button variant="outline" size="sm" onClick={() => rerun.mutate()} disabled={rerun.isPending}>
              {rerun.isPending ? <Spinner className="h-3 w-3" /> : <RefreshCw className="h-3 w-3" />}
              Re-run OCR
            </Button>
          )}
        </div>
      </Card>

      {pages.length === 0 ? (
        <Card className="p-8 text-center text-sm text-muted-foreground">
          {status === 'pending' && 'OCR has not started yet.'}
          {status === 'running' && 'OCR is running — text will appear here when complete.'}
          {status === 'failed' && 'OCR failed. Check the runbook or re-run.'}
          {status === 'completed' && 'OCR completed but no text was extracted.'}
          {status === 'unknown' && 'No OCR results available.'}
        </Card>
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
                  <span className="ml-2 text-xs font-normal text-muted-foreground">
                    · {(p.confidence * 100).toFixed(1)}% conf
                    {p.processing_time_ms ? ` · ${p.processing_time_ms}ms` : ''}
                    {p.language ? ` · ${p.language}` : ''}
                    {highlight && pageEntities.length > 0 && (
                      <span className="ml-2">· {pageEntities.length} entit{pageEntities.length === 1 ? 'y' : 'ies'}</span>
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

function LayoutTab({ documentId, versionId, mimeType }: { documentId: string; versionId?: string; mimeType: string }) {
  const isPdf = mimeType === 'application/pdf'
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const canRerun = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  const ocr = useQuery({
    queryKey: ['ocr', documentId, versionId],
    queryFn: () => getOCR(documentId, versionId!),
    enabled: Boolean(versionId) && isPdf,
  })
  const entitiesQ = useQuery({
    queryKey: ['entities', documentId, 'for-raw-text'],
    queryFn: () => listEntities(documentId, { limit: 1000 }),
    enabled: Boolean(versionId) && isPdf,
  })
  const dl = useQuery({
    queryKey: ['download-url', documentId, versionId],
    queryFn: () => getDownloadURL(documentId, versionId!),
    enabled: Boolean(versionId) && isPdf,
    staleTime: 60 * 1000,
    retry: 1,
  })
  const forceSurya = useMutation({
    mutationFn: () => rerunOCR(documentId, versionId!, { forceEngine: 'surya' }),
    onSuccess: () => { toast.success('Re-running OCR with Surya — boxes will appear when complete'); qc.invalidateQueries({ queryKey: ['ocr', documentId, versionId] }) },
    onError: () => toast.error('Force-Surya rerun failed'),
  })

  if (!isPdf) return <Card className="p-8 text-center text-sm text-muted-foreground">Layout view is only available for PDF documents.</Card>
  if (!versionId) return <Card className="p-8 text-center text-sm text-muted-foreground">No version available — upload a file to enable layout analysis.</Card>
  if (ocr.isLoading || dl.isLoading) return <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
  if (!dl.data?.url) return <Card className="p-8 text-center text-sm text-muted-foreground">Could not load document URL.</Card>

  const pages = ocr.data?.pages ?? []
  const totalBoxes = pages.reduce((acc, p) => {
    const raw = p.bounding_boxes
    const arr = Array.isArray(raw) ? raw : Array.isArray((raw as { lines?: unknown[] })?.lines) ? (raw as { lines: unknown[] }).lines : []
    return acc + arr.length
  }, 0)
  return (
    <div className="space-y-3">
      {totalBoxes === 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-warning">
          <span>This PDF was processed via the text-extraction fast path (pymupdf) — no bounding boxes were captured.</span>
          {canRerun && (
            <Button variant="outline" size="sm" onClick={() => forceSurya.mutate()} disabled={forceSurya.isPending}>
              {forceSurya.isPending ? <Spinner className="h-3 w-3" /> : <RefreshCw className="h-3 w-3" />}
              Force Surya
            </Button>
          )}
        </div>
      )}
      <Card className="p-3">
        <PDFLayoutViewer url={dl.data.url} pages={pages} entities={entitiesQ.data?.entities} />
      </Card>
    </div>
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

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/documents/$documentId')({ component: DocumentDetailPage })
