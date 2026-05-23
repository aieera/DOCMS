import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ShieldOff, ShieldCheck } from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'
import { Dialog } from '@/components/ui/Dialog'
import { Badge } from '@/components/ui/shadcn/badge'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { setRetentionExempt } from '@/api/documents'
import { readErrorMessage } from '@/api/client'
import type { Document } from '@/types/api'

interface Props {
  doc: Document
  /**
   * Permission gate from the parent. When false, the toggle is hidden
   * entirely — exempting a document is an admin-capability operation
   * (the backend enforces this too via the policy service, but
   * surfacing the control to a user who'd just be denied is bad UX).
   */
  canManage: boolean
}

/**
 * Per-document retention exemption toggle.
 *
 * Distinct from legal hold (a separate panel surfaces that):
 *   - Legal hold = litigation-driven, freezes ALL lifecycle changes.
 *   - Retention exempt = business waiver, only excludes the doc from
 *     the retention sweep. Documents can still be edited, archived
 *     manually, shared, etc.
 *
 * Setting exempt=true requires a justification string — the audit
 * event (dms.document.retention_exempt_set.v1) carries it so a future
 * FRCP / SOX reviewer can reconstruct the business reason.
 */
export function RetentionExemptToggle({ doc, canManage }: Props) {
  const qc = useQueryClient()
  const [setOpen, setSetOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [confirmRemove, setConfirmRemove] = useState(false)

  const m = useMutation({
    mutationFn: (vars: { exempt: boolean; reason?: string }) =>
      setRetentionExempt(doc.id, vars.exempt, vars.reason),
    onSuccess: (_data, vars) => {
      toast.success(vars.exempt ? 'Retention exemption set' : 'Retention exemption cleared')
      qc.invalidateQueries({ queryKey: ['document', doc.id] })
      setSetOpen(false)
      setConfirmRemove(false)
      setReason('')
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not update exemption'),
  })

  if (!canManage && !doc.retention_exempt) return null

  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2 text-sm font-semibold">
            {doc.retention_exempt
              ? <ShieldCheck className="h-4 w-4 text-amber-600 dark:text-amber-400" />
              : <ShieldOff className="h-4 w-4 text-muted-foreground" />}
            Retention exemption
            {doc.retention_exempt && (
              <Badge variant="in_review" data-testid="retention-exempt-badge">Exempt</Badge>
            )}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            {doc.retention_exempt
              ? 'This document is excluded from the retention sweep until exemption is cleared. Lifecycle, edits, sharing, and legal-hold rules continue to apply normally.'
              : 'A business waiver that keeps this document out of the retention sweep. Distinct from legal hold — does not freeze lifecycle changes.'}
          </p>
          {doc.retention_exempt && doc.retention_exempt_reason && (
            <p className="mt-2 text-xs">
              <span className="font-medium">Reason: </span>
              <span className="text-muted-foreground">{doc.retention_exempt_reason}</span>
            </p>
          )}
        </div>
        {canManage && (
          doc.retention_exempt ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setConfirmRemove(true)}
              data-testid="retention-exempt-clear"
            >
              Clear exemption
            </Button>
          ) : (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setSetOpen(true)}
              data-testid="retention-exempt-set"
            >
              Set exemption…
            </Button>
          )
        )}
      </div>

      <Dialog
        open={setOpen}
        onOpenChange={(o) => { setSetOpen(o); if (!o) setReason('') }}
        title="Set retention exemption"
      >
        <form
          onSubmit={(e) => {
            e.preventDefault()
            const trimmed = reason.trim()
            if (!trimmed) { toast.error('A reason is required'); return }
            m.mutate({ exempt: true, reason: trimmed })
          }}
          className="space-y-4"
        >
          <div>
            <label className="mb-1 block text-sm font-medium">Business reason</label>
            <textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              rows={4}
              autoFocus
              maxLength={1000}
              required
              placeholder="Why is this document being kept beyond default retention? (e.g. 'Anchor document for the Acme litigation, per Legal 2026-05-22.')"
              className="w-full rounded-md border border-border bg-background p-2 text-sm"
              data-testid="retention-exempt-reason"
            />
            <p className="mt-1 text-xs text-muted-foreground">
              Recorded with the audit event so future compliance reviews can reconstruct the justification.
            </p>
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setSetOpen(false)} disabled={m.isPending}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={m.isPending || !reason.trim()}
              loading={m.isPending}
              data-testid="retention-exempt-submit"
            >
              Set exemption
            </Button>
          </div>
        </form>
      </Dialog>

      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title="Clear retention exemption?"
        description="The document re-enters the retention sweep at the next run. Existing lifecycle state is preserved."
        confirmLabel="Clear exemption"
        loading={m.isPending}
        onConfirm={() => m.mutate({ exempt: false, reason: '' })}
      />
    </div>
  )
}
