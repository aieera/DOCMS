import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { getFilingAnalytics } from '@/api/smart-routing'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/Skeleton'

function FilingAnalyticsPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['filing-analytics'],
    queryFn: getFilingAnalytics,
    refetchInterval: 30_000,
  })

  if (isLoading) {
    return (
      <div className="space-y-6">
        <PageHeader title="Filing analytics" description="Loading…" />
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
        </div>
        <Skeleton className="h-48" />
      </div>
    )
  }
  if (!data) return null

  const acc = data.suggestion_acceptance
  const acceptancePct = (acc.acceptance_rate * 100).toFixed(1)
  const maxCount = Math.max(1, ...(data.top_categories ?? []).map((c) => c.count))

  return (
    <div className="space-y-6">
      <PageHeader
        title="Filing analytics"
        description="Where documents land per category, and how well the smart-routing suggestions track real user behavior."
      />

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Metric label="Total filings" value={data.total_filings.toLocaleString()} />
        <Metric label="Suggested" value={acc.total_suggested.toLocaleString()} />
        <Metric label="Accepted" value={acc.total_accepted.toLocaleString()} />
        <Metric label="Acceptance rate" value={`${acceptancePct}%`} />
      </div>

      <Section title="Top categories (by filings)">
        {(data.top_categories ?? []).length === 0 ? (
          <Empty msg="No filings recorded yet." />
        ) : (
          <ul className="space-y-2.5">
            {data.top_categories.map((c) => (
              <li key={c.category_key} className="flex items-center gap-3">
                <span className="w-32 truncate text-sm">{c.category_key}</span>
                <div className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full bg-info"
                    style={{ width: `${(c.count / maxCount) * 100}%` }}
                  />
                </div>
                <span className="w-16 text-right text-sm tabular-nums text-muted-foreground">{c.count}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Top (category → folder) pairs">
        {(data.top_folder_by_category ?? []).length === 0 ? (
          <Empty msg="No filings recorded yet." />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left">
                <tr>
                  <th className="pb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">Category</th>
                  <th className="pb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">Folder</th>
                  <th className="pb-2 text-right text-xs font-medium uppercase tracking-wider text-muted-foreground">Filings</th>
                </tr>
              </thead>
              <tbody>
                {data.top_folder_by_category.map((row, i) => (
                  <tr key={`${row.category_key}-${row.folder_id}-${i}`} className="border-t border-border">
                    <td className="py-2.5">{row.category_key}</td>
                    <td className="py-2.5 font-mono text-xs text-muted-foreground">{row.folder_id}</td>
                    <td className="py-2.5 text-right tabular-nums">{row.count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>
    </div>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <Card className="p-4">
      <div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">{label}</div>
      <div className="mt-2 text-2xl font-semibold tracking-tight tabular-nums">{value}</div>
    </Card>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h2 className="mb-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground">{title}</h2>
      <Card className="p-4">{children}</Card>
    </section>
  )
}

function Empty({ msg }: { msg: string }) {
  return <p className="py-4 text-center text-sm text-muted-foreground">{msg}</p>
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/filing-analytics')({
  component: FilingAnalyticsPage,
})
