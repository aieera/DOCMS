import { Globe, Shield } from 'lucide-react'
import { cn } from '@/lib/cn'

// Region → geopolitical boundary mirror. Keep in sync with
// pkg/regionenforcer/boundaries.go and the supported_regions seed in
// migration 000014. A mismatch between this list and the backend is a
// silent UX bug: we'd render the wrong legal-framework tooltip on a
// document the backend has correctly pinned.
const BOUNDARY: Record<string, 'EU' | 'US' | 'MENA' | 'APAC' | 'OTHER'> = {
  'us-east-1': 'US',
  'us-west-2': 'US',
  'eu-west-1': 'EU',
  'eu-central-1': 'EU',
  'me-south-1': 'MENA',
  'ap-southeast-1': 'APAC',
  'ap-northeast-1': 'APAC',
  custom: 'OTHER',
}

const BOUNDARY_COPY: Record<'EU' | 'US' | 'MENA' | 'APAC' | 'OTHER', { label: string; rule: string; tone: string }> = {
  EU:    { label: 'EU',    rule: 'EU data residency (GDPR Article 44–49)', tone: 'bg-sky-100 text-sky-900 ring-sky-300 dark:bg-sky-950/50 dark:text-sky-200' },
  US:    { label: 'US',    rule: 'US data residency (no cross-boundary replication)', tone: 'bg-indigo-100 text-indigo-900 ring-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-200' },
  MENA:  { label: 'MENA',  rule: 'Middle East data residency (UAE / KSA PDPL)', tone: 'bg-amber-100 text-amber-900 ring-amber-300 dark:bg-amber-950/50 dark:text-amber-200' },
  APAC:  { label: 'APAC',  rule: 'APAC data residency (Singapore PDPA / Japan APPI)', tone: 'bg-emerald-100 text-emerald-900 ring-emerald-300 dark:bg-emerald-950/50 dark:text-emerald-200' },
  OTHER: { label: 'Custom',rule: 'Custom residency contract', tone: 'bg-slate-200 text-slate-800 ring-slate-400 dark:bg-slate-700 dark:text-slate-200' },
}

interface Props {
  region: string
  size?: 'sm' | 'md'
  className?: string
}

export function RegionPinBadge({ region, size = 'md', className }: Props) {
  const boundary = BOUNDARY[region] ?? 'OTHER'
  const copy = BOUNDARY_COPY[boundary]
  const Icon = boundary === 'OTHER' ? Globe : Shield
  return (
    <span
      data-testid={`region-pin-badge-${region}`}
      data-region={region}
      data-boundary={boundary}
      title={`Stored in ${region} — governed by ${copy.rule}.`}
      className={cn(
        'inline-flex items-center gap-1 rounded-full ring-1 font-medium',
        size === 'sm' ? 'px-1.5 py-0.5 text-[10px]' : 'px-2 py-0.5 text-xs',
        copy.tone,
        className,
      )}
    >
      <Icon className={size === 'sm' ? 'h-2.5 w-2.5' : 'h-3 w-3'} aria-hidden="true" />
      <span className="font-mono tabular-nums">{region}</span>
      <span className="opacity-70">· {copy.label}</span>
    </span>
  )
}
