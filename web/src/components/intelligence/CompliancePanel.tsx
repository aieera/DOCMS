import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { ChevronDown, ChevronUp, RefreshCw, ShieldAlert } from 'lucide-react'

import {
  getDocumentCompliance,
  rescanDocument,
  reviewComplianceFinding,
  type ComplianceFinding,
  type RemediationStatus,
  type RiskLevel,
} from '@/api/compliance-pii'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/shadcn/button'

interface Props {
  documentId: string
}

const RISK_DOT: Record<string, string> = {
  critical: 'bg-red-500',
  high:     'bg-orange-500',
  medium:   'bg-amber-500',
  low:      'bg-emerald-500',
}

const STATUS_LABEL: Record<RemediationStatus, string> = {
  open:           'Open',
  acknowledged:   'Acknowledged',
  remediated:     'Remediated',
  false_positive: 'False positive',
}

export function CompliancePanel({ documentId }: Props) {
  const qc = useQueryClient()
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const { data, isLoading } = useQuery({
    queryKey: ['compliance', documentId],
    queryFn: () => getDocumentCompliance(documentId),
    refetchInterval: 30_000,
  })

  const review = useMutation({
    mutationFn: ({ id, status, note }: { id: string; status: RemediationStatus; note?: string }) =>
      reviewComplianceFinding(documentId, id, status, note),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['compliance', documentId] }),
    onError: () => toast.error('Review failed'),
  })

  const rescan = useMutation({
    mutationFn: () => rescanDocument(documentId),
    onSuccess: () => toast.success('Rescan queued'),
    onError: () => toast.error('Rescan failed'),
  })

  if (isLoading) return <div className="p-4 text-sm text-zinc-500">Loading compliance scan…</div>
  if (!data?.summary) {
    return (
      <div className="rounded border border-zinc-200 p-4 text-sm text-zinc-500 dark:border-zinc-800">
        No compliance scan yet for this document.
        <Button size="sm" variant="ghost" className="ml-2" onClick={() => rescan.mutate()}>
          Run scan
        </Button>
      </div>
    )
  }

  const s = data.summary
  const findings = data.findings ?? []
  const risk: RiskLevel = s.overall_risk

  const toggle = (id: string) => {
    setExpanded((cur) => {
      const next = new Set(cur)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <div className="rounded border border-zinc-200 dark:border-zinc-800">
      <div className="flex items-center justify-between border-b border-zinc-100 px-4 py-3 dark:border-zinc-900">
        <div className="flex items-center gap-2 text-sm font-medium">
          <ShieldAlert className="h-4 w-4 text-violet-500" />
          Compliance scan
          <span aria-hidden className={`ml-2 inline-block h-2 w-2 rounded-full ${RISK_DOT[risk] ?? 'bg-zinc-300'}`} />
          <span className="uppercase tracking-wide">{risk}</span>
        </div>
        <div className="flex items-center gap-2 text-xs text-zinc-500">
          <span>scanned {new Date(s.scanned_at).toLocaleString()}</span>
          <Button size="sm" variant="ghost" disabled={rescan.isPending} onClick={() => rescan.mutate()}>
            <RefreshCw className="mr-1 h-3 w-3" />
            Rescan
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-4 gap-3 px-4 py-3 text-xs">
        <Stat label="PII" value={s.pii_count} />
        <Stat label="PHI" value={s.phi_count} />
        <Stat label="Open critical" value={s.critical_count} />
        <Stat label="Open high" value={s.high_count} />
      </div>

      {findings.length === 0 ? (
        <div className="px-4 pb-4 text-sm text-zinc-500">No findings.</div>
      ) : (
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-900">
          {findings.map((f) => (
            <FindingRow
              key={f.id}
              finding={f}
              expanded={expanded.has(f.id)}
              busy={review.isPending}
              onToggle={() => toggle(f.id)}
              onReview={(status, note) => review.mutate({ id: f.id, status, note })}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function FindingRow({
  finding,
  expanded,
  busy,
  onToggle,
  onReview,
}: {
  finding: ComplianceFinding
  expanded: boolean
  busy: boolean
  onToggle: () => void
  onReview: (status: RemediationStatus, note?: string) => void
}) {
  const f = finding
  return (
    <li className="px-4 py-2 text-sm">
      <div className="flex items-center gap-3">
        <span aria-hidden className={`h-2 w-2 rounded-full ${RISK_DOT[f.risk_level]}`} />
        <span className="w-32 truncate font-mono">{f.entity_type}</span>
        <span className="w-12 text-right tabular-nums text-zinc-500">{f.occurrence_count}</span>
        <span className="w-24 truncate text-zinc-500">
          {f.page_numbers.length > 0 ? `pp. ${f.page_numbers.join(', ')}` : '—'}
        </span>
        <Badge variant="outline" className="text-[10px] uppercase">
          {f.detection_source}
        </Badge>
        <span className="flex-1" />
        <Badge variant={f.remediation_status === 'open' ? 'in_review' : 'archived'}>
          {STATUS_LABEL[f.remediation_status]}
        </Badge>
        <Button size="sm" variant="ghost" onClick={onToggle} aria-label={expanded ? 'Collapse' : 'Expand'}>
          {expanded ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
        </Button>
      </div>
      {expanded && (
        <div className="mt-2 rounded bg-zinc-50 px-3 py-2 text-xs dark:bg-zinc-900">
          <div className="mb-2 font-medium text-zinc-600">Sample (value redacted)</div>
          <div className="font-mono text-zinc-700 dark:text-zinc-300">{f.sample_context || '—'}</div>
          {f.remediation_status === 'open' && (
            <div className="mt-3 flex flex-wrap gap-2">
              <Button size="sm" variant="outline" disabled={busy} onClick={() => onReview('acknowledged')}>
                Acknowledge
              </Button>
              <Button size="sm" variant="outline" disabled={busy} onClick={() => onReview('remediated')}>
                Mark remediated
              </Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => onReview('false_positive')}>
                False positive
              </Button>
            </div>
          )}
          {f.remediation_status !== 'open' && (
            <div className="mt-2 flex justify-end">
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => onReview('open')}>
                Reopen
              </Button>
            </div>
          )}
        </div>
      )}
    </li>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded bg-zinc-50 px-3 py-2 dark:bg-zinc-900">
      <div className="text-zinc-500">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  )
}
