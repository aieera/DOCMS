import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Activity, Coins, Cpu } from 'lucide-react'

import { getLLMUsage, type LLMUsageRow } from '@/api/llm-usage'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/Skeleton'

function fmtCost(n: number): string {
  if (!n) return '—'
  if (n >= 1) return `$${n.toFixed(2)}`
  if (n >= 0.01) return `$${n.toFixed(3)}`
  return `$${n.toFixed(5)}`
}

export function LLMUsagePage() {
  const { data, isLoading } = useQuery({
    queryKey: ['llm-usage'],
    queryFn: getLLMUsage,
    refetchInterval: 30_000,
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="LLM usage"
        description="Per-tenant token + cost tally aggregated across models. Each Q&A and NER LLM call increments these counters; entries TTL out after 30 days of inactivity."
      />

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Metric label="Calls" value={(data?.totals.calls ?? 0).toLocaleString()} icon={<Activity className="h-4 w-4 text-success" />} />
        <Metric label="Input tokens" value={(data?.totals.input_tokens ?? 0).toLocaleString()} icon={<Cpu className="h-4 w-4 text-info" />} />
        <Metric label="Output tokens" value={(data?.totals.output_tokens ?? 0).toLocaleString()} icon={<Cpu className="h-4 w-4 text-info" />} />
        <Metric label="Cost" value={fmtCost(data?.totals.cost_usd ?? 0)} icon={<Coins className="h-4 w-4 text-warning" />} />
      </div>

      <Card className="overflow-hidden p-0">
        <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          By model
        </div>
        {isLoading ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        ) : !data || data.by_model.length === 0 ? (
          <p className="p-6 text-center text-sm text-muted-foreground">
            No LLM activity yet. Ask a question in any document's Q&A panel to start populating these counters.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted/20">
                <tr className="text-start">
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Model</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">Calls</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">Input tokens</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">Output tokens</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">Cost</th>
                </tr>
              </thead>
              <tbody>
                {data.by_model.map((row) => <Row key={row.model} row={row} />)}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <p className="text-xs text-muted-foreground">
        Counters are stored in Redis and reset 30 days after the last call to that model. For long-term billing reports, wire{' '}
        <code className="rounded bg-muted px-1 font-mono">llm_gateway._meter_usage</code> to write into a{' '}
        <code className="rounded bg-muted px-1 font-mono">tenant_usage_events</code> table — Redis is fine for the rolling-window dashboard above.
      </p>
    </div>
  )
}

function Row({ row }: { row: LLMUsageRow }) {
  return (
    <tr className="border-t border-border">
      <td className="px-4 py-3 font-mono text-xs">{row.model}</td>
      <td className="px-4 py-3 text-end tabular-nums">{row.calls.toLocaleString()}</td>
      <td className="px-4 py-3 text-end tabular-nums">{row.input_tokens.toLocaleString()}</td>
      <td className="px-4 py-3 text-end tabular-nums">{row.output_tokens.toLocaleString()}</td>
      <td className="px-4 py-3 text-end tabular-nums">{fmtCost(row.cost_usd)}</td>
    </tr>
  )
}

function Metric({ label, value, icon }: { label: string; value: string; icon?: React.ReactNode }) {
  return (
    <Card className="p-4">
      <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {icon}
        {label}
      </div>
      <div className="mt-2 text-2xl font-semibold tabular-nums tracking-tight">{value}</div>
    </Card>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/usage')({
  component: LLMUsagePage,
})
