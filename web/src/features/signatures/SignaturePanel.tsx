// Per-document signature panel. Mounted on the document detail page.
//
// Lists every SignatureRequest for the doc, lets the owner create a new
// request, cancel pending ones, and displays per-signer status. Verify
// sits next to the list so a reader can confirm tamper-evidence on a
// completed signed PDF in one click.

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { PenTool, ShieldCheck, AlertTriangle, X, Plus, Mail } from 'lucide-react'
import toast from 'react-hot-toast'

import {
  cancelSignatureRequest,
  createSignatureRequest,
  listSignaturesForDocument,
  verifyDocumentSignatures,
  type SignatureRequest,
  type Signer,
} from '@/api/signatures'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'

interface Props {
  documentID: string
  versionID: string
  // When set, the "Request signature" button is disabled with the
  // tooltip explaining why (typically: doc is on legal hold). Existing
  // requests continue to render so signers can complete in-flight
  // signatures even after a hold lands.
  disabled?: boolean
  disabledReason?: string
}

export function SignaturePanel({ documentID, versionID, disabled, disabledReason }: Props) {
  const qc = useQueryClient()
  const { data: requests, isLoading } = useQuery({
    queryKey: ['signatures', 'document', documentID],
    queryFn: () => listSignaturesForDocument(documentID),
  })
  const [createOpen, setCreateOpen] = useState(false)
  const [cancelTarget, setCancelTarget] = useState<SignatureRequest | null>(null)
  const [verifyResult, setVerifyResult] = useState<{ ok: boolean; message: string } | null>(null)

  const cancel = useMutation({
    mutationFn: (id: string) => cancelSignatureRequest(id),
    onSuccess: () => {
      toast.success('Signature request cancelled')
      qc.invalidateQueries({ queryKey: ['signatures', 'document', documentID] })
      setCancelTarget(null)
    },
    onError: (e) => toast.error(`Cancel failed: ${String(e)}`),
  })

  const verify = useMutation({
    mutationFn: () => verifyDocumentSignatures(documentID),
    onSuccess: (r) => {
      setVerifyResult({
        ok: r.tamper_evident && r.signatures.every((s) => s.valid),
        message:
          r.signature_count === 0
            ? 'No signatures on this document yet.'
            : r.tamper_evident
              ? `${r.signature_count} signature${r.signature_count === 1 ? '' : 's'} verified — PDF is tamper-evident.`
              : 'Document was modified after signing.',
      })
    },
    onError: (e) => toast.error(`Verify failed: ${String(e)}`),
  })

  return (
    <section
      data-testid="signature-panel"
      className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
    >
      <header className="mb-3 flex items-center justify-between">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <PenTool className="h-4 w-4" aria-hidden="true" />
          Signatures
        </h3>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            data-testid="signatures-verify"
            onClick={() => verify.mutate()}
            loading={verify.isPending}
          >
            <ShieldCheck className="mr-1 h-3.5 w-3.5" /> Verify
          </Button>
          <Button
            variant="primary"
            size="sm"
            data-testid="signatures-request"
            onClick={() => setCreateOpen(true)}
            disabled={disabled}
            title={disabled ? disabledReason : undefined}
          >
            <Plus className="mr-1 h-3.5 w-3.5" /> Request signature
          </Button>
        </div>
      </header>

      {verifyResult && (
        <div
          role="status"
          data-testid="signatures-verify-result"
          className={
            verifyResult.ok
              ? 'mb-3 flex items-start gap-2 rounded border border-emerald-200 bg-emerald-50 p-2 text-xs text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200'
              : 'mb-3 flex items-start gap-2 rounded border border-amber-200 bg-amber-50 p-2 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200'
          }
        >
          {verifyResult.ok ? (
            <ShieldCheck className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          ) : (
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          )}
          <span>{verifyResult.message}</span>
        </div>
      )}

      {isLoading && <p className="text-xs text-[var(--color-text-secondary)]">Loading…</p>}

      {requests && requests.length === 0 && !isLoading && (
        <p className="text-xs text-[var(--color-text-secondary)]" data-testid="signatures-empty">
          No signature requests for this document.
        </p>
      )}

      {requests && requests.length > 0 && (
        <ul className="space-y-3">
          {requests.map((req) => (
            <RequestRow
              key={req.id}
              req={req}
              cancelling={cancel.isPending && cancel.variables === req.id}
              onCancel={() => setCancelTarget(req)}
            />
          ))}
        </ul>
      )}

      <CreateRequestDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        documentID={documentID}
        versionID={versionID}
        onCreated={() => {
          qc.invalidateQueries({ queryKey: ['signatures', 'document', documentID] })
          setCreateOpen(false)
          toast.success('Signature request sent')
        }}
      />

      <ConfirmDialog
        open={cancelTarget !== null}
        onOpenChange={(v) => !v && setCancelTarget(null)}
        title="Cancel signature request?"
        description={
          cancelTarget
            ? `Pending signers will lose access to the signing link. The cancellation is recorded in the audit trail.`
            : ''
        }
        confirmLabel="Cancel request"
        destructive
        loading={cancel.isPending}
        onConfirm={() => cancelTarget && cancel.mutate(cancelTarget.id)}
      />
    </section>
  )
}

function RequestRow({
  req,
  cancelling,
  onCancel,
}: {
  req: SignatureRequest
  cancelling: boolean
  onCancel: () => void
}) {
  const cancellable = req.status === 'pending' || req.status === 'in_progress'
  return (
    <li
      data-testid={`signature-request-${req.id}`}
      className="rounded border border-[var(--color-border)]/70 bg-[var(--color-bg)] p-3"
    >
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="flex items-center gap-2 text-xs">
            <StatusBadge status={req.status} />
            <span className="text-[var(--color-text-secondary)]">
              created {new Date(req.created_at).toLocaleDateString()}
              {req.expires_at && ` · expires ${new Date(req.expires_at).toLocaleDateString()}`}
            </span>
          </div>
          <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
            Provider: {req.provider} · {req.signers.length} signer{req.signers.length === 1 ? '' : 's'}
          </p>
        </div>
        {cancellable && (
          <Button
            variant="ghost"
            size="sm"
            data-testid={`signature-cancel-${req.id}`}
            onClick={onCancel}
            loading={cancelling}
            aria-label="Cancel signature request"
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>
      <ul className="mt-2 space-y-1">
        {req.signers
          .slice()
          .sort((a, b) => a.order - b.order)
          .map((s) => (
            <SignerRow key={s.id} signer={s} />
          ))}
      </ul>
    </li>
  )
}

function SignerRow({ signer }: { signer: Signer }) {
  return (
    <li className="flex items-center justify-between gap-2 rounded bg-[var(--color-bg-secondary)] px-2 py-1 text-xs">
      <span className="flex min-w-0 items-center gap-1.5">
        <Mail className="h-3 w-3 shrink-0 text-[var(--color-text-secondary)]" aria-hidden="true" />
        <span className="truncate">
          <strong>{signer.name}</strong> · {signer.email}
        </span>
        <span className="rounded bg-slate-200 px-1 py-0.5 text-[10px] uppercase text-slate-700 dark:bg-slate-700 dark:text-slate-200">
          {signer.role}
        </span>
      </span>
      <SignerStatusBadge status={signer.status} />
    </li>
  )
}

function StatusBadge({ status }: { status: SignatureRequest['status'] }) {
  const styles: Record<SignatureRequest['status'], string> = {
    pending: 'bg-amber-100 text-amber-900 dark:bg-amber-950/50 dark:text-amber-200',
    in_progress: 'bg-sky-100 text-sky-900 dark:bg-sky-950/50 dark:text-sky-200',
    completed: 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950/50 dark:text-emerald-200',
    cancelled: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200',
    expired: 'bg-red-100 text-red-900 dark:bg-red-950/50 dark:text-red-200',
  }
  return (
    <span className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${styles[status]}`}>
      {status.replace('_', ' ')}
    </span>
  )
}

function SignerStatusBadge({ status }: { status: Signer['status'] }) {
  const styles: Record<Signer['status'], string> = {
    pending: 'text-amber-700 dark:text-amber-300',
    signed: 'text-emerald-700 dark:text-emerald-300',
    declined: 'text-red-700 dark:text-red-300',
  }
  return <span className={`text-[10px] font-medium ${styles[status]}`}>{status}</span>
}

function CreateRequestDialog({
  open,
  onOpenChange,
  documentID,
  versionID,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  documentID: string
  versionID: string
  onCreated: () => void
}) {
  const [signers, setSigners] = useState<Array<{ email: string; name: string }>>([
    { email: '', name: '' },
  ])
  const create = useMutation({
    mutationFn: () =>
      createSignatureRequest({
        document_id: documentID,
        version_id: versionID,
        signers: signers
          .filter((s) => s.email.trim() && s.name.trim())
          .map((s, i) => ({ email: s.email.trim(), name: s.name.trim(), role: 'signer', order: i })),
      }),
    onSuccess: onCreated,
    onError: (e) => toast.error(`Could not create request: ${String(e)}`),
  })

  const valid = signers.some((s) => s.email.trim() && s.name.trim())

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Request signature" size="md">
      <div className="space-y-3">
        <p className="text-xs text-[var(--color-text-secondary)]">
          Each signer receives an email with a unique signing link. Order determines the sequence —
          signer #2 cannot sign until #1 completes.
        </p>
        {signers.map((s, i) => (
          <div key={i} className="grid grid-cols-2 gap-2">
            <Input
              data-testid={`signer-${i}-name`}
              placeholder="Name"
              value={s.name}
              onChange={(e) => {
                const next = [...signers]
                next[i] = { ...next[i], name: e.target.value }
                setSigners(next)
              }}
            />
            <Input
              data-testid={`signer-${i}-email`}
              type="email"
              placeholder="email@example.com"
              value={s.email}
              onChange={(e) => {
                const next = [...signers]
                next[i] = { ...next[i], email: e.target.value }
                setSigners(next)
              }}
            />
          </div>
        ))}
        <button
          type="button"
          className="text-xs text-[var(--color-primary)] hover:underline"
          onClick={() => setSigners([...signers, { email: '', name: '' }])}
        >
          + Add another signer
        </button>
        <div className="flex justify-end gap-2 pt-3">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!valid}
            loading={create.isPending}
            onClick={() => create.mutate()}
            data-testid="signatures-request-submit"
          >
            Send for signing
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
