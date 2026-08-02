import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Shield, AlertTriangle, CheckCircle2, Clock } from 'lucide-react'

import { getLicense, getSeatUsage, type LicenseResponse, type LicenseStatus } from '@/api/license'
import { PageHeader } from '@/components/shared/PageHeader'
import { Spinner } from '@/components/ui/Spinner'

// /admin/tenant/license — license-state surface (ADR 0095).
// Today: always `unlicensed_dev_mode` (no JWT validator wired).
// Future: same component renders real claims when enforcement ships.

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/subscription?tab=license). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/tenant/license')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/subscription', search: { tab: 'license' }, replace: true })
  },
})

export function LicensePage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['tenant-license'],
    queryFn: getLicense,
    refetchInterval: 60_000,
  })
  // seats_used lives in the auth service (owner of the users table), not
  // in the license JWT, so it's a separate fetch merged into the summary.
  const seats = useQuery({
    queryKey: ['tenant-seat-usage'],
    queryFn: getSeatUsage,
    refetchInterval: 60_000,
  })

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="Tenant license"
        description="Signed JWT claims, seat usage, feature flags, and expiry."
      />

      {isLoading && <Spinner />}
      {error && (
        <div className="rounded-lg border border-red-500/40 bg-red-50 p-4 text-sm dark:bg-red-950/30">
          Failed to load license: {(error as Error).message}
        </div>
      )}

      {data && (
        <>
          <StatusBanner data={data} />
          {data.status === 'unlicensed_dev_mode' ? (
            <UnlicensedDevPlaceholder data={data} seatsUsed={seats.data?.seats_used} />
          ) : (
            <LicensedSummary data={data} seatsUsed={seats.data?.seats_used} />
          )}
        </>
      )}
    </div>
  )
}

function StatusBanner({ data }: { data: LicenseResponse }) {
  const config = bannerConfig(data.status, data.days_remaining)
  return (
    <section className={`mb-6 rounded-lg border p-4 ${config.cls}`}>
      <div className="flex items-start gap-3">
        <config.Icon className="mt-0.5 h-5 w-5 shrink-0" />
        <div className="flex-1">
          <p className="font-semibold">{config.title}</p>
          <p className="mt-1 text-sm">{data.enforcement_message}</p>
          <p className="mt-2 text-xs text-muted-foreground">
            ADR{' '}
            <a href="/docs/adr/0095-license-enforcement" className="underline">
              {data.adr}
            </a>{' '}
            covers the multi-phase plan to ship the JWT signer, validator middleware, feature-flag
            gates, and grace-period state machine.
          </p>
        </div>
      </div>
    </section>
  )
}

function bannerConfig(status: LicenseStatus, daysRemaining?: number) {
  switch (status) {
    case 'unlicensed_dev_mode':
      return {
        Icon: AlertTriangle,
        cls: 'border-amber-500/40 bg-amber-50/60 dark:bg-amber-950/20',
        title: 'Unlicensed (dev mode)',
      }
    case 'expired':
      return {
        Icon: AlertTriangle,
        cls: 'border-red-500/40 bg-red-50/60 dark:bg-red-950/20',
        title: 'License expired (read-only mode)',
      }
    case 'grace':
      return {
        Icon: Clock,
        cls: 'border-amber-500/40 bg-amber-50/60 dark:bg-amber-950/20',
        title: `License in grace period — ${Math.abs(daysRemaining ?? 0)} days into grace`,
      }
    case 'active':
    default:
      return {
        Icon: CheckCircle2,
        cls: 'border-emerald-500/40 bg-emerald-50/60 dark:bg-emerald-950/20',
        title: 'License active',
      }
  }
}

function UnlicensedDevPlaceholder({ data, seatsUsed }: { data: LicenseResponse; seatsUsed?: number }) {
  // Today's response carries no real claims. Render the future shape
  // with `—` placeholders so the page structure is visible. Seats used
  // is real even without a license — it comes from the auth service.
  const placeholders = [
    { label: 'Tenant name', value: '—' },
    { label: 'Seat limit', value: '—' },
    { label: 'Seats used', value: seatsUsed != null ? String(seatsUsed) : '—' },
    { label: 'Expiry', value: '—' },
    { label: 'Issued to', value: '—' },
    { label: 'Issued by', value: '—' },
    { label: 'Grace period', value: `${data.grace_days} days (future)` },
  ]
  return (
    <>
      <section className="mb-6">
        <h2 className="mb-3 text-lg font-semibold">Claims</h2>
        <div className="rounded-lg border border-border bg-card">
          <dl className="divide-y divide-border">
            {placeholders.map((p) => (
              <div key={p.label} className="flex items-center justify-between px-4 py-3 text-sm">
                <dt className="text-muted-foreground">{p.label}</dt>
                <dd className="font-mono">{p.value}</dd>
              </div>
            ))}
          </dl>
        </div>
      </section>

      <section>
        <h2 className="mb-3 text-lg font-semibold">Feature flags (future)</h2>
        <div className="rounded-lg border border-border bg-card p-4 text-sm text-muted-foreground">
          <p>
            When enforcement ships, this section will show per-feature toggles (eSign, MCP, iPaaS,
            Intel LLM, allowed connectors, allowed regions) derived from the JWT.
          </p>
          <p className="mt-2">
            Today every feature is implicitly enabled because no validator runs.
          </p>
        </div>
      </section>
    </>
  )
}

function LicensedSummary({ data, seatsUsed }: { data: LicenseResponse; seatsUsed?: number }) {
  // Renders when status is active / grace / expired. seats_used comes
  // from the auth service's live count (falling back to the license
  // payload, then an em dash while the count is still loading).
  const used = seatsUsed ?? data.seats_used
  return (
    <>
      <section className="mb-6">
        <h2 className="mb-3 text-lg font-semibold">Claims</h2>
        <div className="rounded-lg border border-border bg-card">
          <dl className="divide-y divide-border">
            <Row label="Tenant name" value={data.tenant_name ?? '—'} />
            <Row label="Seat usage" value={`${used ?? '—'} / ${data.seat_limit ?? 0}`} />
            <Row label="Expiry" value={data.expiry ?? '—'} />
            <Row
              label="Days remaining"
              value={data.days_remaining != null ? String(data.days_remaining) : '—'}
            />
            <Row label="Issued to" value={data.issued_to ?? '—'} />
            <Row label="Issued by" value={data.issued_by ?? '—'} />
            <Row label="Grace period" value={`${data.grace_days} days`} />
          </dl>
        </div>
      </section>

      <section>
        <h2 className="mb-3 text-lg font-semibold">Feature flags</h2>
        <div className="rounded-lg border border-border bg-card">
          <dl className="divide-y divide-border">
            <Flag label="eSignature" enabled={data.feature_flags?.esign} />
            <Flag label="MCP server" enabled={data.feature_flags?.mcp} />
            <Flag label="iPaaS triggers" enabled={data.feature_flags?.ipaas} />
            <Flag label="Intelligence LLM" enabled={data.feature_flags?.intel_llm} />
            <Row
              label="Allowed connectors"
              value={(data.feature_flags?.connectors ?? []).join(', ') || '—'}
            />
            <Row
              label="Allowed regions"
              value={(data.feature_flags?.regions ?? []).join(', ') || '—'}
            />
          </dl>
        </div>
      </section>
    </>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between px-4 py-3 text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="font-mono">{value}</dd>
    </div>
  )
}

function Flag({ label, enabled }: { label: string; enabled?: boolean }) {
  return (
    <div className="flex items-center justify-between px-4 py-3 text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs ${
          enabled
            ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200'
            : 'bg-muted text-muted-foreground'
        }`}
      >
        <Shield className="h-3 w-3" />
        {enabled ? 'Enabled' : 'Disabled'}
      </dd>
    </div>
  )
}
