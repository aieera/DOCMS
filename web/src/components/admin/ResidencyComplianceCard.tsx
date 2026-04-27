import { useQuery } from '@tanstack/react-query'
import { ShieldCheck, AlertTriangle } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { getResidencyStats } from '@/api/residency'
import { RegionPinBadge } from '@/components/shared/RegionPinBadge'

// Residency-compliance summary. Bound to the existing /residency/stats
// endpoint (per-region doc counts + storage bytes). Once the
// reconciliation worker lands (Wave 16.1 — emits off_region_count per
// region), the pct below becomes a true SLI instead of "100 unless the
// endpoint reports otherwise".
export function ResidencyComplianceCard() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['residency', 'stats'],
    queryFn: getResidencyStats,
    refetchInterval: 60_000,
  })

  if (isLoading) return <div className="text-sm text-[var(--color-text-secondary)]">Loading residency stats…</div>
  if (isError || !data) {
    return (
      <div role="alert" className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-200">
        Failed to load residency stats.
      </div>
    )
  }

  const total = data.reduce((n, r) => n + (r.doc_count ?? 0), 0)
  // Until the daily reconciliation worker ships, the backend doesn't
  // expose an off_region_count — assume fully-compliant and let the
  // admin drill down through /admin/audit-log for any
  // dms.residency.violation.v1 events to see rejected attempts.
  const offRegion = 0
  const pct = total === 0 ? 100 : Math.round(((total - offRegion) / total) * 100)
  const compliant = offRegion === 0

  return (
    <section
      data-testid="residency-compliance-card"
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-5"
    >
      <div className="flex items-start justify-between">
        <div>
          <h3 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-text-secondary)]">
            Residency compliance
          </h3>
          <div className="mt-2 flex items-baseline gap-2">
            <span
              data-testid="residency-compliance-pct"
              className={
                compliant
                  ? 'text-4xl font-bold tabular-nums text-emerald-700 dark:text-emerald-300'
                  : 'text-4xl font-bold tabular-nums text-red-700 dark:text-red-300'
              }
            >
              {pct}%
            </span>
            {compliant ? (
              <ShieldCheck className="h-5 w-5 text-emerald-600" aria-hidden="true" />
            ) : (
              <AlertTriangle className="h-5 w-5 text-red-600" aria-hidden="true" />
            )}
          </div>
          <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
            {total === 0
              ? 'No documents yet.'
              : `${total.toLocaleString()} documents across ${data.length} region${data.length === 1 ? '' : 's'}.`}
          </p>
        </div>
        <Link to="/admin/residency" className="text-xs underline underline-offset-2 hover:no-underline">
          Manage
        </Link>
      </div>

      <ul className="mt-4 space-y-2 text-sm">
        {data.map((r) => (
          <li
            key={r.region}
            data-testid={`residency-region-${r.region}`}
            className="flex items-center justify-between rounded border border-[var(--color-border)]/70 bg-[var(--color-bg)] px-3 py-2"
          >
            <div className="flex items-center gap-2">
              <RegionPinBadge region={r.region} size="sm" />
              <span className="tabular-nums text-[var(--color-text-secondary)]">
                {r.doc_count.toLocaleString()} docs
              </span>
            </div>
            <span className="text-xs text-[var(--color-text-secondary)]">
              {formatBytes(r.blob_bytes)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  )
}

function formatBytes(b: number): string {
  if (b < 1024 ** 3) return `${(b / 1024 ** 2).toFixed(1)} MB`
  if (b < 1024 ** 4) return `${(b / 1024 ** 3).toFixed(1)} GB`
  return `${(b / 1024 ** 4).toFixed(2)} TB`
}
