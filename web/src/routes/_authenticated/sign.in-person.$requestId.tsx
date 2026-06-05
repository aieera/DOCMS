import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMemo, useRef, useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { CheckCircle2, UserCheck } from 'lucide-react'
import { isAxiosError } from 'axios'

import {
  getRequest,
  signInPerson,
  sha256Hex,
  type Signer,
} from '@/api/signatures'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { SignaturePad, detectDeviceKind, type SignaturePadHandle } from '@/components/signatures/SignaturePad'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

export const Route = createFileRoute('/_authenticated/sign/in-person/$requestId')({
  component: InPersonSignPage,
})

// /sign/in-person/$requestId — ADR 0073 tablet ceremony.
//
// Single device, sequential signer + witness on the same session.
// The page walks the signer list in order_index sequence; between
// each step a hand-off prompt makes the device transfer
// unambiguous. The backend enforces the order — if the operator
// somehow gets ahead, the 409 response carries the expected next
// signer id and we fast-forward to that step.
//
// Auth: this is an /_authenticated route, so the device user is the
// workflow operator (counter clerk, notary assistant, etc.). The
// named signer row provides the signer identity on the audit
// trail. The certificate of completion (issued by the document
// service post-completion) makes that distinction explicit.

function InPersonSignPage() {
  const { requestId } = Route.useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const reqQ = useQuery({
    queryKey: ['signature-request', requestId],
    queryFn: () => getRequest(requestId),
  })

  // Pending signers in order. Filter out cc/approver — only signer
  // and witness gate completion.
  const orderedSigners: Signer[] = useMemo(() => {
    const all = reqQ.data?.signers ?? []
    return all
      .filter((s) => s.role === 'signer' || s.role === 'witness' || !s.role)
      .slice()
      .sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
  }, [reqQ.data])

  const firstPendingIndex = useMemo(
    () => orderedSigners.findIndex((s) => s.status !== 'signed'),
    [orderedSigners],
  )
  const [stepIndex, setStepIndex] = useState(0)

  // Sync the step pointer to the next pending signer when the
  // request loads. Without this the operator would always start at
  // index 0 even if the signer already signed in a previous session.
  useEffect(() => {
    if (firstPendingIndex >= 0 && firstPendingIndex !== stepIndex) {
      setStepIndex(firstPendingIndex)
    }
  }, [firstPendingIndex, stepIndex])

  const [handoff, setHandoff] = useState(false)
  const padRef = useRef<SignaturePadHandle>(null)

  const signMut = useAppMutation({
    mutationFn: async ({ signer, svgPath }: { signer: Signer; svgPath: string }) => {
      let docHash = ''
      try {
        const bytes = await fetchDocumentBytes(reqQ.data!.document_id)
        docHash = await sha256Hex(bytes)
      } catch {
        // best-effort
      }
      return signInPerson({
        request_id: requestId,
        signer_id: signer.id!,
        svg_path: svgPath,
        device_kind: detectDeviceKind(),
        doc_hash_sha256: docHash,
      })
    },
    onSuccess: async (_, vars) => {
      toast.success(`${vars.signer.name || 'Signer'} signed`)
      const nextIdx = stepIndex + 1
      await qc.invalidateQueries({ queryKey: ['signature-request', requestId] })
      if (nextIdx >= orderedSigners.length) {
        navigate({ to: '/sign/done' })
        return
      }
      setStepIndex(nextIdx)
      setHandoff(true)
    },
    onError: (e: unknown) => {
      // 409 with expected_signer_id → fast-forward instead of
      // dropping the operator into a dead-end error.
      if (isAxiosError(e) && e.response?.status === 409) {
        const expected = e.response.data?.expected_signer_id
        if (expected) {
          const idx = orderedSigners.findIndex((s) => s.id === expected)
          if (idx >= 0) {
            setStepIndex(idx)
            toast.message('Reorienting to the next pending signer')
            return
          }
        }
      }
      toast.error((e as Error).message || 'Signing failed')
    },
  })

  if (reqQ.isLoading) return <Spinner />
  if (!reqQ.data) return <p className="p-6 text-sm text-destructive">Request not found</p>
  if (reqQ.data.signing_mode && reqQ.data.signing_mode !== 'in_person') {
    return (
      <p className="p-6 text-sm text-destructive">
        This signature request was created in <code>{reqQ.data.signing_mode}</code> mode, not <code>in_person</code>.
      </p>
    )
  }
  if (reqQ.data.status === 'completed' || firstPendingIndex < 0) {
    return (
      <div className="mx-auto max-w-md p-6 text-center" data-testid="in-person-complete">
        <CheckCircle2 className="mx-auto mb-3 h-10 w-10 text-foreground" />
        <h2 className="text-lg font-semibold">All signers complete</h2>
        <p className="mt-1 text-sm text-muted-foreground">This signature request is done.</p>
      </div>
    )
  }

  const signer = orderedSigners[stepIndex]
  if (!signer) return <p className="p-6 text-sm text-destructive">No signer at step {stepIndex}</p>
  const total = orderedSigners.length

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6">
      <PageHeader title="In-person signing" description={`Document ${reqQ.data.document_id}`} />

      <ol
        className="mb-4 flex flex-wrap items-center gap-2 text-xs"
        aria-label="Signer progress"
        data-testid="in-person-progress"
      >
        {orderedSigners.map((s, idx) => (
          <li
            key={s.id}
            className={
              idx === stepIndex
                ? 'rounded-full bg-foreground px-2.5 py-1 font-medium text-background'
                : s.status === 'signed'
                  ? 'rounded-full border border-border px-2.5 py-1 text-muted-foreground line-through'
                  : 'rounded-full border border-border px-2.5 py-1 text-muted-foreground'
            }
            data-testid={`in-person-step-${idx}`}
          >
            {idx + 1}. {s.name || s.email} {s.role === 'witness' ? '(witness)' : ''}
          </li>
        ))}
      </ol>

      {handoff ? (
        <section
          className="rounded-lg border border-dashed border-border bg-card p-8 text-center"
          data-testid="in-person-handoff"
        >
          <UserCheck className="mx-auto mb-3 h-10 w-10 text-foreground" />
          <h2 className="text-lg font-semibold">Please hand the device to</h2>
          <p className="mt-1 text-2xl font-semibold tracking-tight">{signer.name || signer.email}</p>
          <p className="mt-1 text-sm text-muted-foreground">
            {signer.role === 'witness' ? 'Witness countersignature' : `Signer ${stepIndex + 1} of ${total}`}
          </p>
          <Button
            className="mt-4 w-full sm:w-auto"
            onClick={() => setHandoff(false)}
            data-testid="in-person-handoff-continue"
          >
            I'm ready <DirectionalIcon name="ArrowRight" className="ms-1 h-4 w-4" />
          </Button>
        </section>
      ) : (
        <section className="rounded-lg border border-border bg-card p-4" data-testid="in-person-pad-block">
          <h2 className="mb-1 text-base font-semibold sm:text-lg">
            {signer.name || signer.email}
            {signer.role === 'witness' && <span className="ms-2 rounded bg-muted px-2 py-0.5 text-xs">Witness</span>}
          </h2>
          <p className="mb-3 text-xs text-muted-foreground">
            Step {stepIndex + 1} of {total}. Sign below, then pass the device to the next signer when prompted.
          </p>
          <SignaturePad
            ref={padRef}
            onSubmit={(svg) => signMut.mutate({ signer, svgPath: svg })}
            disabled={signMut.isPending}
            submitLabel={signMut.isPending ? 'Recording…' : 'Apply signature'}
          />
        </section>
      )}
    </div>
  )
}

async function fetchDocumentBytes(documentId: string): Promise<Blob> {
  const res = await fetch(`/api/v1/documents/${documentId}/content`, {
    credentials: 'include',
  })
  if (!res.ok) throw new Error('Could not load document bytes')
  return res.blob()
}
