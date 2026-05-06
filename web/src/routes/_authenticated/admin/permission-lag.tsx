import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Activity, AlertTriangle, CheckCircle2, Clock, Shield } from 'lucide-react'

import { getPermissionPropagationStats } from '@/api/permission-stats'
import { PageHeader } from '@/components/shared/PageHeader'
import { Spinner } from '@/components/ui/Spinner'

// ADR 0066 §"SLI" — admin dashboard for permission-propagation lag.
// Polls the search service's in-process Prometheus stats every 10s
// (cheap; reads RAM, not OpenSearch). The 5s alert threshold is the
// runbook's `histogram_quantile(0.95, …) > 5` line; we surface it
// inline so on-call sees the breach without leaving the app.

const SLI_THRESHOLD_SECONDS = 5

function PermissionLagPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['permission-propagation-stats'],
    queryFn: getPermissionPropagationStats,
    refetchInterval: 10_000,
  })

  if (isLoading || !data) {
    return (
      <div className="flex items-center justify-center py-12">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  const p95Breached = data.p95_seconds > SLI_THRESHOLD_SECONDS
  const totalCalls = data.total_success + data.total_failure
  const failureRate = totalCalls > 0 ? data.total_failure / totalCalls : 0

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader
        title="Permission propagation lag"
        description="Time from a permission change event to the search index update committing. ADR 0066. Polled every 10s; underlying metric is the search service's permission_propagation_lag_seconds histogram."
      />

      {/* SLI breach banner */}
      {p95Breached && (
        <div
          className="mb-4 flex items-start gap-2 rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-700 dark:bg-red-950 dark:text-red-200"
          data-testid="sli-breach-banner"
        >
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <p className="font-medium">SLI breach</p>
            <p>
              p95 propagation lag is {data.p95_seconds.toFixed(2)}s — over the {SLI_THRESHOLD_SECONDS}s threshold.
              Check the search service logs for `debounced propagation failed` or stuck OpenSearch updates.
            </p>
          </div>
        </div>
      )}

      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        <Card
          icon={<Clock className="h-4 w-4 text-emerald-500" />}
          label="p50"
          value={`${data.p50_seconds.toFixed(2)}s`}
          testId="lag-p50"
        />
        <Card
          icon={<Clock className={`h-4 w-4 ${p95Breached ? 'text-red-500' : 'text-amber-500'}`} />}
          label={`p95 (alert > ${SLI_THRESHOLD_SECONDS}s)`}
          value={`${data.p95_seconds.toFixed(2)}s`}
          testId="lag-p95"
        />
        <Card
          icon={<Clock className="h-4 w-4 text-rose-500" />}
          label="p99"
          value={`${data.p99_seconds.toFixed(2)}s`}
          testId="lag-p99"
        />
      </div>

      <div className="mt-3 grid grid-cols-1 gap-3 md:grid-cols-3">
        <Card
          icon={<CheckCircle2 className="h-4 w-4 text-emerald-500" />}
          label="Success"
          value={data.total_success.toLocaleString()}
          testId="lag-success"
        />
        <Card
          icon={<AlertTriangle className="h-4 w-4 text-red-500" />}
          label={`Failure (${(failureRate * 100).toFixed(1)}%)`}
          value={data.total_failure.toLocaleString()}
          testId="lag-failure"
        />
        <Card
          icon={<Activity className="h-4 w-4 text-sky-500" />}
          label="Pending in debouncer"
          value={data.pending_count.toLocaleString()}
          testId="lag-pending"
        />
      </div>

      {/* Bar visualizes p50/p95/p99 against the SLI threshold. CSS
          widths use `min(100%, ratio * 100%)` so a runaway lag value
          doesn't break the layout. */}
      <div className="mt-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
        <h3 className="mb-3 flex items-center gap-2 text-sm font-medium">
          <Shield className="h-4 w-4" />
          Latency distribution vs. SLI
        </h3>
        <Bar label="p50" seconds={data.p50_seconds} threshold={SLI_THRESHOLD_SECONDS} testId="bar-p50" />
        <Bar label="p95" seconds={data.p95_seconds} threshold={SLI_THRESHOLD_SECONDS} testId="bar-p95" />
        <Bar label="p99" seconds={data.p99_seconds} threshold={SLI_THRESHOLD_SECONDS} testId="bar-p99" />
        <p className="mt-2 text-xs text-[var(--color-text-secondary)]">
          Red zone marks the {SLI_THRESHOLD_SECONDS}s alert threshold.
          The 5s baseline is also the debouncer's coalescing window — propagation latency below it is structural,
          not a sign of trouble.
        </p>
      </div>
    </div>
  )
}

function Card({ icon, label, value, testId }: {
  icon: React.ReactNode
  label: string
  value: string
  testId?: string
}) {
  return (
    <div
      className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3"
      data-testid={testId}
    >
      <div className="flex items-center justify-between text-xs text-[var(--color-text-secondary)]">
        <span>{label}</span>
        {icon}
      </div>
      <div className="mt-1 text-xl font-semibold">{value}</div>
    </div>
  )
}

function Bar({ label, seconds, threshold, testId }: {
  label: string
  seconds: number
  threshold: number
  testId?: string
}) {
  // Scale: max display is 2x the threshold so the bar always lands
  // inside its container even on a major incident.
  const max = threshold * 2
  const pct = Math.min((seconds / max) * 100, 100)
  const breached = seconds > threshold
  return (
    <div className="mb-2" data-testid={testId}>
      <div className="mb-1 flex justify-between text-xs">
        <span className="font-mono">{label}</span>
        <span className={breached ? 'font-mono font-medium text-red-600' : 'font-mono text-[var(--color-text-secondary)]'}>
          {seconds.toFixed(2)}s
        </span>
      </div>
      <div className="relative h-2 w-full rounded-full bg-[var(--color-bg-tertiary)]">
        {/* SLI threshold marker */}
        <div
          className="absolute top-0 bottom-0 w-px bg-red-500/70"
          style={{ left: `${(threshold / max) * 100}%` }}
        />
        <div
          className={`h-2 rounded-full ${breached ? 'bg-red-500' : 'bg-emerald-500'}`}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/permission-lag')({
  component: PermissionLagPage,
})
