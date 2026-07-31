import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Search, FileArchive, Download, Loader2, AlertTriangle } from 'lucide-react'

import { listHolds } from '@/api/holds'
import {
  getScope, createExportJob, listExportJobs, downloadExportJob,
  type HoldScope, type ExportJob,
} from '@/api/ediscovery'
import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'

function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = bytes / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(1)} ${units[i]}`
}

export function EDiscoveryPage() {
  const qc = useQueryClient()
  const { data: holds = [] } = useQuery({ queryKey: ['holds', 'active'], queryFn: () => listHolds({ status: 'active' }) })
  const [holdId, setHoldId] = useState('')
  const [query, setQuery] = useState('')
  const [scope, setScope] = useState<HoldScope | null>(null)

  // Poll the jobs list while any job is running so progress + the download
  // link appear without a manual refresh.
  const { data: jobs = [] } = useQuery({
    queryKey: ['export-jobs'],
    queryFn: listExportJobs,
    refetchInterval: (q) => {
      const data = q.state.data as ExportJob[] | undefined
      return data?.some((j) => j.status === 'pending' || j.status === 'running') ? 2000 : false
    },
  })

  const preview = useAppMutation({
    mutationFn: () => getScope(holdId, query),
    onSuccess: setScope,
    defaultErrorMessage: 'Could not preview scope',
  })

  const request = useAppMutation({
    mutationFn: () => createExportJob(holdId, query),
    onSuccess: () => {
      toast.success('Export requested — building in the background')
      qc.invalidateQueries({ queryKey: ['export-jobs'] })
    },
    defaultErrorMessage: 'Could not request export',
  })

  const download = useAppMutation({
    mutationFn: (id: string) => downloadExportJob(id),
    defaultErrorMessage: 'Download failed',
  })

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="E-discovery export"
        description="Hold-scoped search-and-export: pick a legal hold, refine, preview the scope, and export the in-scope documents + metadata + audit trail (EDRM load file)."
      />

      <div className="mt-6 space-y-3 rounded-lg border border-border bg-card p-4">
        <div className="grid gap-2 sm:grid-cols-2">
          <div>
            <label className="text-xs font-semibold uppercase text-muted-foreground">Legal hold</label>
            <select
              className="mt-1 w-full rounded border border-border bg-background px-2 py-1.5 text-sm"
              value={holdId}
              onChange={(e) => { setHoldId(e.target.value); setScope(null) }}
              data-testid="hold-select"
            >
              <option value="">Select an active hold…</option>
              {holds.map((h) => (
                <option key={h.id} value={h.id}>{h.name} ({h.document_ids?.length ?? 0} docs)</option>
              ))}
            </select>
          </div>
          <div>
            <label className="text-xs font-semibold uppercase text-muted-foreground">Refine (title / metadata)</label>
            <input
              className="mt-1 w-full rounded border border-border bg-background px-2 py-1.5 text-sm"
              placeholder="optional search to narrow scope"
              value={query}
              onChange={(e) => { setQuery(e.target.value); setScope(null) }}
              data-testid="refine-query"
            />
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" disabled={!holdId || preview.isPending}
            onClick={() => preview.mutate(undefined)} data-testid="preview-scope">
            <Search className="h-3.5 w-3.5" /> Preview scope
          </Button>
          {scope && (
            <span className="rounded bg-muted px-2 py-1 text-sm" data-testid="scope-summary">
              <strong>{scope.doc_count}</strong> document{scope.doc_count === 1 ? '' : 's'} · ~{humanSize(scope.size_bytes)}
            </span>
          )}
          <Button size="sm" className="ms-auto" disabled={!holdId || request.isPending}
            onClick={() => request.mutate(undefined)} data-testid="request-export">
            <FileArchive className="h-3.5 w-3.5" /> Request export
          </Button>
        </div>
      </div>

      <h3 className="mt-6 text-sm font-semibold">Export jobs</h3>
      <div className="mt-2 space-y-2">
        {jobs.map((j) => (
          <div key={j.id} className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-3 text-sm" data-testid={`job-${j.id}`}>
            <JobStatusIcon status={j.status} />
            <span className="font-mono text-xs">{j.id.slice(0, 8)}</span>
            <span className="rounded bg-muted px-1.5 py-0.5 text-xs">{j.status}</span>
            <span className="text-muted-foreground">{j.doc_count} docs · {humanSize(j.size_bytes)}</span>
            {j.query && <span className="text-xs text-muted-foreground">“{j.query}”</span>}
            {j.status === 'failed' && j.error && (
              <span className="text-xs text-red-600" title={j.error}>{j.error.slice(0, 60)}</span>
            )}
            {j.download_ready && (
              <Button size="sm" variant="outline" className="ms-auto h-7" disabled={download.isPending}
                onClick={() => download.mutate(j.id)} data-testid={`download-${j.id}`}>
                <Download className="h-3.5 w-3.5" /> Download
              </Button>
            )}
          </div>
        ))}
        {jobs.length === 0 && (
          <p className="rounded border border-dashed border-border p-4 text-center text-sm text-muted-foreground">
            No export jobs yet. Pick a hold and request an export.
          </p>
        )}
      </div>
    </div>
  )
}

function JobStatusIcon({ status }: { status: ExportJob['status'] }) {
  if (status === 'completed') return <FileArchive className="h-4 w-4 text-emerald-500" />
  if (status === 'failed') return <AlertTriangle className="h-4 w-4 text-red-500" />
  return <Loader2 className="h-4 w-4 animate-spin text-blue-500" />
}

export const Route = createFileRoute('/_authenticated/admin/ediscovery')({
  component: EDiscoveryPage,
})
