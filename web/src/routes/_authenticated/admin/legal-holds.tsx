import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Scale, ShieldOff, Download } from 'lucide-react'

import { listHolds, releaseHold, type LegalHold } from '@/api/holds'
import { exportEDiscovery } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { formatDate, formatRelativeTime } from '@/lib/formatters'

type StatusFilter = 'active' | 'released' | 'all'

function LegalHoldsPage() {
  const [filter, setFilter] = useState<StatusFilter>('active')
  const qc = useQueryClient()

  const statusParam = filter === 'all' ? undefined : filter
  const { data, isLoading } = useQuery({
    queryKey: ['legal-holds', filter],
    queryFn: () => listHolds(statusParam ? { status: statusParam } : {}),
  })

  const release = useMutation({
    mutationFn: ({ id, reason, approver }: { id: string; reason: string; approver: string }) =>
      releaseHold(id, { reason, approver_id: approver }),
    onSuccess: () => {
      toast.success('Hold released')
      qc.invalidateQueries({ queryKey: ['legal-holds'] })
    },
    onError: (err: unknown) => toast.error(extractMessage(err)),
  })

  const [exportTarget, setExportTarget] = useState<LegalHold | null>(null)

  const onRelease = (hold: LegalHold) => {
    const reason = window.prompt(`Release hold "${hold.name}"? Enter reason:`)
    if (!reason) return
    const approver = window.prompt('Approver user UUID (for audit trail):')
    if (!approver) return
    release.mutate({ id: hold.id, reason, approver })
  }

  return (
    <div>
      <PageHeader
        title="Legal Holds"
        description="Manage holds that block deletion, disposition, and redaction"
      />

      <div className="mb-4 flex gap-2">
        {(['active', 'released', 'all'] as const).map((f) => (
          <button
            key={f}
            onClick={() => setFilter(f)}
            className={`rounded-md px-3 py-1 text-sm ${
              filter === f
                ? 'bg-[var(--color-primary)] text-white'
                : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-secondary)]'
            }`}
          >
            {f[0].toUpperCase() + f.slice(1)}
          </button>
        ))}
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
        </div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Scale className="h-12 w-12" />}
          title={filter === 'active' ? 'No active legal holds' : 'No holds'}
          description="Holds block deletion, disposition, and redaction of attached documents."
        />
      ) : (
        <ul className="space-y-2">
          {data.map((hold) => (
            <li
              key={hold.id}
              className="flex items-start justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{hold.name}</span>
                  <Badge variant={hold.is_active ? 'in_review' : 'archived'}>
                    {hold.is_active ? 'Active' : 'Released'}
                  </Badge>
                  {hold.matter_reference && (
                    <span className="text-xs text-[var(--color-text-secondary)]">
                      Matter: {hold.matter_reference}
                    </span>
                  )}
                </div>
                {hold.description && (
                  <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
                    {hold.description}
                  </p>
                )}
                <div className="mt-1 text-xs text-[var(--color-text-secondary)]">
                  Applied {formatRelativeTime(hold.applied_at)} on {formatDate(hold.applied_at)}
                  {hold.released_at && <> · released {formatRelativeTime(hold.released_at)}</>}
                </div>
              </div>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  data-testid={`hold-export-${hold.id}`}
                  onClick={() => setExportTarget(hold)}
                  title="Export attached documents as a signed ZIP for legal review"
                >
                  <Download className="h-4 w-4" /> Export
                </Button>
                {hold.is_active && (
                  <Button onClick={() => onRelease(hold)} disabled={release.isPending}>
                    <ShieldOff className="h-4 w-4" /> Release
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      {exportTarget && (
        <ExportDialog hold={exportTarget} onClose={() => setExportTarget(null)} />
      )}
    </div>
  )
}

function ExportDialog({
  hold,
  onClose,
}: {
  hold: LegalHold
  onClose: () => void
}) {
  const [caseName, setCaseName] = useState(hold.name)
  const [custodianEmail, setCustodianEmail] = useState('')

  // matter_reference is the natural case_id; fall back to the hold's
  // own UUID so the audit row + filename always have a stable handle.
  const caseID = hold.matter_reference || hold.id

  const exportMut = useMutation({
    mutationFn: () =>
      exportEDiscovery({
        case_id: caseID,
        case_name: caseName.trim() || hold.name,
        custodian_email: custodianEmail.trim(),
        document_ids: hold.document_ids ?? [],
      }),
    onSuccess: (r) => {
      toast.success(`Exported ${r.filename}`)
      onClose()
    },
    onError: (err: unknown) => toast.error(extractMessage(err) || 'Export failed'),
  })

  const docCount = hold.document_ids?.length ?? 0
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()} title="Export for eDiscovery" size="md">
      <div className="space-y-3">
        <p className="text-xs text-[var(--color-text-secondary)]">
          Streams a signed ZIP bundling every document attached to this hold. The export is
          recorded as a <code>dms.audit.ediscovery_exported.v1</code> event for chain-of-custody.
        </p>
        <div>
          <label htmlFor="ediscovery-case-id" className="text-xs font-medium">Case ID</label>
          <Input id="ediscovery-case-id" data-testid="ediscovery-case-id" value={caseID} disabled />
        </div>
        <div>
          <label htmlFor="ediscovery-case-name" className="text-xs font-medium">Case name</label>
          <Input
            id="ediscovery-case-name"
            data-testid="ediscovery-case-name"
            value={caseName}
            onChange={(e) => setCaseName(e.target.value)}
            placeholder="Smith v Acme — initial production"
          />
        </div>
        <div>
          <label htmlFor="ediscovery-custodian-email" className="text-xs font-medium">Custodian email</label>
          <Input
            id="ediscovery-custodian-email"
            data-testid="ediscovery-custodian-email"
            type="email"
            value={custodianEmail}
            onChange={(e) => setCustodianEmail(e.target.value)}
            placeholder="legal@acme.example"
          />
        </div>
        <p className="text-xs text-[var(--color-text-secondary)]">
          {docCount === 0
            ? 'This hold has no documents attached — export will produce a manifest-only ZIP.'
            : `${docCount} document${docCount === 1 ? '' : 's'} will be bundled.`}
        </p>
        <div className="flex justify-end gap-2 pt-3">
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            data-testid="ediscovery-submit"
            disabled={!custodianEmail.trim()}
            loading={exportMut.isPending}
            onClick={() => exportMut.mutate()}
          >
            <Download className="h-4 w-4" /> Export ZIP
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function extractMessage(err: unknown): string {
  if (typeof err === 'object' && err && 'message' in err) {
    const m = (err as { message?: string }).message
    if (m) return m
  }
  return 'Release failed'
}

export const Route = createFileRoute('/_authenticated/admin/legal-holds')({ component: LegalHoldsPage })
