import { useTranslation } from 'react-i18next'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Activity, Coins, Cpu } from 'lucide-react'

import { getLLMUsage, type LLMUsageRow } from '@/api/llm-usage'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatUsd as fmtCost } from '@/lib/formatters'

export function LLMUsagePage() {
  const { t } = useTranslation('common')
  const { data, isLoading } = useQuery({
    queryKey: ['llm-usage'],
    queryFn: getLLMUsage,
    refetchInterval: 30_000,
  })

  return (
    <div className="space-y-6">
      <PageHeader variant="section"
        title={t('adm.use.title')}
        description={t('adm.use.description')}
      />

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Metric label={t('adm.use.m_calls')} value={(data?.totals.calls ?? 0).toLocaleString()} icon={<Activity className="h-4 w-4 text-success" />} />
        <Metric label={t('adm.use.m_input')} value={(data?.totals.input_tokens ?? 0).toLocaleString()} icon={<Cpu className="h-4 w-4 text-info" />} />
        <Metric label={t('adm.use.m_output')} value={(data?.totals.output_tokens ?? 0).toLocaleString()} icon={<Cpu className="h-4 w-4 text-info" />} />
        <Metric label={t('adm.use.m_cost')} value={fmtCost(data?.totals.cost_usd ?? 0)} icon={<Coins className="h-4 w-4 text-warning" />} />
      </div>

      <Card className="overflow-hidden p-0">
        <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {t('adm.use.by_model')}
        </div>
        {isLoading ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        ) : !data || data.by_model.length === 0 ? (
          <p className="p-6 text-center text-sm text-muted-foreground">
            {t('adm.use.empty')}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted/20">
                <tr>
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground text-start">{t('adm.use.col_model')}</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.use.col_calls')}</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.use.col_input')}</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.use.col_output')}</th>
                  <th className="px-4 py-2.5 text-end text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.use.col_cost')}</th>
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
        {t('adm.use.footnote')}
      </p>
    </div>
  )
}

function Row({ row }: { row: LLMUsageRow }) {
  return (
    <tr className="border-t border-border">
      <td dir="ltr" className="px-4 py-3 font-mono text-xs">{row.model}</td>
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

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/ai?tab=usage). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/intelligence/usage')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/ai', search: { tab: 'usage' }, replace: true })
  },
})
