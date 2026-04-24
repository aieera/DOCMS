// Three-card posture view for /admin/platform/security. Each card
// shows the latest result of a CI gate — SAST, Dep scan, DAST —
// plus a deep-link to the GH Actions run (when known).
//
// The Secret-scan gate is deliberately NOT given its own card; per
// ADR 0033 secret scans fail on any finding (no severity axis),
// and the banner is the operator signal. Showing it as a card
// alongside findings-count cards would imply the same axis it
// doesn't have. Operators get the full grid from the endpoint
// response and can drill into secret-scan via the GH Actions link.

import { ExternalLink, ShieldCheck, ShieldAlert, ShieldQuestion } from 'lucide-react'

import { type SecurityPostureScan } from '@/api/platform'

import { useSecurityPosture } from './useSecurityPosture'
import { Skeleton } from '@/components/ui/Skeleton'

// The three gates shown as full cards. Secret scan shows up as a
// small footer row so operators aren't surprised it's missing.
const CARD_TYPES: SecurityPostureScan['scan_type'][] = ['sast', 'dep_scan', 'dast']

interface Meta {
  label: string
  description: string
}
const META: Record<SecurityPostureScan['scan_type'], Meta> = {
  sast: {
    label: 'Latest SAST scan',
    description: 'Semgrep + custom .semgrep/ rules',
  },
  dep_scan: {
    label: 'Dep scan',
    description: 'govulncheck + npm audit + Trivy',
  },
  dast: {
    label: 'DAST baseline',
    description: 'OWASP ZAP against staging',
  },
  secret_scan: {
    label: 'Secret scan',
    description: 'gitleaks (full history)',
  },
}

export function SecurityPostureCards() {
  const { data, isLoading, isError } = useSecurityPosture()

  if (isError) {
    return (
      <div
        role="alert"
        data-testid="security-posture-error"
        className="rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-100"
      >
        Failed to load security posture — check workflow service logs.
      </div>
    )
  }

  if (isLoading || !data) {
    return (
      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        <Skeleton className="h-36 w-full" />
        <Skeleton className="h-36 w-full" />
        <Skeleton className="h-36 w-full" />
      </div>
    )
  }

  const byType = new Map<SecurityPostureScan['scan_type'], SecurityPostureScan>()
  for (const s of data.scans) byType.set(s.scan_type, s)

  return (
    <section data-testid="security-posture">
      <ul
        className="grid grid-cols-1 gap-3 md:grid-cols-3"
        data-testid="security-posture-cards"
      >
        {CARD_TYPES.map((t) => (
          <ScanCard key={t} meta={META[t]} scan={byType.get(t)} />
        ))}
      </ul>

      {/* Secret-scan footer — see component docstring for why it's not a card. */}
      <SecretScanFooter scan={byType.get('secret_scan')} meta={META.secret_scan} />
    </section>
  )
}

function ScanCard({ meta, scan }: { meta: Meta; scan?: SecurityPostureScan }) {
  const tone = toneForStatus(scan?.status)
  const icon =
    scan?.status === 'pass' ? (
      <ShieldCheck className="h-4 w-4" aria-hidden="true" />
    ) : scan?.status === 'fail' ? (
      <ShieldAlert className="h-4 w-4" aria-hidden="true" />
    ) : (
      <ShieldQuestion className="h-4 w-4" aria-hidden="true" />
    )

  const testId = `security-card-${scan?.scan_type ?? 'unknown'}`
  return (
    <li
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
      data-testid={testId}
    >
      <div className="flex items-start justify-between">
        <div>
          <h3 className="text-sm font-semibold">{meta.label}</h3>
          <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
            {meta.description}
          </p>
        </div>
        <span
          className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium ${toneClass(tone)}`}
          data-testid={`${testId}-status`}
        >
          {icon}
          {scan?.status ?? 'unknown'}
        </span>
      </div>

      <dl className="mt-3 grid grid-cols-2 gap-2 text-sm">
        <div>
          <dt className="text-xs text-[var(--color-text-secondary)]">Critical</dt>
          <dd
            className={`font-mono ${scan && scan.critical_count > 0 ? 'text-red-700 dark:text-red-300' : ''}`}
            data-testid={`${testId}-critical`}
          >
            {scan?.critical_count ?? '—'}
          </dd>
        </div>
        <div>
          <dt className="text-xs text-[var(--color-text-secondary)]">High</dt>
          <dd
            className={`font-mono ${scan && scan.high_count > 0 ? 'text-amber-700 dark:text-amber-300' : ''}`}
            data-testid={`${testId}-high`}
          >
            {scan?.high_count ?? '—'}
          </dd>
        </div>
      </dl>

      <div className="mt-3 flex items-center justify-between text-xs text-[var(--color-text-secondary)]">
        <span data-testid={`${testId}-ran-at`}>
          {scan?.ran_at ? formatRelative(scan.ran_at) : 'never run'}
        </span>
        {scan?.run_url ? (
          <a
            href={scan.run_url}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 underline-offset-2 hover:underline"
            data-testid={`${testId}-link`}
          >
            GH Actions run
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
          </a>
        ) : null}
      </div>
    </li>
  )
}

function SecretScanFooter({
  meta,
  scan,
}: {
  meta: Meta
  scan?: SecurityPostureScan
}) {
  const tone = toneForStatus(scan?.status)
  return (
    <p
      className="mt-4 text-xs text-[var(--color-text-secondary)]"
      data-testid="security-footer-secret"
    >
      {meta.label}:{' '}
      <span className={`rounded px-1.5 py-0.5 font-mono ${toneClass(tone)}`}>
        {scan?.status ?? 'unknown'}
      </span>
      {scan?.run_url ? (
        <a
          href={scan.run_url}
          target="_blank"
          rel="noopener noreferrer"
          className="ms-2 underline-offset-2 hover:underline"
        >
          latest run
        </a>
      ) : null}
    </p>
  )
}

type Tone = 'red' | 'amber' | 'green' | 'neutral'

function toneForStatus(status?: ScanStatusLocal): Tone {
  if (!status || status === 'unknown') return 'neutral'
  if (status === 'fail') return 'red'
  return 'green'
}

type ScanStatusLocal = 'pass' | 'fail' | 'unknown'

function toneClass(tone: Tone) {
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

function formatRelative(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const deltaS = Math.round((Date.now() - d.getTime()) / 1000)
  if (deltaS < 60) return `${deltaS}s ago`
  const m = Math.round(deltaS / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.round(h / 24)}d ago`
}
