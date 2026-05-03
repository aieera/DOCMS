import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { getFilingAnalytics } from '@/api/smart-routing'
import { PageHeader } from '@/components/shared/PageHeader'

function FilingAnalyticsPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['filing-analytics'],
    queryFn: getFilingAnalytics,
    refetchInterval: 30_000,
  })

  if (isLoading) return <div className="p-6 text-sm text-zinc-500">Loading…</div>
  if (!data) return null

  const acc = data.suggestion_acceptance
  const acceptancePct = (acc.acceptance_rate * 100).toFixed(1)
  const maxCount = Math.max(1, ...(data.top_categories ?? []).map((c) => c.count))

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="Filing analytics"
        description="Where documents land per category, and how well the smart-routing suggestions track real user behavior."
      />

      <div className="mt-6 grid grid-cols-4 gap-4">
        <Metric label="Total filings" value={data.total_filings.toLocaleString()} />
        <Metric label="Suggested" value={acc.total_suggested.toLocaleString()} />
        <Metric label="Accepted" value={acc.total_accepted.toLocaleString()} />
        <Metric label="Acceptance rate" value={`${acceptancePct}%`} />
      </div>

      <Section title="Top categories (by filings)">
        {(data.top_categories ?? []).length === 0 ? (
          <Empty msg="No filings recorded yet." />
        ) : (
          <ul className="space-y-2">
            {data.top_categories.map((c) => (
              <li key={c.category_key} className="flex items-center gap-3">
                <span className="w-32 truncate text-sm">{c.category_key}</span>
                <div className="h-2 flex-1 overflow-hidden rounded bg-zinc-100 dark:bg-zinc-800">
                  <div
                    className="h-full bg-violet-500"
                    style={{ width: `${(c.count / maxCount) * 100}%` }}
                  />
                </div>
                <span className="w-16 text-right text-sm tabular-nums text-zinc-500">
                  {c.count}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Top (category → folder) pairs">
        {(data.top_folder_by_category ?? []).length === 0 ? (
          <Empty msg="No filings recorded yet." />
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-xs uppercase text-zinc-500">
              <tr>
                <th className="py-2">Category</th>
                <th className="py-2">Folder</th>
                <th className="py-2 text-right">Filings</th>
              </tr>
            </thead>
            <tbody>
              {data.top_folder_by_category.map((row, i) => (
                <tr key={`${row.category_key}-${row.folder_id}-${i}`} className="border-t border-zinc-100 dark:border-zinc-900">
                  <td className="py-2">{row.category_key}</td>
                  <td className="py-2 font-mono text-xs text-zinc-500">{row.folder_id}</td>
                  <td className="py-2 text-right tabular-nums">{row.count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded border border-zinc-200 p-4 dark:border-zinc-800">
      <div className="text-xs uppercase tracking-wide text-zinc-500">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mt-8">
      <h2 className="mb-3 text-sm font-medium">{title}</h2>
      <div className="rounded border border-zinc-200 p-4 dark:border-zinc-800">{children}</div>
    </div>
  )
}

function Empty({ msg }: { msg: string }) {
  return <div className="text-sm text-zinc-500">{msg}</div>
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/filing-analytics')({
  component: FilingAnalyticsPage,
})
