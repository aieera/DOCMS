import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Activity, AlertTriangle, CheckCircle2, Clock, Shield } from 'lucide-react'

import { getPermissionPropagationStats } from '@/api/permission-stats'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Spinner } from '@/components/ui/Spinner'
import { cn } from '@/lib/cn'

// ADR 0083 §"SLI" — admin dashboard for permission-propagation lag.
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
    <div className="space-y-6">
      <PageHeader
        title="Permission propagation lag"
        description="Time from a permission change event to the search index update committing. Polled every 10s; underlying metric is the search service's permission_propagation_lag_seconds histogram."
      />

      {p95Breached && (
        <div
          className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-foreground"
          data-testid="sli-breach-banner"
        >
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <p className="font-medium">SLI breach</p>
            <p className="text-xs">
              p95 propagation lag is {data.p95_seconds.toFixed(2)}s — over the {SLI_THRESHOLD_SECONDS}s threshold.
              Permission changes are taking longer than expected to reach search results. Check the
              search service logs, or contact support if this persists.
            </p>
          </div>
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-3">
        <Stat icon={Clock} tone="success" label="p50" value={`${data.p50_seconds.toFixed(2)}s`} testId="lag-p50" />
        <Stat icon={Clock} tone={p95Breached ? 'destructive' : 'warning'} label={`p95 (alert > ${SLI_THRESHOLD_SECONDS}s)`} value={`${data.p95_seconds.toFixed(2)}s`} testId="lag-p95" />
        <Stat icon={Clock} tone="destructive" label="p99" value={`${data.p99_seconds.toFixed(2)}s`} testId="lag-p99" />
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <Stat icon={CheckCircle2} tone="success" label="Success" value={data.total_success.toLocaleString()} testId="lag-success" />
        <Stat icon={AlertTriangle} tone="destructive" label={`Failure (${(failureRate * 100).toFixed(1)}%)`} value={data.total_failure.toLocaleString()} testId="lag-failure" />
        <Stat icon={Activity} tone="info" label="Pending in debouncer" value={data.pending_count.toLocaleString()} testId="lag-pending" />
      </div>

      <Card className="space-y-3 p-5">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <Shield className="h-4 w-4" /> Latency distribution vs. SLI
        </h3>
        <Bar label="p50" seconds={data.p50_seconds} threshold={SLI_THRESHOLD_SECONDS} testId="bar-p50" />
        <Bar label="p95" seconds={data.p95_seconds} threshold={SLI_THRESHOLD_SECONDS} testId="bar-p95" />
        <Bar label="p99" seconds={data.p99_seconds} threshold={SLI_THRESHOLD_SECONDS} testId="bar-p99" />
        <p className="text-xs text-muted-foreground">
          Vertical line marks the {SLI_THRESHOLD_SECONDS}s alert threshold. The 5s baseline is also the debouncer's coalescing window — propagation latency below it is structural, not a sign of trouble.
        </p>
      </Card>
    </div>
  )
}

const TONE_CLASS: Record<string, string> = {
  success: 'text-success',
  warning: 'text-warning',
  destructive: 'text-destructive',
  info: 'text-info',
  muted: 'text-muted-foreground',
}

function Stat({
  icon: Icon, tone, label, value, testId,
}: {
  icon: typeof Clock
  tone: 'success' | 'warning' | 'destructive' | 'info' | 'muted'
  label: string
  value: string
  testId?: string
}) {
  return (
    <Card className="p-4" data-testid={testId}>
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{label}</span>
        <Icon className={cn('h-4 w-4', TONE_CLASS[tone])} />
      </div>
      <div className="mt-2 text-2xl font-semibold tracking-tight">{value}</div>
    </Card>
  )
}

function Bar({ label, seconds, threshold, testId }: {
  label: string
  seconds: number
  threshold: number
  testId?: string
}) {
  const max = threshold * 2
  const pct = Math.min((seconds / max) * 100, 100)
  const breached = seconds > threshold
  return (
    <div className="space-y-1" data-testid={testId}>
      <div className="flex items-center justify-between text-xs">
        <span className="font-mono text-muted-foreground">{label}</span>
        <span className={cn('font-mono', breached ? 'font-medium text-destructive' : 'text-muted-foreground')}>
          {seconds.toFixed(2)}s
        </span>
      </div>
      <div className="relative h-2 w-full rounded-full bg-muted">
        <div
          className="absolute inset-y-0 w-px bg-destructive/70"
          style={{ left: `${(threshold / max) * 100}%` }}
          aria-hidden
        />
        <div
          className={cn('h-2 rounded-full transition-all', breached ? 'bg-destructive' : 'bg-success')}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/permission-lag')({
  component: PermissionLagPage,
})
