import { useTranslation } from 'react-i18next'
import { useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import {
  getComplianceDashboard,
  listPendingFindings,
  type RiskLevel,
} from '@/api/compliance-pii'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { ErrorState } from '@/components/ui/ErrorState'
import { formatDateTime } from '@/lib/formatters'

const RISK_LEVELS: RiskLevel[] = ['critical', 'high', 'medium', 'low']
// Severity display order. risk_distribution arrives as a Go map → JSON keys in
// ALPHABETICAL order (critical, high, low, medium, none), which renders "Low"
// before "Medium". Re-order by real severity.
const RISK_ORDER = ['critical', 'high', 'medium', 'low', 'none']

const RISK_COLOR: Record<string, string> = {
  critical: 'bg-destructive',
  high:     'bg-warning-strong',
  medium:   'bg-warning',
  low:      'bg-success',
  none:     'bg-muted-foreground/40',
}

export function ComplianceAdminDashboard() {
  const { t } = useTranslation('common')
  const [riskFilter, setRiskFilter] = useState<string>('')

  const { data: dash, isLoading: dashLoading, isError: dashError, refetch: refetchDash } = useQuery({
    queryKey: ['compliance-dashboard'],
    queryFn: getComplianceDashboard,
    refetchInterval: 30_000,
  })

  const PAGE = 50
  const [page, setPage] = useState(0)
  const { data: findings, isLoading: findingsLoading } = useQuery({
    queryKey: ['compliance-pending', riskFilter, page],
    queryFn: () =>
      listPendingFindings({
        risk_level: riskFilter || undefined,
        limit: PAGE,
        offset: page * PAGE,
      }),
    refetchInterval: 30_000,
  })

  if (dashLoading) return <div className="p-6 text-sm text-muted-foreground">Loading…</div>
  // A failed dashboard fetch used to render "Loading…" forever — an
  // expired session or 403 looked like a hang.
  if (dashError || !dash) {
    return <ErrorState message={t('adm.comp2.err_load')} onRetry={() => void refetchDash()} />
  }

  const totalRisk = Object.values(dash.risk_distribution).reduce((a, b) => a + b, 0) || 1

  return (
    <div>
      <PageHeader variant="section"
        title={t('adm.comp2.title')}
        description={t('adm.comp2.description')}
      />

      <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Metric label={t('adm.comp2.m_scanned')} value={dash.total_documents_scanned} />
        <Metric label={t('adm.comp2.m_with')} value={dash.documents_with_findings} />
        <Metric label={t('adm.comp2.m_open')} value={dash.open_findings} />
        <Metric label={t('adm.comp2.m_held')} value={dash.auto_held_documents} />
      </div>

      <Section title={t('adm.comp2.risk_dist')}>
        <div className="space-y-2">
          {Object.entries(dash.risk_distribution)
            .sort(([a], [b]) => RISK_ORDER.indexOf(a) - RISK_ORDER.indexOf(b))
            .map(([risk, count]) => (
            <div key={risk} className="flex items-center gap-3 text-sm">
              <span className={`h-2 w-2 rounded-full ${RISK_COLOR[risk] ?? 'bg-muted-foreground/40'}`} />
              <span className="w-24">{t(`adm.comp2.r_${risk}`, { defaultValue: risk })}</span>
              <div className="h-2 flex-1 overflow-hidden rounded bg-muted">
                <div
                  className={RISK_COLOR[risk] ?? 'bg-muted-foreground/40'}
                  style={{ height: '100%', width: `${(count / totalRisk) * 100}%` }}
                />
              </div>
              <span className="w-12 text-end tabular-nums text-muted-foreground">{count}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section title={t('adm.comp2.top_entities')}>
        {(dash.top_entity_types ?? []).length === 0 ? (
          <div className="text-sm text-muted-foreground">{t('adm.comp2.no_findings')}</div>
        ) : (
          <ul className="space-y-2">
            {dash.top_entity_types.map((e) => (
              <li key={e.entity_type} className="flex items-center gap-3 text-sm">
                <span dir="ltr" className="w-32 truncate font-mono">{e.entity_type}</span>
                <div className="h-2 flex-1 overflow-hidden rounded bg-muted">
                  <div
                    className="h-full bg-primary"
                    style={{
                      width: `${
                        (e.count / Math.max(1, ...dash.top_entity_types.map((x) => x.count))) * 100
                      }%`,
                    }}
                  />
                </div>
                <span className="w-16 text-end tabular-nums text-muted-foreground">{e.count}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t('adm.comp2.open_findings')}>
        <div className="mb-3 flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">{t('adm.comp2.filter')}</span>
          <FilterPill active={riskFilter === ''} onClick={() => setRiskFilter('')}>
            {t('adm.comp2.all')}
          </FilterPill>
          {RISK_LEVELS.map((r) => (
            <FilterPill key={r} active={riskFilter === r} onClick={() => { setRiskFilter(r); setPage(0) }}>
              {t(`adm.comp2.r_${r}`, { defaultValue: r })}
            </FilterPill>
          ))}
        </div>
        {findingsLoading ? (
          <div className="text-sm text-muted-foreground">{t('adm.comp2.loading')}</div>
        ) : (findings?.findings ?? []).length === 0 ? (
          <div className="text-sm text-muted-foreground">{t('adm.comp2.no_matching')}</div>
        ) : (
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-xs uppercase text-muted-foreground">
              <tr>
                <th className="py-2 text-start">{t('adm.comp2.col_type')}</th>
                <th className="py-2 text-start">{t('adm.comp2.col_risk')}</th>
                <th className="py-2 text-start">{t('adm.comp2.col_source')}</th>
                <th className="py-2 text-start">{t('adm.comp2.col_document')}</th>
                <th className="py-2 text-end">{t('adm.comp2.col_count')}</th>
                <th className="py-2 text-start">{t('adm.comp2.col_created')}</th>
              </tr>
            </thead>
            <tbody>
              {(findings?.findings ?? []).map((f) => (
                <tr key={f.id} className="border-t border-border">
                  <td dir="ltr" className="py-2 font-mono">{f.entity_type}</td>
                  <td className="py-2">
                    <Badge variant={f.risk_level === 'critical' ? 'disposed' : 'in_review'}>
                      {t(`adm.comp2.r_${f.risk_level}`, { defaultValue: f.risk_level })}
                    </Badge>
                  </td>
                  <td className="py-2">
                    <Badge variant="outline" className="text-[10px]">
                      {t(`adm.comp2.src_${f.detection_source}`, { defaultValue: f.detection_source })}
                    </Badge>
                  </td>
                  <td dir="ltr" className="py-2 font-mono text-xs text-muted-foreground">{f.document_id.slice(0, 8)}…</td>
                  <td className="py-2 text-end tabular-nums">{f.occurrence_count}</td>
                  <td className="py-2 text-muted-foreground">{formatDateTime(f.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
        {(findings?.total ?? 0) > PAGE && (
          <div className="mt-3 flex items-center justify-between text-xs">
            <span className="text-muted-foreground">
              {t('adm.comp2.range', {
                from: page * PAGE + 1,
                to: Math.min((page + 1) * PAGE, findings?.total ?? 0),
                total: findings?.total ?? 0,
              })}
            </span>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={page === 0}
                onClick={() => setPage((p) => Math.max(0, p - 1))}
              >
                {t('adm.comp2.previous')}
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={(page + 1) * PAGE >= (findings?.total ?? 0)}
                onClick={() => setPage((p) => p + 1)}
              >
                {t('adm.comp2.next')}
              </Button>
            </div>
          </div>
        )}
      </Section>
    </div>
  )
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg bg-card p-4 shadow-sm">
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value.toLocaleString()}</div>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mt-8">
      <h2 className="mb-3 text-sm font-medium">{title}</h2>
      <div className="rounded-lg bg-card p-4 shadow-sm">{children}</div>
    </div>
  )
}

function FilterPill({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      className={[
        'rounded-full px-3 py-1 text-xs capitalize',
        active ? 'bg-primary text-primary-foreground' : 'bg-muted',
      ].join(' ')}
    >
      {children}
    </button>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/pii-scanning?tab=findings). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/intelligence/compliance')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/pii-scanning', search: { tab: 'findings' }, replace: true })
  },
})
