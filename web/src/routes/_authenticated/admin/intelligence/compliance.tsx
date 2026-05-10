import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import {
  getComplianceDashboard,
  listPendingFindings,
  type RiskLevel,
} from '@/api/compliance-pii'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'

const RISK_LEVELS: RiskLevel[] = ['critical', 'high', 'medium', 'low']

const RISK_COLOR: Record<string, string> = {
  critical: 'bg-destructive',
  high:     'bg-orange-500',
  medium:   'bg-warning/100',
  low:      'bg-success',
  none:     'bg-muted-foreground/40',
}

function ComplianceAdminDashboard() {
  const [riskFilter, setRiskFilter] = useState<string>('')

  const { data: dash, isLoading: dashLoading } = useQuery({
    queryKey: ['compliance-dashboard'],
    queryFn: getComplianceDashboard,
    refetchInterval: 30_000,
  })

  const { data: findings, isLoading: findingsLoading } = useQuery({
    queryKey: ['compliance-pending', riskFilter],
    queryFn: () => listPendingFindings({ risk_level: riskFilter || undefined }),
    refetchInterval: 30_000,
  })

  if (dashLoading || !dash) return <div className="p-6 text-sm text-muted-foreground">Loading…</div>

  const totalRisk = Object.values(dash.risk_distribution).reduce((a, b) => a + b, 0) || 1

  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHeader
        title="Compliance scanning"
        description="Tenant-wide PII/PHI findings produced by the intelligence pipeline (ADR 0054)."
      />

      <div className="mt-6 grid grid-cols-4 gap-4">
        <Metric label="Documents scanned" value={dash.total_documents_scanned} />
        <Metric label="With findings" value={dash.documents_with_findings} />
        <Metric label="Open findings" value={dash.open_findings} />
        <Metric label="Auto-hold recommended" value={dash.auto_held_documents} />
      </div>

      <Section title="Risk distribution">
        <div className="space-y-2">
          {Object.entries(dash.risk_distribution).map(([risk, count]) => (
            <div key={risk} className="flex items-center gap-3 text-sm">
              <span className={`h-2 w-2 rounded-full ${RISK_COLOR[risk] ?? 'bg-muted-foreground/40'}`} />
              <span className="w-24 capitalize">{risk}</span>
              <div className="h-2 flex-1 overflow-hidden rounded bg-muted">
                <div
                  className={RISK_COLOR[risk] ?? 'bg-muted-foreground/40'}
                  style={{ height: '100%', width: `${(count / totalRisk) * 100}%` }}
                />
              </div>
              <span className="w-12 text-right tabular-nums text-muted-foreground">{count}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Top entity types">
        {(dash.top_entity_types ?? []).length === 0 ? (
          <div className="text-sm text-muted-foreground">No findings yet.</div>
        ) : (
          <ul className="space-y-2">
            {dash.top_entity_types.map((e) => (
              <li key={e.entity_type} className="flex items-center gap-3 text-sm">
                <span className="w-32 truncate font-mono">{e.entity_type}</span>
                <div className="h-2 flex-1 overflow-hidden rounded bg-muted">
                  <div
                    className="h-full bg-violet-500"
                    style={{
                      width: `${
                        (e.count / Math.max(1, ...dash.top_entity_types.map((x) => x.count))) * 100
                      }%`,
                    }}
                  />
                </div>
                <span className="w-16 text-right tabular-nums text-muted-foreground">{e.count}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Open findings">
        <div className="mb-3 flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Filter:</span>
          <FilterPill active={riskFilter === ''} onClick={() => setRiskFilter('')}>
            All
          </FilterPill>
          {RISK_LEVELS.map((r) => (
            <FilterPill key={r} active={riskFilter === r} onClick={() => setRiskFilter(r)}>
              {r}
            </FilterPill>
          ))}
        </div>
        {findingsLoading ? (
          <div className="text-sm text-muted-foreground">Loading…</div>
        ) : (findings?.findings ?? []).length === 0 ? (
          <div className="text-sm text-muted-foreground">No matching findings.</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-xs uppercase text-muted-foreground">
              <tr>
                <th className="py-2">Type</th>
                <th className="py-2">Risk</th>
                <th className="py-2">Source</th>
                <th className="py-2">Document</th>
                <th className="py-2 text-right">Count</th>
                <th className="py-2">Created</th>
              </tr>
            </thead>
            <tbody>
              {(findings?.findings ?? []).map((f) => (
                <tr key={f.id} className="border-t border-border">
                  <td className="py-2 font-mono">{f.entity_type}</td>
                  <td className="py-2">
                    <Badge variant={f.risk_level === 'critical' ? 'disposed' : 'in_review'}>
                      {f.risk_level}
                    </Badge>
                  </td>
                  <td className="py-2">
                    <Badge variant="outline" className="text-[10px] uppercase">
                      {f.detection_source}
                    </Badge>
                  </td>
                  <td className="py-2 font-mono text-xs text-muted-foreground">{f.document_id.slice(0, 8)}…</td>
                  <td className="py-2 text-right tabular-nums">{f.occurrence_count}</td>
                  <td className="py-2 text-muted-foreground">{new Date(f.created_at).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  )
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded border border-border p-4">
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value.toLocaleString()}</div>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mt-8">
      <h2 className="mb-3 text-sm font-medium">{title}</h2>
      <div className="rounded border border-border p-4">{children}</div>
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
        active ? 'bg-violet-500 text-white' : 'bg-muted',
      ].join(' ')}
    >
      {children}
    </button>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/compliance')({
  component: ComplianceAdminDashboard,
})
