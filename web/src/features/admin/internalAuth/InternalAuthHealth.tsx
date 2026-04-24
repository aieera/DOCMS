// Internal-auth health grid. One card per Go service, sourced from
// Prometheus via the workflow service's /api/v1/platform/metrics/query
// passthrough. No direct browser-to-Prometheus traffic.
//
// Data is fetched in three parallel queries, each a single PromQL:
//   - success counts by service (last 24h)
//   - rejected counts by service (last 24h)
//   - the most recent cert-expiry timestamp per service (if known)
//
// Service "mode" is read from internal_auth_mode_info{mode="..."}. A
// service without a series reports mode="unknown" — which typically
// means VAULTDMS_INTERNAL_AUTH_MODE is unset (rollout opt-in) and the
// operator hasn't flipped it yet.

import { useQuery } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { RefreshCw, ShieldCheck, ShieldAlert, ShieldQuestion } from 'lucide-react'

import { queryPrometheus, type PromVectorSample } from '@/api/platform'
import { internalAuthMessages as M } from '@/i18n/messages/internalAuth'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Badge } from '@/components/ui/Badge'

// The 12 Go services the panel tracks. Kept in sync with the scrape
// config in ops/prometheus/prometheus.yml (one job per service).
const SERVICES = [
  'auth',
  'policy',
  'document',
  'storage',
  'search',
  'audit',
  'workflow',
  'notification',
  'signature',
  'billing',
  'connector',
  'acknowledgement',
] as const

type ServiceName = (typeof SERVICES)[number]

// PromQL — all aggregate by `job` (the scrape label set per service).
// outcome="ok" is the success case; everything else is rejected.
const Q_SUCCESS =
  'sum by (job) (increase(internal_auth_total{outcome="ok"}[24h]))'
const Q_REJECTED =
  'sum by (job) (increase(internal_auth_total{outcome!="ok"}[24h]))'
const Q_MODE = 'internal_auth_mode_info'
const Q_CERT_EXPIRY =
  'min by (job) (internal_auth_cert_expiry_timestamp_seconds)'

interface ServiceHealth {
  name: ServiceName
  mode: string
  success24h: number
  rejected24h: number
  certExpiryUnix: number | null
}

function toMap(samples: PromVectorSample[] | undefined) {
  const out = new Map<string, string>()
  if (!samples) return out
  for (const s of samples) {
    const job = s.metric.job
    if (!job) continue
    out.set(job, s.value[1])
  }
  return out
}

function modeFromSamples(samples: PromVectorSample[] | undefined) {
  // internal_auth_mode_info is keyed by (job, mode). Pick the mode
  // label whose sample value is 1.
  const out = new Map<string, string>()
  if (!samples) return out
  for (const s of samples) {
    if (s.value[1] !== '1') continue
    const job = s.metric.job
    const mode = s.metric.mode
    if (job && mode) out.set(job, mode)
  }
  return out
}

// certTone maps "days until expiry" to a visual tone per spec:
//   red  <7d
//   amber <30d
//   green >=30d
function certTone(daysUntil: number | null): 'red' | 'amber' | 'green' | 'neutral' {
  if (daysUntil == null) return 'neutral'
  if (daysUntil < 7) return 'red'
  if (daysUntil < 30) return 'amber'
  return 'green'
}

function toneClass(tone: ReturnType<typeof certTone>) {
  switch (tone) {
    case 'red':
      return 'bg-red-100 text-red-900 dark:bg-red-900/40 dark:text-red-100'
    case 'amber':
      return 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100'
    case 'green':
      return 'bg-green-100 text-green-900 dark:bg-green-900/40 dark:text-green-100'
    default:
      return 'bg-[var(--color-bg-secondary)] text-[var(--color-text-secondary)]'
  }
}

export function InternalAuthHealth() {
  const successQ = useQuery({
    queryKey: ['internalauth-success-24h'],
    queryFn: () => queryPrometheus(Q_SUCCESS),
  })
  const rejectedQ = useQuery({
    queryKey: ['internalauth-rejected-24h'],
    queryFn: () => queryPrometheus(Q_REJECTED),
  })
  const modeQ = useQuery({
    queryKey: ['internalauth-mode'],
    queryFn: () => queryPrometheus(Q_MODE),
  })
  const certQ = useQuery({
    queryKey: ['internalauth-cert-expiry'],
    queryFn: () => queryPrometheus(Q_CERT_EXPIRY),
  })

  const allLoading =
    successQ.isLoading || rejectedQ.isLoading || modeQ.isLoading || certQ.isLoading
  const anyError =
    successQ.isError || rejectedQ.isError || modeQ.isError || certQ.isError
  const upstreamUnconfigured =
    successQ.data?.status === 'error' &&
    (successQ.data.error ?? '').toLowerCase().includes('prometheus not configured')

  const success = toMap(successQ.data?.data?.result)
  const rejected = toMap(rejectedQ.data?.data?.result)
  const modes = modeFromSamples(modeQ.data?.data?.result)
  const expiry = toMap(certQ.data?.data?.result)

  const rows: ServiceHealth[] = SERVICES.map((name) => ({
    name,
    mode: modes.get(name) ?? M.modeUnknown,
    success24h: Math.round(parseFloat(success.get(name) ?? '0')),
    rejected24h: Math.round(parseFloat(rejected.get(name) ?? '0')),
    certExpiryUnix: expiry.has(name) ? parseFloat(expiry.get(name)!) : null,
  }))

  const onRefresh = () => {
    Promise.all([
      successQ.refetch(),
      rejectedQ.refetch(),
      modeQ.refetch(),
      certQ.refetch(),
    ])
      .then(() => toast.success(M.healthRefreshed))
      .catch(() => toast.error(M.healthError))
  }

  return (
    <section
      aria-labelledby="internal-auth-health-title"
      data-testid="internal-auth-health"
    >
      <header className="mb-4 flex items-center justify-between">
        <h2 id="internal-auth-health-title" className="text-sm font-semibold">
          {M.servicesCardTitle}
        </h2>
        <Button
          variant="ghost"
          size="sm"
          onClick={onRefresh}
          aria-label="Refresh health"
          data-testid="internal-auth-refresh"
        >
          <RefreshCw className="mr-1 h-4 w-4" aria-hidden="true" />
          Refresh
        </Button>
      </header>

      {anyError && !upstreamUnconfigured ? (
        <div
          role="alert"
          className="mb-4 rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-100"
        >
          {M.healthError}
        </div>
      ) : null}

      {upstreamUnconfigured ? (
        <div
          role="status"
          className="mb-4 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-100"
        >
          {M.metricsBackendUnconfigured}
        </div>
      ) : null}

      {allLoading ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {SERVICES.map((s) => (
            <Skeleton key={s} className="h-36 w-full" />
          ))}
        </div>
      ) : (
        <ul
          className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
          data-testid="internal-auth-service-grid"
        >
          {rows.map((r) => {
            const daysUntil =
              r.certExpiryUnix != null
                ? Math.round((r.certExpiryUnix * 1000 - Date.now()) / 86400000)
                : null
            const tone = certTone(daysUntil)
            const icon =
              tone === 'red' ? (
                <ShieldAlert className="h-4 w-4" aria-hidden="true" />
              ) : tone === 'neutral' ? (
                <ShieldQuestion className="h-4 w-4" aria-hidden="true" />
              ) : (
                <ShieldCheck className="h-4 w-4" aria-hidden="true" />
              )
            return (
              <li
                key={r.name}
                className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
                data-testid={`service-card-${r.name}`}
              >
                <div className="flex items-start justify-between">
                  <div>
                    <h3 className="text-sm font-semibold">{r.name}</h3>
                    <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
                      {M.modeLabel}:{' '}
                      <Badge variant="outline" className="align-middle">
                        {r.mode}
                      </Badge>
                    </p>
                  </div>
                  <div
                    className={`flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium ${toneClass(tone)}`}
                    aria-label={
                      daysUntil != null
                        ? `${M.certExpiryLabel} ${daysUntil}${M.certDaysSuffix}`
                        : M.certExpiryUnknown
                    }
                  >
                    {icon}
                    <span>
                      {daysUntil != null
                        ? `${daysUntil}${M.certDaysSuffix}`
                        : M.certExpiryUnknown}
                    </span>
                  </div>
                </div>
                <dl className="mt-3 grid grid-cols-2 gap-2 text-sm">
                  <div>
                    <dt className="text-xs text-[var(--color-text-secondary)]">
                      {M.successCountLabel}
                    </dt>
                    <dd className="font-mono" data-testid={`success-${r.name}`}>
                      {r.success24h.toLocaleString()}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-xs text-[var(--color-text-secondary)]">
                      {M.rejectedCountLabel}
                    </dt>
                    <dd
                      className={`font-mono ${r.rejected24h > 0 ? 'text-red-700 dark:text-red-300' : ''}`}
                      data-testid={`rejected-${r.name}`}
                    >
                      {r.rejected24h.toLocaleString()}
                    </dd>
                  </div>
                </dl>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
