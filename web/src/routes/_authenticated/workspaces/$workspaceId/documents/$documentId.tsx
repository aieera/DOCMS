import { useEffect, useRef, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { useDocument } from '@/hooks/useDocuments'
import { useAuthStore } from '@/store/authStore'
import { getOCR, rerunOCR, type OCRStatus } from '@/api/ocr'
import { getDownloadURL } from '@/api/documents'
import { PDFLayoutViewer } from '@/components/viewer/PDFLayoutViewer'
import { Badge } from '@/components/ui/Badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/Button'
import { formatFileSize, formatDateTime } from '@/lib/formatters'
import { Download, Share, History, MessageSquare, FileText, RefreshCw, AlertCircle, Clock, CheckCircle2, LayoutGrid } from 'lucide-react'
import { TagSuggestionsPanel } from '@/components/intelligence/TagSuggestionsPanel'
import { RouteSuggestionBanner } from '@/components/intelligence/RouteSuggestionBanner'
import { CompliancePanel } from '@/components/intelligence/CompliancePanel'
import { ComplianceBadge } from '@/components/intelligence/ComplianceBadge'
import { DocQAChat } from '@/components/intelligence/DocQAChat'
import { LanguageBadge } from '@/components/intelligence/LanguageBadge'
import { TranslationPanel } from '@/components/intelligence/TranslationPanel'

function DocumentDetailPage() {
  const { documentId } = Route.useParams()
  const { data: doc, isLoading } = useDocument(documentId)
  const [tab, setTab] = useState<'preview' | 'text' | 'layout' | 'qa' | 'compliance'>('preview')

  if (isLoading) return <div className="flex justify-center py-16"><Spinner className="h-8 w-8" /></div>
  if (!doc) return <div className="py-16 text-center text-sm text-[var(--color-text-secondary)]">Document not found</div>

  const versionId = (doc as unknown as { current_version_id?: string }).current_version_id

  return (
    <div className="flex gap-6">
      <div className="flex-1">
        <div className="mb-4 flex items-center gap-3">
          <FileIcon mime={doc.mime_type} className="h-8 w-8" />
          <div className="flex-1">
            <h1 className="text-xl font-bold">{doc.title}</h1>
            <p className="text-sm text-[var(--color-text-secondary)]">{doc.created_by_name} · {formatDateTime(doc.created_at)}</p>
          </div>
          <div className="flex items-center gap-2">
            <LanguageBadge documentId={documentId} />
            <ComplianceBadge documentId={documentId} />
          </div>
        </div>

        {/* ADR 0053 — banner appears only when smart_route produced
            pending suggestions for this doc. Self-hides otherwise. */}
        <div className="mb-3">
          <RouteSuggestionBanner documentId={documentId} />
        </div>

        <div className="mb-3 flex gap-1 border-b border-[var(--color-border)]">
          <TabButton active={tab === 'preview'} onClick={() => setTab('preview')}>Preview</TabButton>
          <TabButton active={tab === 'text'} onClick={() => setTab('text')}>
            <FileText className="mr-1 h-3 w-3" /> Raw text
          </TabButton>
          <TabButton active={tab === 'layout'} onClick={() => setTab('layout')}>
            <LayoutGrid className="mr-1 h-3 w-3" /> Layout
          </TabButton>
          <TabButton active={tab === 'qa'} onClick={() => setTab('qa')}>
            <MessageSquare className="mr-1 h-3 w-3" /> Q&amp;A
          </TabButton>
          <TabButton active={tab === 'compliance'} onClick={() => setTab('compliance')}>
            <AlertCircle className="mr-1 h-3 w-3" /> Compliance
          </TabButton>
        </div>

        {tab === 'preview' && (
          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
            Document viewer placeholder — PDF/image/video viewer renders here
          </div>
        )}

        {tab === 'text' && (
          <OCRPanel documentId={documentId} versionId={versionId} />
        )}

        {tab === 'layout' && (
          <LayoutTab documentId={documentId} versionId={versionId} mimeType={doc.mime_type} />
        )}

        {tab === 'qa' && (
          <DocQAChat documentId={documentId} />
        )}

        {tab === 'compliance' && (
          <CompliancePanel documentId={documentId} />
        )}
      </div>

      <aside className="w-80 shrink-0 space-y-4">
        <div className="flex gap-2">
          <Button variant="outline" size="sm"><Download className="h-4 w-4" /> Download</Button>
          <Button variant="outline" size="sm"><Share className="h-4 w-4" /> Share</Button>
        </div>
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 space-y-3">
          <h3 className="text-sm font-semibold">Details</h3>
          <div className="space-y-2 text-sm">
            <Row label="Status"><Badge variant={doc.lifecycle_state}>{doc.lifecycle_state}</Badge></Row>
            <Row label="Type">{doc.document_class || 'Unclassified'}</Row>
            <Row label="Size">{formatFileSize(doc.size_bytes)}</Row>
            <Row label="Versions">{doc.version_count}</Row>
            <Row label="MIME">{doc.mime_type}</Row>
          </div>
          {(doc.tags?.length ?? 0) > 0 && (
            <div>
              <p className="mb-1 text-xs font-medium text-[var(--color-text-secondary)]">Tags</p>
              <div className="flex flex-wrap gap-1">
                {(doc.tags ?? []).map((t) => <Badge key={t}>{t}</Badge>)}
              </div>
            </div>
          )}
        </div>
        <div className="flex flex-col gap-2">
          <Button variant="ghost" size="sm" className="justify-start"><History className="h-4 w-4" /> Version History</Button>
          <Button variant="ghost" size="sm" className="justify-start"><MessageSquare className="h-4 w-4" /> Comments</Button>
        </div>

        {/* Intelligence panels — each component self-hides when it has
            nothing to render, so the sidebar stays compact for docs
            that haven't reached the relevant pipeline stage yet. */}
        <TagSuggestionsPanel documentId={documentId} />
        {versionId && (
          <TranslationPanel documentId={documentId} versionId={versionId} />
        )}
      </aside>
    </div>
  )
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      className={`flex items-center px-3 py-2 text-sm border-b-2 -mb-px transition-colors ${
        active
          ? 'border-[var(--color-primary)] text-[var(--color-primary)] font-medium'
          : 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text)]'
      }`}
    >
      {children}
    </button>
  )
}

function OCRPanel({ documentId, versionId }: { documentId: string; versionId?: string }) {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const canRerun = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  // Track previous status across renders so we only fire transition
  // toasts on the actual change, not on every poll re-render.
  const lastStatus = useRef<OCRStatus | null>(null)

  const { data, isLoading, refetch } = useQuery({
    queryKey: ['ocr', documentId, versionId],
    queryFn: () => getOCR(documentId, versionId!),
    enabled: Boolean(versionId),
    // Poll every 5s while OCR is running so the UI updates live.
    refetchInterval: (query) => {
      const status = (query.state.data?.status ?? 'unknown') as OCRStatus
      return status === 'running' || status === 'pending' ? 5000 : false
    },
  })

  // Surface every status transition as a toast so the user sees real
  // progress instead of a silently-changing badge. Only fires on
  // change; the unknown→initial transition is suppressed so we don't
  // spam a toast on first load.
  useEffect(() => {
    const cur = (data?.status ?? 'unknown') as OCRStatus
    const prev = lastStatus.current
    lastStatus.current = cur
    if (prev === null || prev === cur) return
    if (prev === 'unknown' && cur === 'pending') return
    switch (cur) {
      case 'running':
        toast.loading('OCR processing — first run downloads models (~5 min)', {
          id: `ocr-${versionId}`,
          duration: 10000,
        })
        break
      case 'completed':
        toast.success(
          `OCR completed${data?.total_pages ? ` — ${data.total_pages} page${data.total_pages === 1 ? '' : 's'} extracted` : ''}`,
          { id: `ocr-${versionId}` },
        )
        break
      case 'failed':
        toast.error('OCR failed — check the runbook or re-run', {
          id: `ocr-${versionId}`,
        })
        break
      case 'pending':
        toast(`OCR queued — waiting for the worker`, {
          id: `ocr-${versionId}`,
          icon: '⏳',
        })
        break
    }
  }, [data?.status, data?.total_pages, versionId])

  const rerun = useMutation({
    mutationFn: () => rerunOCR(documentId, versionId!),
    onSuccess: () => {
      toast.success('OCR re-queued')
      qc.invalidateQueries({ queryKey: ['ocr', documentId, versionId] })
    },
    onError: () => toast.error('Re-run failed'),
  })

  if (!versionId) {
    return (
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
        No version available — upload a file to enable OCR.
      </div>
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
      <div className="flex items-center justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3">
        <div className="flex items-center gap-3">
          <StatusBadge status={status} />
          {pages.length > 0 && (
            <>
              <span className="text-xs text-[var(--color-text-secondary)]">
                {pages.length} page{pages.length === 1 ? '' : 's'}
              </span>
              {avgConf > 0 && (
                <span className="text-xs text-[var(--color-text-secondary)]">
                  · avg {(avgConf * 100).toFixed(1)}% confidence
                </span>
              )}
              {pages[0]?.engine && (
                <span className="text-xs text-[var(--color-text-secondary)]">
                  · {pages[0].engine}
                </span>
              )}
            </>
          )}
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" size="sm" onClick={() => refetch()}>
            <RefreshCw className="h-3 w-3" />
          </Button>
          {canRerun && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => rerun.mutate()}
              disabled={rerun.isPending}
            >
              {rerun.isPending ? <Spinner className="h-3 w-3" /> : <RefreshCw className="h-3 w-3" />}
              Re-run OCR
            </Button>
          )}
        </div>
      </div>

      {pages.length === 0 ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
          {status === 'pending' && 'OCR has not started yet.'}
          {status === 'running' && 'OCR is running — text will appear here when complete.'}
          {status === 'failed' && 'OCR failed. Check the runbook or re-run.'}
          {status === 'completed' && 'OCR completed but no text was extracted.'}
          {status === 'unknown' && 'No OCR results available.'}
        </div>
      ) : (
        <div className="space-y-2">
          {pages.map((p) => (
            <details
              key={p.id}
              className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] open:bg-[var(--color-bg)]"
              open={pages.length <= 3}
            >
              <summary className="cursor-pointer px-3 py-2 text-sm font-medium">
                Page {p.page_number}
                <span className="ml-2 text-xs font-normal text-[var(--color-text-secondary)]">
                  · {(p.confidence * 100).toFixed(1)}% conf
                  {p.processing_time_ms ? ` · ${p.processing_time_ms}ms` : ''}
                  {p.language ? ` · ${p.language}` : ''}
                </span>
              </summary>
              <pre className="whitespace-pre-wrap break-words border-t border-[var(--color-border)] p-3 text-xs">
                {p.text_content || <span className="italic text-[var(--color-text-secondary)]">No text on this page</span>}
              </pre>
            </details>
          ))}
        </div>
      )}
    </div>
  )
}

// LayoutTab renders the PDF page-by-page with Surya bounding boxes
// overlaid, color-coded by per-line confidence. Read-only for now —
// click a box to see its text + confidence in a tooltip. Only supports
// PDFs (Surya boxes are rasterized in PDF page-pixel space); other mime
// types fall back to an explanatory empty state.
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
  const dl = useQuery({
    queryKey: ['download-url', documentId, versionId],
    queryFn: () => getDownloadURL(documentId, versionId!),
    enabled: Boolean(versionId) && isPdf,
    // Presigned URLs are short-lived (5 min default) and we'd rather
    // refetch than serve a stale 404 from a transient backend error.
    staleTime: 60 * 1000,
    retry: 1,
  })
  const forceSurya = useMutation({
    mutationFn: () => rerunOCR(documentId, versionId!, { forceEngine: 'surya' }),
    onSuccess: () => {
      toast.success('Re-running OCR with Surya — boxes will appear when complete')
      qc.invalidateQueries({ queryKey: ['ocr', documentId, versionId] })
    },
    onError: () => toast.error('Force-Surya rerun failed'),
  })

  if (!isPdf) {
    return (
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
        Layout view is only available for PDF documents.
      </div>
    )
  }
  if (!versionId) {
    return (
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
        No version available — upload a file to enable layout analysis.
      </div>
    )
  }
  if (ocr.isLoading || dl.isLoading) {
    return <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
  }
  if (!dl.data?.url) {
    return (
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
        Could not load document URL.
      </div>
    )
  }
  const pages = ocr.data?.pages ?? []
  const totalBoxes = pages.reduce((acc, p) => {
    const raw = p.bounding_boxes
    const arr = Array.isArray(raw) ? raw : Array.isArray((raw as { lines?: unknown[] })?.lines) ? (raw as { lines: unknown[] }).lines : []
    return acc + arr.length
  }, 0)
  return (
    <div className="space-y-3">
      {totalBoxes === 0 && (
        <div className="flex items-center justify-between gap-3 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-700 dark:text-amber-400">
          <span>
            This PDF was processed via the text-extraction fast path (pymupdf) — no bounding boxes were captured.
          </span>
          {canRerun && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => forceSurya.mutate()}
              disabled={forceSurya.isPending}
            >
              {forceSurya.isPending ? <Spinner className="h-3 w-3" /> : <RefreshCw className="h-3 w-3" />}
              Force Surya
            </Button>
          )}
        </div>
      )}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3">
        <PDFLayoutViewer url={dl.data.url} pages={pages} />
      </div>
    </div>
  )
}

function StatusBadge({ status }: { status: OCRStatus }) {
  const map: Record<OCRStatus, { label: string; icon: typeof Clock; color: string }> = {
    pending: { label: 'Pending', icon: Clock, color: 'text-slate-500' },
    running: { label: 'Running', icon: RefreshCw, color: 'text-blue-500' },
    completed: { label: 'Completed', icon: CheckCircle2, color: 'text-emerald-500' },
    failed: { label: 'Failed', icon: AlertCircle, color: 'text-red-500' },
    unknown: { label: 'Unknown', icon: Clock, color: 'text-slate-400' },
  }
  const { label, icon: Icon, color } = map[status]
  const spin = status === 'running' ? 'animate-spin' : ''
  return (
    <span className={`inline-flex items-center gap-1 text-xs font-medium ${color}`}>
      <Icon className={`h-3 w-3 ${spin}`} />
      OCR {label}
    </span>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex justify-between">
      <span className="text-[var(--color-text-secondary)]">{label}</span>
      <span>{children}</span>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/documents/$documentId')({ component: DocumentDetailPage })
