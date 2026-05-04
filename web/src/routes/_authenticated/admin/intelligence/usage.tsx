import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Activity, Coins, Cpu } from 'lucide-react'

import { getLLMUsage, type LLMUsageRow } from '@/api/llm-usage'
import { PageHeader } from '@/components/shared/PageHeader'

function fmtCost(n: number): string {
  if (!n) return '—'
  if (n >= 1) return `$${n.toFixed(2)}`
  if (n >= 0.01) return `$${n.toFixed(3)}`
  return `$${n.toFixed(5)}`
}

function LLMUsagePage() {
  const { data, isLoading } = useQuery({
    queryKey: ['llm-usage'],
    queryFn: getLLMUsage,
    refetchInterval: 30_000,
  })

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="LLM usage"
        description="Per-tenant token + cost tally aggregated across models. Each Q&A and NER LLM call increments these counters; entries TTL out after 30 days of inactivity."
      />

      <div className="mt-6 grid grid-cols-1 gap-3 md:grid-cols-4">
        <Metric
          label="Calls"
          value={(data?.totals.calls ?? 0).toLocaleString()}
          icon={<Activity className="h-4 w-4 text-emerald-500" />}
        />
        <Metric
          label="Input tokens"
          value={(data?.totals.input_tokens ?? 0).toLocaleString()}
          icon={<Cpu className="h-4 w-4 text-sky-500" />}
        />
        <Metric
          label="Output tokens"
          value={(data?.totals.output_tokens ?? 0).toLocaleString()}
          icon={<Cpu className="h-4 w-4 text-violet-500" />}
        />
        <Metric
          label="Cost"
          value={fmtCost(data?.totals.cost_usd ?? 0)}
          icon={<Coins className="h-4 w-4 text-amber-500" />}
        />
      </div>

      <section className="mt-8 rounded border border-[var(--color-border)]">
        <header className="border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-4 py-2 text-sm font-medium">
          By model
        </header>
        {isLoading ? (
          <div className="p-6 text-sm text-[var(--color-text-secondary)]">
            Loading…
          </div>
        ) : !data || data.by_model.length === 0 ? (
          <div className="p-6 text-sm text-[var(--color-text-secondary)]">
            No LLM activity yet. Ask a question in any document's Q&amp;A panel to start populating these counters.
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-xs uppercase text-[var(--color-text-secondary)]">
              <tr>
                <th className="px-4 py-2">Model</th>
                <th className="px-4 py-2 text-right">Calls</th>
                <th className="px-4 py-2 text-right">Input tokens</th>
                <th className="px-4 py-2 text-right">Output tokens</th>
                <th className="px-4 py-2 text-right">Cost</th>
              </tr>
            </thead>
            <tbody>
              {data.by_model.map((row) => (
                <Row key={row.model} row={row} />
              ))}
            </tbody>
          </table>
        )}
      </section>

      <p className="mt-6 text-xs text-[var(--color-text-secondary)]">
        Counters are stored in Redis and reset 30 days after the last call to that model.
        For long-term billing reports, wire <code>llm_gateway._meter_usage</code> to write into a
        <code> tenant_usage_events </code> table — Redis is fine for the rolling-window dashboard above.
      </p>
    </div>
  )
}

function Row({ row }: { row: LLMUsageRow }) {
  return (
    <tr className="border-t border-[var(--color-border)]">
      <td className="px-4 py-2 font-mono text-xs">{row.model}</td>
      <td className="px-4 py-2 text-right tabular-nums">{row.calls.toLocaleString()}</td>
      <td className="px-4 py-2 text-right tabular-nums">{row.input_tokens.toLocaleString()}</td>
      <td className="px-4 py-2 text-right tabular-nums">{row.output_tokens.toLocaleString()}</td>
      <td className="px-4 py-2 text-right tabular-nums">{fmtCost(row.cost_usd)}</td>
    </tr>
  )
}

function Metric({
  label,
  value,
  icon,
}: {
  label: string
  value: string
  icon?: React.ReactNode
}) {
  return (
    <div className="rounded border border-[var(--color-border)] p-4">
      <div className="flex items-center gap-2 text-xs uppercase tracking-wide text-[var(--color-text-secondary)]">
        {icon}
        {label}
      </div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/usage')({
  component: LLMUsagePage,
})
