// /admin/platform/load-tests — §16 load-test campaign archive (ADR 0105).
//
// The list of reports comes from a build-time-generated TS module
// (web/scripts/build-load-test-index.mjs scans
// docs/load-tests/<date>/summary.md and emits typed records). No
// backend endpoint — the repo IS the report database.
import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { Activity, AlertCircle, CheckCircle2, Clock, FileText, XCircle } from 'lucide-react'
import { REPORTS, type LoadTestReport, type Verdict } from '@/generated/load-test-reports'
import { PageHeader } from '@/components/shared/PageHeader'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

export const Route = createFileRoute('/_authenticated/admin/platform/load-tests')({
  component: LoadTestsPage,
})

function LoadTestsPage() {
  const [selected, setSelected] = useState<LoadTestReport | null>(REPORTS[0] ?? null)

  return (
    <div className="mx-auto max-w-7xl p-6">
      <PageHeader
        title="Load test history"
        description="Blueprint §16 sign-off runs. Each entry is a campaign with full ramp/hold/burst/cool protocol against 100 tenants × 1M docs. Add a new run by following docs/load-tests/RUNBOOK.md — the index here regenerates at build time."
      />

      {REPORTS.length === 0 ? <EmptyState /> : (
        <div className="mt-6 grid grid-cols-1 gap-4 lg:grid-cols-[320px_1fr]">
          <ul className="space-y-2">
            {REPORTS.map((r) => (
              <li key={r.slug}>
                <button
                  onClick={() => setSelected(r)}
                  className={`flex w-full items-start gap-3 rounded-md border p-3 text-start text-sm transition-colors ${
                    selected?.slug === r.slug
                      ? 'border-violet-500/50 bg-violet-50/40 dark:bg-violet-950/15'
                      : 'border-border bg-card hover:bg-accent'
                  }`}
                >
                  <VerdictIcon verdict={r.meta.verdict} />
                  <div className="min-w-0 flex-1">
                    <div className="font-semibold">{r.meta.date}</div>
                    <div className="truncate text-xs text-muted-foreground">{r.meta.campaign || r.slug}</div>
                    <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
                      {r.meta.sut_version || 'sut version not recorded'}
                    </div>
                  </div>
                  <DirectionalIcon name="ChevronRight" className="mt-1 h-3.5 w-3.5 text-muted-foreground" />
                </button>
              </li>
            ))}
          </ul>

          {selected ? <ReportDetail report={selected} /> : (
            <p className="rounded-md border border-dashed border-border p-6 text-center text-sm text-muted-foreground">
              Pick a campaign on the left to see its summary.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function EmptyState() {
  return (
    <div className="mt-6 flex flex-col items-center gap-3 rounded-md border border-dashed border-border p-12 text-center">
      <Activity className="h-10 w-10 text-muted-foreground" />
      <h2 className="text-base font-semibold">No campaigns recorded yet</h2>
      <p className="max-w-md text-sm text-muted-foreground">
        §16 sign-off requires a populated summary in
        <code className="mx-1 rounded bg-muted px-1.5 py-0.5 text-xs">docs/load-tests/&lt;YYYY-MM-DD&gt;/summary.md</code>.
        Follow the runbook at <code className="mx-1 rounded bg-muted px-1.5 py-0.5 text-xs">docs/load-tests/RUNBOOK.md</code>,
        then rebuild the frontend — the list regenerates from the repo at build time.
      </p>
    </div>
  )
}

function VerdictIcon({ verdict }: { verdict: Verdict }) {
  if (verdict === 'passed') return <CheckCircle2 className="mt-0.5 h-4 w-4 text-emerald-500" aria-label="passed" />
  if (verdict === 'failed') return <XCircle className="mt-0.5 h-4 w-4 text-destructive" aria-label="failed" />
  return <Clock className="mt-0.5 h-4 w-4 text-amber-500" aria-label="pending" />
}

function ReportDetail({ report }: { report: LoadTestReport }) {
  return (
    <article className="space-y-4 rounded-md border border-border bg-card p-5">
      <header className="space-y-1">
        <h2 className="flex items-center gap-2 text-lg font-semibold">
          <FileText className="h-5 w-5 text-muted-foreground" />
          {report.meta.campaign || report.slug}
        </h2>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs text-muted-foreground sm:grid-cols-4">
          <Field label="Date"        value={report.meta.date} />
          <Field label="SUT"         value={report.meta.sut_version} />
          <Field label="Helm"        value={report.meta.helm_chart} />
          <Field label="Run by"      value={report.meta.run_by} />
        </dl>
        <div className="pt-1">
          <VerdictBadge verdict={report.meta.verdict} />
        </div>
      </header>

      {report.meta.verdict === 'pending' && (
        <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-50/60 p-2 text-xs dark:bg-amber-950/20">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 text-amber-700 dark:text-amber-300" />
          <span>
            Verdict is "pending" — this campaign has not been finalised. A buyer-facing report MUST have every §16 SLO row filled in and the verdict flipped to <code>passed</code> or <code>failed</code> before it counts.
          </span>
        </div>
      )}

      {/* The summary body is Markdown. We render it as a preformatted
          block on purpose: the FE has no markdown renderer dep and
          shipping one to render a single static doc is overkill.
          Operators copy/paste the relevant table out when they need
          to share it. */}
      <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap rounded-md border border-border bg-background p-3 font-mono text-xs leading-5">
        {report.body}
      </pre>
    </article>
  )
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">{label}</dt>
      <dd className="truncate font-mono text-[12px] text-foreground">{value || '—'}</dd>
    </div>
  )
}

function VerdictBadge({ verdict }: { verdict: Verdict }) {
  const styles: Record<Verdict, string> = {
    passed:  'border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
    failed:  'border-destructive/40 bg-destructive/10 text-destructive',
    pending: 'border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300',
  }
  return (
    <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-semibold uppercase ${styles[verdict]}`}>
      <VerdictIcon verdict={verdict} />
      {verdict}
    </span>
  )
}
