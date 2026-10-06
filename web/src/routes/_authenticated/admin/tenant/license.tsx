import { createFileRoute, redirect } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
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
  const { t } = useTranslation('common')
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
        title={t('adm.lic.title')}
        description={t('adm.lic.description')}
      />

      {isLoading && <Spinner />}
      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm">
          {t('adm.lic.err_load', { error: (error as Error).message })}
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
  const { t } = useTranslation('common')
  const config = bannerConfig(data.status, data.days_remaining, t)
  return (
    <section className={`mb-6 rounded-lg border p-4 ${config.cls}`}>
      <div className="flex items-start gap-3">
        <config.Icon className="mt-0.5 h-5 w-5 shrink-0" />
        <div className="flex-1">
          <p className="font-semibold">{config.title}</p>
          <p className="mt-1 text-sm">{data.enforcement_message}</p>
          {/* Pre-sale item 04: this used to cite ADR 0095 and describe the
              enforcement engineering plan — internal references that told a
              customer our licence enforcement is a work in progress. The
              banner already says everything a tenant admin needs (status,
              enforcement message, days remaining). */}
        </div>
      </div>
    </section>
  )
}

function bannerConfig(status: LicenseStatus, daysRemaining: number | undefined, t: TFunction<'common'>) {
  switch (status) {
    case 'unlicensed_dev_mode':
      return {
        Icon: AlertTriangle,
        cls: 'border-warning/40 bg-warning/10',
        title: t('adm.lic.st_unlicensed'),
      }
    case 'expired':
      return {
        Icon: AlertTriangle,
        cls: 'border-destructive/40 bg-destructive/5',
        title: t('adm.lic.st_expired'),
      }
    case 'grace':
      return {
        Icon: Clock,
        cls: 'border-warning/40 bg-warning/10',
        title: t('adm.lic.st_grace', { count: Math.abs(daysRemaining ?? 0) }),
      }
    case 'active':
    default:
      return {
        Icon: CheckCircle2,
        cls: 'border-success/40 bg-success/10',
        title: t('adm.lic.st_active'),
      }
  }
}

function UnlicensedDevPlaceholder({ data, seatsUsed }: { data: LicenseResponse; seatsUsed?: number }) {
  const { t } = useTranslation('common')
  // Today's response carries no real claims. Render the future shape
  // with `—` placeholders so the page structure is visible. Seats used
  // is real even without a license — it comes from the auth service.
  const placeholders = [
    { label: t('adm.lic.tenant_name'), value: '—' },
    { label: t('adm.lic.seat_limit'), value: '—' },
    { label: t('adm.lic.seats_used'), value: seatsUsed != null ? String(seatsUsed) : '—' },
    { label: t('adm.lic.expiry'), value: '—' },
    { label: t('adm.lic.issued_to'), value: '—' },
    { label: t('adm.lic.issued_by'), value: '—' },
    { label: t('adm.lic.grace'), value: t('adm.lic.grace_days_future', { count: data.grace_days }) },
  ]
  return (
    <>
      <section className="mb-6">
        <h2 className="mb-3 text-lg font-semibold">{t('adm.lic.claims')}</h2>
        <div className="rounded-lg bg-card shadow-neu">
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
        <h2 className="mb-3 text-lg font-semibold">{t('adm.lic.flags_future')}</h2>
        <div className="rounded-lg bg-card p-4 text-sm text-muted-foreground shadow-neu">
          <p>{t('adm.lic.future_body')}</p>
          <p className="mt-2">{t('adm.lic.future_note')}</p>
        </div>
      </section>
    </>
  )
}

function LicensedSummary({ data, seatsUsed }: { data: LicenseResponse; seatsUsed?: number }) {
  const { t } = useTranslation('common')
  // Renders when status is active / grace / expired. seats_used comes
  // from the auth service's live count (falling back to the license
  // payload, then an em dash while the count is still loading).
  const used = seatsUsed ?? data.seats_used
  return (
    <>
      <section className="mb-6">
        <h2 className="mb-3 text-lg font-semibold">{t('adm.lic.claims')}</h2>
        <div className="rounded-lg bg-card shadow-neu">
          <dl className="divide-y divide-border">
            <Row label={t('adm.lic.tenant_name')} value={data.tenant_name ?? '—'} />
            <Row
              label={t('adm.lic.seat_usage')}
              value={t('adm.lic.seat_usage_value', { used: used ?? '—', limit: data.seat_limit ?? 0 })}
            />
            <Row label={t('adm.lic.expiry')} value={data.expiry ?? '—'} />
            <Row
              label={t('adm.lic.days_remaining')}
              value={data.days_remaining != null ? String(data.days_remaining) : '—'}
            />
            <Row label={t('adm.lic.issued_to')} value={data.issued_to ?? '—'} />
            <Row label={t('adm.lic.issued_by')} value={data.issued_by ?? '—'} />
            <Row label={t('adm.lic.grace')} value={t('adm.lic.grace_days', { count: data.grace_days })} />
          </dl>
        </div>
      </section>

      <section>
        <h2 className="mb-3 text-lg font-semibold">{t('adm.lic.flags')}</h2>
        <div className="rounded-lg bg-card shadow-neu">
          <dl className="divide-y divide-border">
            <Flag label={t('adm.lic.f_esign')} enabled={data.feature_flags?.esign} />
            <Flag label={t('adm.lic.f_mcp')} enabled={data.feature_flags?.mcp} />
            <Flag label={t('adm.lic.f_ipaas')} enabled={data.feature_flags?.ipaas} />
            <Flag label={t('adm.lic.f_intel')} enabled={data.feature_flags?.intel_llm} />
            <Row
              label={t('adm.lic.f_connectors')}
              value={(data.feature_flags?.connectors ?? []).join(', ') || '—'}
            />
            <Row
              label={t('adm.lic.f_regions')}
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
  const { t } = useTranslation('common')
  return (
    <div className="flex items-center justify-between px-4 py-3 text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs ${
          enabled
            ? 'bg-success/15 text-foreground'
            : 'bg-muted text-muted-foreground'
        }`}
      >
        <Shield className="h-3 w-3" />
        {enabled ? t('adm.lic.enabled') : t('adm.lic.disabled')}
      </dd>
    </div>
  )
}
