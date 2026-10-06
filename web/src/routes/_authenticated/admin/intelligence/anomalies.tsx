import { Trans, useTranslation } from 'react-i18next'
import { useState, useMemo } from 'react'
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
import { formatDateTime } from '@/lib/formatters'

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

// GET/PUT /admin/anomaly-config — existed server-side with a written
// client (api/anomaly.ts) but no UI until now.
// Field shape + bounds only; label and hint resolve from the catalogue
// (adm.anom.cfg_<key> / _hint) at render time.
const ANOMALY_CONFIG_FIELDS: Omit<ConfigField<AnomalyConfig>, 'label' | 'hint'>[] = [
  { key: 'enabled', kind: 'toggle' },
  { key: 'schedule_cron', kind: 'text' },
  { key: 'z_score_threshold', kind: 'number', min: 1, max: 10, step: 0.1 },
  { key: 'content_distance_threshold', kind: 'number', min: 0, max: 1, step: 0.05 },
  { key: 'min_documents_for_analysis', kind: 'number', min: 1, step: 1 },
  { key: 'analyze_metadata', kind: 'toggle' },
  { key: 'analyze_content', kind: 'toggle' },
  { key: 'analyze_behavioral', kind: 'toggle' },
]

function AnomalyDashboardPage() {
  const { t } = useTranslation('common')
  // The shared card needs label+hint per field; the catalogue supplies
  // them per render, with `t` in the deps so a locale change propagates.
  const configFields = useMemo(
    () => ANOMALY_CONFIG_FIELDS.map((f) => ({
      ...f,
      label: t(`adm.anom.cfg_${f.key}`),
      hint: t(`adm.anom.cfg_${f.key}_hint`, { defaultValue: '' }) || undefined,
    })),
    [t],
  )
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
      toast.success(t('adm.anom.queued', { id: resp.report_id.slice(0, 8) }))
      setOpenReport(resp.report_id)
    },
    onError: () => toast.error(t('adm.anom.err_run')),
  })

  // Sum of anomalies_found across completed reports — includes findings
  // that were later resolved or marked false-positive, so this is
  // "reported", not "open" (the per-finding status lives one level
  // deeper and isn't aggregated by this endpoint).
  const reportedCount = (reports?.reports ?? [])
    .filter((r) => r.status === 'completed')
    .reduce((acc, r) => acc + r.anomalies_found, 0)

  return (
    <div>
      <PageHeader
        title={t('adm.anom.title')}
        description={t('adm.anom.description')}
        actions={
          <Button size="sm" onClick={() => run.mutate()} disabled={run.isPending}>
            <Play className="me-2 h-4 w-4" />
            {t('adm.anom.run')}
          </Button>
        }
      />

      <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Metric label={t('adm.anom.m_reports')} value={(reports?.total ?? 0).toLocaleString()} />
        <Metric label={t('adm.anom.m_findings')} value={reportedCount.toLocaleString()} />
        <Metric
          label={t('adm.anom.m_last_scan')}
          value={(reports?.reports?.[0]?.created_at
            ? formatDateTime(reports.reports[0].created_at)
            : '—')}
        />
      </div>

      <div className="mt-6 rounded border border-border">
        <div className="flex items-center justify-between border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          <span>{t('adm.anom.recent')}</span>
          {(reports?.total ?? 0) > 30 && (
            <span className="font-normal normal-case tracking-normal">
              {t('adm.anom.showing', { total: reports?.total })}
            </span>
          )}
        </div>
        <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-start text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-2">{t('adm.anom.col_created')}</th>
              <th className="px-4 py-2">{t('adm.anom.col_type')}</th>
              <th className="px-4 py-2">{t('adm.anom.col_status')}</th>
              <th className="px-4 py-2 text-end">{t('adm.anom.col_docs')}</th>
              <th className="px-4 py-2 text-end">{t('adm.anom.col_anomalies')}</th>
              <th className="px-4 py-2 text-end"></th>
            </tr>
          </thead>
          <tbody>
            {isLoading && (
              <tr><td colSpan={6} className="px-4 py-4 text-center text-muted-foreground">{t('adm.anom.loading')}</td></tr>
            )}
            {!isLoading && (reports?.reports ?? []).length === 0 && (
              <tr><td colSpan={6} className="px-4 py-4 text-center text-muted-foreground">
                <Trans i18nKey="adm.anom.empty" ns="common" components={[<strong key="r" />]} />
              </td></tr>
            )}
            {(reports?.reports ?? []).map((r) => (
              <tr key={r.id} className="border-t border-border">
                <td dir="auto" className="px-4 py-2 text-muted-foreground">{new Date(r.created_at).toLocaleString()}</td>
                <td className="px-4 py-2">{t(`adm.anom.at_${r.analysis_type}`, { defaultValue: r.analysis_type })}</td>
                <td className="px-4 py-2">
                  <Badge variant={r.status === 'completed' ? 'active' : r.status === 'failed' ? 'disposed' : 'in_review'}>
                    {t(`adm.anom.rs_${r.status}`, { defaultValue: r.status })}
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

      {canEditConfig && (
        <div className="mt-8">
          <IntelligenceConfigCard
            title={t('adm.anom.scan_settings')}
            description={t('adm.anom.scan_settings_desc')}
            queryKey={['anomaly-config']}
            fetchConfig={getAnomalyConfig}
            saveConfig={updateAnomalyConfig}
            fields={configFields}
            testid="anomaly-config"
          />
        </div>
      )}

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
  const { t } = useTranslation('common')
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
    onError: () => toast.error(t('adm.anom.err_update')),
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
            {t('adm.anom.report_title', { id: reportId.slice(0, 8) })}
            {data?.report && (
              <Badge variant={data.report.status === 'completed' ? 'active' : 'in_review'}>
                {t(`adm.anom.rs_${data.report.status}`, { defaultValue: data.report.status })}
              </Badge>
            )}
          </SheetTitle>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto p-4">
          {isLoading && <div className="text-sm text-muted-foreground">{t('adm.anom.loading')}</div>}
          {data && (
            <>
              <div className="mb-3 text-xs text-muted-foreground">
                {t('adm.anom.scan_summary', {
                  type: t(`adm.anom.at_${data.report.analysis_type}`, { defaultValue: data.report.analysis_type }),
                  docs: data.report.total_documents,
                  anomalies: data.report.anomalies_found,
                })}
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
                <div className="text-sm text-muted-foreground">{t('adm.anom.no_findings')}</div>
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
  const { t } = useTranslation('common')
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 rounded bg-muted/40 p-3 text-xs">
      {Object.entries(data).map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="font-medium text-muted-foreground">{t(`adm.anom.ev_${k}`, { defaultValue: k.replace(/_/g, ' ') })}</dt>
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
  const { t } = useTranslation('common')
  const f = finding
  return (
    <li className="rounded border border-border p-3 text-sm">
      <div className="flex items-center gap-2">
        <span aria-hidden className={`h-2 w-2 rounded-full ${SEVERITY_DOT[f.severity]}`} />
        <span className="text-xs font-medium">{t(`adm.anom.ty_${f.anomaly_type}`, { defaultValue: f.anomaly_type.replace(/_/g, ' ') })}</span>
        <Badge variant={SEVERITY_VARIANT[f.severity]} className="text-[10px]">
          {t(`adm.anom.sev_${f.severity}`, { defaultValue: f.severity })}
        </Badge>
        <span className="flex-1" />
        <Badge variant={STATUS_VARIANT[f.status]}>{t(`adm.anom.fs_${f.status}`)}</Badge>
      </div>
      <div className="mt-2 text-foreground">{f.description}</div>
      <div className="mt-2 text-xs text-muted-foreground">
        {t('adm.anom.document')}<span dir="ltr" className="font-mono">{f.document_id.slice(0, 8)}…</span>
      </div>
      {f.evidence && Object.keys(f.evidence).length > 0 && (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
            {t('adm.anom.evidence')}
          </summary>
          <div className="mt-1">
            <KeyValueList data={f.evidence as Record<string, unknown>} />
          </div>
        </details>
      )}
      {f.status === 'open' && (
        <div className="mt-3 flex gap-2">
          <Button size="sm" variant="outline" disabled={busy} onClick={() => onUpdate('acknowledged')}>
            {t('adm.anom.acknowledge')}
          </Button>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => onUpdate('resolved')}>
            {t('adm.anom.resolve')}
          </Button>
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => onUpdate('false_positive')}>
            {t('adm.anom.false_positive')}
          </Button>
        </div>
      )}
      {f.status !== 'open' && (
        <div className="mt-2 flex justify-end">
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => onUpdate('open')}>
            {t('adm.anom.reopen')}
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
      {/* "Last scan" is a formatted date and the counts are numerals --
          both start with a bidi-weak character, so without dir="auto"
          they take the paragraph direction and reorder under RTL. */}
      <div dir="auto" className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/anomalies')({
  component: AnomalyDashboardPage,
})
