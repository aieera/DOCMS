import { useState } from 'react'
import { TimeAgo } from '@/components/shared/TimeAgo'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { ChevronDown, ChevronUp, RefreshCw, ShieldAlert } from 'lucide-react'

import {
  getDocumentCompliance,
  rescanDocument,
  reviewComplianceFinding,
  type ComplianceFinding,
  type RemediationStatus,
  type RiskLevel,
} from '@/api/compliance-pii'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'

interface Props {
  documentId: string
}

const RISK_DOT: Record<string, string> = {
  critical: 'bg-destructive',
  high:     'bg-warning',
  medium:   'bg-warning/60',
  low:      'bg-success',
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

  const review = useAppMutation({
    mutationFn: ({ id, status, note }: { id: string; status: RemediationStatus; note?: string }) =>
      reviewComplianceFinding(documentId, id, status, note),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['compliance', documentId] }),
    onError: () => toast.error('Review failed'),
  })

  const rescan = useAppMutation({
    mutationFn: () => rescanDocument(documentId),
    // "Scan" rather than "Rescan": this fires on the first run too, where
    // "Rescan" is misleading.
    onSuccess: () => toast.success('Scan queued'),
    onError: () => toast.error('Scan failed'),
  })

  if (isLoading) return <div className="p-4 text-sm text-muted-foreground">Loading compliance scan…</div>
  if (!data?.summary) {
    return (
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg bg-card p-4 text-sm shadow-neu-sm">
        <p className="text-muted-foreground">
          No compliance scan yet for this document.
        </p>
        <Button
          size="sm"
          variant="default"
          onClick={() => rescan.mutate()}
          loading={rescan.isPending}
          data-testid="compliance-run-scan"
        >
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
    <div className="rounded-lg bg-card shadow-neu">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <div className="flex items-center gap-2 text-sm font-medium">
          <ShieldAlert className="h-4 w-4 text-violet-500" />
          Compliance scan
          <span aria-hidden className={`ms-2 inline-block h-2 w-2 rounded-full ${RISK_DOT[risk] ?? 'bg-muted-foreground'}`} />
          <span className="uppercase tracking-wide">{risk}</span>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span>scanned <TimeAgo date={s.scanned_at} /></span>
          <Button size="sm" variant="ghost" disabled={rescan.isPending} onClick={() => rescan.mutate()}>
            <RefreshCw className="me-1 h-3 w-3" />
            Rescan
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-3 px-4 py-3 text-xs sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="PII" value={s.pii_count} />
        <Stat label="PHI" value={s.phi_count} />
        <Stat label="Open critical" value={s.critical_count} />
        <Stat label="Open high" value={s.high_count} />
      </div>

      {findings.length === 0 ? (
        <div className="px-4 pb-4 text-sm text-muted-foreground">No findings.</div>
      ) : (
        <ul className="divide-y divide-border">
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
        <span className="w-12 text-end tabular-nums text-muted-foreground">{f.occurrence_count}</span>
        <span className="w-24 truncate text-muted-foreground">
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
        <div className="mt-2 rounded-xl bg-muted px-3 py-2 text-xs shadow-neu-inset">
          <div className="mb-2 font-medium text-muted-foreground">Sample (value redacted)</div>
          <div className="font-mono text-foreground">{f.sample_context || '—'}</div>
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
    <div className="rounded bg-muted px-3 py-2">
      <div className="text-muted-foreground">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  )
}
