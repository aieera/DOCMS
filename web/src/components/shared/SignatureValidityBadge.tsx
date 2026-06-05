// ADR 0072 — PAdES-LTV validity badge.
//
// Renders a compact summary of the Tier-1 verifier's verdict +
// (when supplied) the Tier-2 EU DSS result. "Tier-2" is opt-in:
// it doesn't fire from the in-app UX (privacy + rate-limit
// concerns), but the badge accepts a tier2 prop so an admin tool
// can render the joint state.
//
// Re-validate runs validatePDF against the current version's
// content blob and refreshes the badge in place.
import { useState } from 'react'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { CheckCircle2, AlertTriangle, ShieldQuestion, RotateCw, ShieldOff } from 'lucide-react'

import { validatePDF, type PAdESReport, type CertStatus } from '@/api/signatures'
import { Button } from '@/components/ui/shadcn/button'

interface BadgeProps {
  documentId: string
  // tier1 is the latest cached report (server-provided on initial
  // page load). null/undefined → renders an "unverified" pill with a
  // Re-validate CTA.
  tier1?: PAdESReport | null
  // tier2 is the optional EU DSS result. The frontend never calls
  // the EU validator itself — admins paste a result from the
  // nightly workflow when they want to surface it.
  tier2?: { passed: boolean; ranAt: string } | null
}

export function SignatureValidityBadge({ documentId, tier1, tier2 }: BadgeProps) {
  const [report, setReport] = useState<PAdESReport | null>(tier1 ?? null)
  const revalidate = useAppMutation({
    mutationFn: async () => {
      const res = await fetch(`/api/v1/documents/${documentId}/content`, { credentials: 'include' })
      if (!res.ok) throw new Error('could not fetch document bytes')
      const blob = await res.blob()
      return validatePDF(blob)
    },
    onSuccess: (r) => {
      setReport(r)
      toast.success('Signature re-validated')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const tone = badgeTone(report)
  const Icon = tone.icon

  return (
    <div
      className={`inline-flex items-center gap-2 rounded-md border px-2 py-1 text-xs ${tone.cls}`}
      data-testid="signature-validity-badge"
      data-tone={tone.id}
    >
      <Icon className="h-3.5 w-3.5" />
      <span>{summarize(report)}</span>
      {report?.ltv_enabled && (
        <span className="rounded-full bg-white/40 px-1.5 py-0.5 font-mono text-[10px] dark:bg-black/30" data-testid="ltv-age">
          LTV {ltvAgeLabel(report.ltv_age)}
        </span>
      )}
      {tier2 && (
        <span
          className={`rounded-full px-1.5 py-0.5 text-[10px] ${
            tier2.passed ? 'bg-emerald-100 text-emerald-800' : 'bg-red-100 text-red-800'
          }`}
          data-testid="tier2-pill"
        >
          Tier-2 {tier2.passed ? '✓' : '✗'}
        </span>
      )}
      <Button
        size="sm"
        variant="ghost"
        className="ms-1 h-6 px-1.5"
        onClick={() => revalidate.mutate()}
        loading={revalidate.isPending}
        title="Re-validate"
        data-testid="signature-revalidate"
      >
        <RotateCw className="h-3 w-3" />
      </Button>
    </div>
  )
}

interface Tone {
  id: 'unsigned' | 'valid' | 'indeterminate' | 'invalid' | 'unknown'
  cls: string
  icon: typeof CheckCircle2
}

function badgeTone(r: PAdESReport | null): Tone {
  if (!r || r.signature_count === 0) {
    return { id: 'unsigned', cls: 'border-slate-300 bg-slate-50 text-slate-600 dark:bg-slate-800 dark:text-slate-300', icon: ShieldQuestion }
  }
  if (!r.tamper_evident) {
    return { id: 'invalid', cls: 'border-red-300 bg-red-50 text-red-800 dark:bg-red-900/20 dark:text-red-200', icon: ShieldOff }
  }
  // Aggregate cert status across all signatures: any revoked → invalid;
  // any indeterminate → indeterminate; all valid → valid.
  const worst = aggregateStatus(r.signatures.map((s) => s.cert_status))
  switch (worst) {
    case 'revoked':
      return { id: 'invalid', cls: 'border-red-300 bg-red-50 text-red-800 dark:bg-red-900/20 dark:text-red-200', icon: ShieldOff }
    case 'indeterminate':
      return { id: 'indeterminate', cls: 'border-amber-300 bg-amber-50 text-amber-900 dark:bg-amber-900/20 dark:text-amber-200', icon: AlertTriangle }
    case 'valid':
      return { id: 'valid', cls: 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200', icon: CheckCircle2 }
    default:
      return { id: 'unknown', cls: 'border-slate-300 bg-slate-50 text-slate-700 dark:bg-slate-800 dark:text-slate-200', icon: ShieldQuestion }
  }
}

function aggregateStatus(items: CertStatus[]): CertStatus {
  if (items.includes('revoked')) return 'revoked'
  if (items.includes('indeterminate')) return 'indeterminate'
  if (items.includes('unknown')) return 'unknown'
  return 'valid'
}

function summarize(r: PAdESReport | null): string {
  if (!r || r.signature_count === 0) return 'Unsigned'
  if (!r.tamper_evident) return 'Tampered after signing'
  const status = aggregateStatus(r.signatures.map((s) => s.cert_status))
  const n = r.signature_count
  const sig = n === 1 ? 'signature' : 'signatures'
  switch (status) {
    case 'valid':         return `${n} valid ${sig}`
    case 'indeterminate': return `${n} ${sig} (cert expired since signing)`
    case 'revoked':       return `${n} ${sig} — revoked`
    default:              return `${n} ${sig} — unknown status`
  }
}

// ltvAgeLabel returns a human-readable "30d" / "2h" string from a
// nanosecond duration (Go time.Duration JSON shape).
function ltvAgeLabel(ns?: number): string {
  if (!ns || ns < 0) return 'fresh'
  const seconds = Math.floor(ns / 1_000_000_000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  return `${days}d`
}
