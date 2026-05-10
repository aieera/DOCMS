import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { AlertTriangle, Play, Search } from 'lucide-react'

import {
  getAnomalyReport,
  listAnomalyReports,
  resolveAnomalyFinding,
  runAnomalyAnalysis,
  type AnomalyFinding,
  type AnomalyReport,
  type FindingStatus,
  type Severity,
} from '@/api/anomaly'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/shadcn/button'

const SEVERITY_VARIANT: Record<Severity, string> = {
  high:   'disposed',   // red
  medium: 'in_review',  // amber
  low:    'archived',   // grey
}

const SEVERITY_DOT: Record<Severity, string> = {
  high:   'bg-destructive',
  medium: 'bg-warning/100',
  low:    'bg-muted-foreground/60',
}

const STATUS_VARIANT: Record<FindingStatus, string> = {
  open:           'in_review',
  acknowledged:   'superseded',
  resolved:       'active',
  false_positive: 'archived',
}

const STATUS_LABEL: Record<FindingStatus, string> = {
  open:           'Open',
  acknowledged:   'Acknowledged',
  resolved:       'Resolved',
  false_positive: 'False positive',
}

function AnomalyDashboardPage() {
  const qc = useQueryClient()
  const [openReport, setOpenReport] = useState<string | null>(null)

  const { data: reports, isLoading } = useQuery({
    queryKey: ['anomaly-reports'],
    queryFn: () => listAnomalyReports({ limit: 30 }),
    refetchInterval: (q) => {
      const r = (q.state.data as { reports: AnomalyReport[] } | undefined)?.reports ?? []
      return r.some((x) => x.status === 'pending' || x.status === 'processing') ? 5_000 : 30_000
    },
  })

  const run = useMutation({
    mutationFn: () => runAnomalyAnalysis({ analysis_type: 'combined' }),
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ['anomaly-reports'] })
      toast.success(`Analysis queued (report ${resp.report_id.slice(0, 8)}…)`)
      setOpenReport(resp.report_id)
    },
    onError: () => toast.error('Failed to start analysis'),
  })

  const openCount = (reports?.reports ?? [])
    .filter((r) => r.status === 'completed')
    .reduce((acc, r) => acc + r.anomalies_found, 0)

  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHeader
        title="Anomaly detection"
        description="Workspace-level outlier scans across metadata, content embeddings, and upload behavior (ADR 0058)."
        actions={
          <Button size="sm" onClick={() => run.mutate()} disabled={run.isPending}>
            <Play className="mr-2 h-4 w-4" />
            Run analysis
          </Button>
        }
      />

      <div className="mt-6 grid grid-cols-3 gap-4">
        <Metric label="Reports" value={(reports?.total ?? 0).toLocaleString()} />
        <Metric label="Open findings" value={openCount.toLocaleString()} />
        <Metric
          label="Last scan"
          value={(reports?.reports?.[0]?.created_at
            ? new Date(reports.reports[0].created_at).toLocaleString()
            : '—')}
        />
      </div>

      <div className="mt-8 rounded border border-border">
        <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Recent reports
        </div>
        <table className="w-full text-sm">
          <thead className="text-left text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-2">Created</th>
              <th className="px-4 py-2">Type</th>
              <th className="px-4 py-2">Status</th>
              <th className="px-4 py-2 text-right">Docs</th>
              <th className="px-4 py-2 text-right">Anomalies</th>
              <th className="px-4 py-2 text-right"></th>
            </tr>
          </thead>
          <tbody>
            {isLoading && (
              <tr><td colSpan={6} className="px-4 py-4 text-center text-muted-foreground">Loading…</td></tr>
            )}
            {!isLoading && (reports?.reports ?? []).length === 0 && (
              <tr><td colSpan={6} className="px-4 py-4 text-center text-muted-foreground">
                No reports yet. Click <strong>Run analysis</strong> to start one.
              </td></tr>
            )}
            {(reports?.reports ?? []).map((r) => (
              <tr key={r.id} className="border-t border-border">
                <td className="px-4 py-2 text-muted-foreground">{new Date(r.created_at).toLocaleString()}</td>
                <td className="px-4 py-2 capitalize">{r.analysis_type}</td>
                <td className="px-4 py-2">
                  <Badge variant={r.status === 'completed' ? 'active' : r.status === 'failed' ? 'disposed' : 'in_review'}>
                    {r.status}
                  </Badge>
                </td>
                <td className="px-4 py-2 text-right tabular-nums">{r.total_documents}</td>
                <td className="px-4 py-2 text-right tabular-nums">
                  {r.anomalies_found}
                  {r.anomalies_found > 0 && (
                    <AlertTriangle className="ml-1 inline h-3 w-3 text-warning" />
                  )}
                </td>
                <td className="px-4 py-2 text-right">
                  <Button size="sm" variant="ghost" onClick={() => setOpenReport(r.id)}>
                    <Search className="h-4 w-4" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {openReport && (
        <ReportDetailModal
          reportId={openReport}
          onClose={() => setOpenReport(null)}
        />
      )}
    </div>
  )
}

function ReportDetailModal({ reportId, onClose }: { reportId: string; onClose: () => void }) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['anomaly-report', reportId],
    queryFn: () => getAnomalyReport(reportId),
    refetchInterval: (q) => {
      const rep = (q.state.data as { report: AnomalyReport } | undefined)?.report
      return rep && (rep.status === 'pending' || rep.status === 'processing') ? 3_000 : false
    },
  })

  const resolve = useMutation({
    mutationFn: ({ id, status }: { id: string; status: FindingStatus }) =>
      resolveAnomalyFinding(id, status),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['anomaly-report', reportId] })
      qc.invalidateQueries({ queryKey: ['anomaly-reports'] })
    },
    onError: () => toast.error('Update failed'),
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div
        className="flex h-[85vh] w-[min(1100px,95vw)] flex-col rounded-lg bg-card shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <div className="flex items-center gap-2 text-sm font-medium">
            <AlertTriangle className="h-4 w-4 text-warning" />
            Report {reportId.slice(0, 8)}…
            {data?.report && (
              <Badge variant={data.report.status === 'completed' ? 'active' : 'in_review'}>
                {data.report.status}
              </Badge>
            )}
          </div>
          <Button size="sm" variant="ghost" onClick={onClose}>
            Close
          </Button>
        </div>

        <div className="flex-1 overflow-y-auto p-4">
          {isLoading && <div className="text-sm text-muted-foreground">Loading…</div>}
          {data && (
            <>
              <div className="mb-3 text-xs text-muted-foreground">
                {data.report.analysis_type} · {data.report.total_documents} docs scanned ·{' '}
                {data.report.anomalies_found} anomalies
                {data.report.error_message && (
                  <span className="ml-2 text-destructive">· {data.report.error_message}</span>
                )}
              </div>
              {data.report.summary && Object.keys(data.report.summary).length > 0 && (
                <pre className="mb-4 rounded bg-muted/40 p-3 text-xs">
                  {JSON.stringify(data.report.summary, null, 2)}
                </pre>
              )}
              {(data.findings ?? []).length === 0 ? (
                <div className="text-sm text-muted-foreground">No findings.</div>
              ) : (
                <ul className="space-y-2">
                  {data.findings.map((f) => (
                    <FindingCard
                      key={f.id}
                      finding={f}
                      busy={resolve.isPending}
                      onUpdate={(status) => resolve.mutate({ id: f.id, status })}
                    />
                  ))}
                </ul>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function FindingCard({
  finding,
  busy,
  onUpdate,
}: {
  finding: AnomalyFinding
  busy: boolean
  onUpdate: (status: FindingStatus) => void
}) {
  const f = finding
  return (
    <li className="rounded border border-border p-3 text-sm">
      <div className="flex items-center gap-2">
        <span aria-hidden className={`h-2 w-2 rounded-full ${SEVERITY_DOT[f.severity]}`} />
        <span className="font-mono text-xs uppercase">{f.anomaly_type}</span>
        <Badge variant={SEVERITY_VARIANT[f.severity]} className="text-[10px]">
          {f.severity}
        </Badge>
        <span className="flex-1" />
        <Badge variant={STATUS_VARIANT[f.status]}>{STATUS_LABEL[f.status]}</Badge>
      </div>
      <div className="mt-2 text-foreground">{f.description}</div>
      <div className="mt-2 text-xs text-muted-foreground">
        Document: <span className="font-mono">{f.document_id.slice(0, 8)}…</span>
      </div>
      {f.evidence && Object.keys(f.evidence).length > 0 && (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
            Evidence
          </summary>
          <pre className="mt-1 rounded bg-muted/40 p-2 text-[11px]">
            {JSON.stringify(f.evidence, null, 2)}
          </pre>
        </details>
      )}
      {f.status === 'open' && (
        <div className="mt-3 flex gap-2">
          <Button size="sm" variant="outline" disabled={busy} onClick={() => onUpdate('acknowledged')}>
            Acknowledge
          </Button>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => onUpdate('resolved')}>
            Resolve
          </Button>
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => onUpdate('false_positive')}>
            False positive
          </Button>
        </div>
      )}
      {f.status !== 'open' && (
        <div className="mt-2 flex justify-end">
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => onUpdate('open')}>
            Reopen
          </Button>
        </div>
      )}
    </li>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded border border-border p-4">
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/anomalies')({
  component: AnomalyDashboardPage,
})
