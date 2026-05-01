import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { useDocument } from '@/hooks/useDocuments'
import { useAuthStore } from '@/store/authStore'
import { getOCR, rerunOCR, type OCRStatus } from '@/api/ocr'
import { Badge } from '@/components/ui/Badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/Button'
import { formatFileSize, formatDateTime } from '@/lib/formatters'
import { Download, Share, History, MessageSquare, FileText, RefreshCw, AlertCircle, Clock, CheckCircle2 } from 'lucide-react'

function DocumentDetailPage() {
  const { documentId } = Route.useParams()
  const { data: doc, isLoading } = useDocument(documentId)
  const [tab, setTab] = useState<'preview' | 'text'>('preview')

  if (isLoading) return <div className="flex justify-center py-16"><Spinner className="h-8 w-8" /></div>
  if (!doc) return <div className="py-16 text-center text-sm text-[var(--color-text-secondary)]">Document not found</div>

  const versionId = (doc as unknown as { current_version_id?: string }).current_version_id

  return (
    <div className="flex gap-6">
      <div className="flex-1">
        <div className="mb-4 flex items-center gap-3">
          <FileIcon mime={doc.mime_type} className="h-8 w-8" />
          <div>
            <h1 className="text-xl font-bold">{doc.title}</h1>
            <p className="text-sm text-[var(--color-text-secondary)]">{doc.created_by_name} · {formatDateTime(doc.created_at)}</p>
          </div>
        </div>

        <div className="mb-3 flex gap-1 border-b border-[var(--color-border)]">
          <TabButton active={tab === 'preview'} onClick={() => setTab('preview')}>Preview</TabButton>
          <TabButton active={tab === 'text'} onClick={() => setTab('text')}>
            <FileText className="mr-1 h-3 w-3" /> Extracted text
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
