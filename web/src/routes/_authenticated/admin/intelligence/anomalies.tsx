import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
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
  getAnomalyConfig,
  updateAnomalyConfig,
  type AnomalyConfig,
} from '@/api/anomaly'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { useAuthStore } from '@/store/authStore'
import { IntelligenceConfigCard, type ConfigField } from '@/components/admin/IntelligenceConfigCard'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/shadcn/sheet'

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

// GET/PUT /admin/anomaly-config — existed server-side with a written
// client (api/anomaly.ts) but no UI until now.
const ANOMALY_CONFIG_FIELDS: ConfigField<AnomalyConfig>[] = [
  { key: 'enabled', label: 'Scheduled scans enabled', kind: 'toggle' },
  { key: 'schedule_cron', label: 'Schedule (cron)', kind: 'text', hint: 'e.g. 0 2 * * * for nightly at 02:00' },
  { key: 'z_score_threshold', label: 'Z-score threshold', kind: 'number', min: 1, max: 10, step: 0.1, hint: 'Metadata outlier sensitivity — lower flags more' },
  { key: 'content_distance_threshold', label: 'Content distance threshold', kind: 'number', min: 0, max: 1, step: 0.05, hint: 'Embedding distance for content outliers' },
  { key: 'min_documents_for_analysis', label: 'Min documents per workspace', kind: 'number', min: 1, step: 1, hint: 'Workspaces below this are skipped' },
  { key: 'analyze_metadata', label: 'Analyze metadata', kind: 'toggle' },
  { key: 'analyze_content', label: 'Analyze content embeddings', kind: 'toggle' },
  { key: 'analyze_behavioral', label: 'Analyze upload behavior', kind: 'toggle' },
]

function AnomalyDashboardPage() {
  const qc = useQueryClient()
  const role = useAuthStore((st) => st.user?.role)
  const canEditConfig = role === 'admin' || role === 'owner'
  const [openReport, setOpenReport] = useState<string | null>(null)

  const { data: reports, isLoading } = useQuery({
    queryKey: ['anomaly-reports'],
    queryFn: () => listAnomalyReports({ limit: 30 }),
    refetchInterval: (q) => {
      const r = (q.state.data as { reports: AnomalyReport[] } | undefined)?.reports ?? []
      return r.some((x) => x.status === 'pending' || x.status === 'processing') ? 5_000 : 30_000
    },
  })

  const run = useAppMutation({
    mutationFn: () => runAnomalyAnalysis({ analysis_type: 'combined' }),
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ['anomaly-reports'] })
      toast.success(`Analysis queued (report ${resp.report_id.slice(0, 8)}…)`)
      setOpenReport(resp.report_id)
    },
    onError: () => toast.error('Failed to start analysis'),
  })

  // Sum of anomalies_found across completed reports — includes findings
  // that were later resolved or marked false-positive, so this is
  // "reported", not "open" (the per-finding status lives one level
  // deeper and isn't aggregated by this endpoint).
  const reportedCount = (reports?.reports ?? [])
    .filter((r) => r.status === 'completed')
    .reduce((acc, r) => acc + r.anomalies_found, 0)

  return (
    <div className="max-w-6xl">
      <PageHeader
        title="Anomaly detection"
        description="Workspace-level outlier scans across metadata, content embeddings, and upload behavior."
        actions={
          <Button size="sm" onClick={() => run.mutate()} disabled={run.isPending}>
            <Play className="me-2 h-4 w-4" />
            Run analysis
          </Button>
        }
      />

      <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Metric label="Reports" value={(reports?.total ?? 0).toLocaleString()} />
        <Metric label="Findings reported" value={reportedCount.toLocaleString()} />
        <Metric
          label="Last scan"
          value={(reports?.reports?.[0]?.created_at
            ? new Date(reports.reports[0].created_at).toLocaleString()
            : '—')}
        />
      </div>

      {canEditConfig && (
        <div className="mt-6">
          <IntelligenceConfigCard
            title="Scan settings"
            description="Schedule and sensitivity for the workspace outlier scans."
            queryKey={['anomaly-config']}
            fetchConfig={getAnomalyConfig}
            saveConfig={updateAnomalyConfig}
            fields={ANOMALY_CONFIG_FIELDS}
            testid="anomaly-config"
          />
        </div>
      )}

      <div className="mt-8 rounded border border-border">
        <div className="flex items-center justify-between border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          <span>Recent reports</span>
          {(reports?.total ?? 0) > 30 && (
            <span className="font-normal normal-case tracking-normal">
              showing the 30 most recent of {reports?.total}
            </span>
          )}
        </div>
        <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-start text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-2">Created</th>
              <th className="px-4 py-2">Type</th>
              <th className="px-4 py-2">Status</th>
              <th className="px-4 py-2 text-end">Docs</th>
              <th className="px-4 py-2 text-end">Anomalies</th>
              <th className="px-4 py-2 text-end"></th>
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
                <td className="px-4 py-2 text-end tabular-nums">{r.total_documents}</td>
                <td className="px-4 py-2 text-end tabular-nums">
                  {r.anomalies_found}
                  {r.anomalies_found > 0 && (
                    <AlertTriangle className="ms-1 inline h-3 w-3 text-warning" />
                  )}
                </td>
                <td className="px-4 py-2 text-end">
                  <Button size="sm" variant="ghost" onClick={() => setOpenReport(r.id)}>
                    <Search className="h-4 w-4" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
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

  const resolve = useAppMutation({
    mutationFn: ({ id, status }: { id: string; status: FindingStatus }) =>
      resolveAnomalyFinding(id, status),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['anomaly-report', reportId] })
      qc.invalidateQueries({ queryKey: ['anomaly-reports'] })
    },
    onError: () => toast.error('Update failed'),
  })

  // Radix Sheet replaces the hand-rolled fixed-inset div: focus trap,
  // Escape-to-close, role="dialog"/aria wiring, and scroll lock come
  // from the primitive instead of being (absent) hand-rolled concerns.
  return (
    <Sheet open onOpenChange={(open) => { if (!open) onClose() }}>
      <SheetContent side="right" className="flex w-full max-w-2xl flex-col gap-0 p-0 sm:max-w-2xl">
        <SheetHeader className="border-b border-border px-4 py-3">
          <SheetTitle className="flex items-center gap-2 text-sm font-medium">
            <AlertTriangle className="h-4 w-4 text-warning" />
            Report {reportId.slice(0, 8)}…
            {data?.report && (
              <Badge variant={data.report.status === 'completed' ? 'active' : 'in_review'}>
                {data.report.status}
              </Badge>
            )}
          </SheetTitle>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto p-4">
          {isLoading && <div className="text-sm text-muted-foreground">Loading…</div>}
          {data && (
            <>
              <div className="mb-3 text-xs text-muted-foreground">
                {data.report.analysis_type} · {data.report.total_documents} docs scanned ·{' '}
                {data.report.anomalies_found} anomalies
                {data.report.error_message && (
                  <span className="ms-2 text-destructive">· {data.report.error_message}</span>
                )}
              </div>
              {data.report.summary && Object.keys(data.report.summary).length > 0 && (
                <div className="mb-4">
                  <KeyValueList data={data.report.summary as Record<string, unknown>} />
                </div>
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
      </SheetContent>
    </Sheet>
  )
}

// Renders a flat object as label/value rows; nested values fall back to
// compact JSON. Replaces the raw JSON.stringify <pre> dumps that made
// the report summary and finding evidence read like a debugger.
function KeyValueList({ data }: { data: Record<string, unknown> }) {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 rounded bg-muted/40 p-3 text-xs">
      {Object.entries(data).map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="font-medium text-muted-foreground">{k.replace(/_/g, ' ')}</dt>
          <dd className="break-all font-mono">
            {typeof v === 'object' && v !== null ? JSON.stringify(v) : String(v)}
          </dd>
        </div>
      ))}
    </dl>
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
          <div className="mt-1">
            <KeyValueList data={f.evidence as Record<string, unknown>} />
          </div>
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
