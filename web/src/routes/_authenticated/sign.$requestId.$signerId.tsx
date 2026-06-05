import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Shield, ShieldCheck, ShieldAlert } from 'lucide-react'

import {
  getRequest,
  startQES,
  recordSignature,
  sha256Hex,
  SIGNATURE_TYPE_DESCRIPTIONS,
  type SignatureType,
  type QESProvider,
} from '@/api/signatures'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { SignaturePad, detectDeviceKind, type SignaturePadHandle } from '@/components/signatures/SignaturePad'

export const Route = createFileRoute('/_authenticated/sign/$requestId/$signerId')({
  component: SignPage,
})

// SignPage — ADR 0070 + ADR 0073 entry point for a remote/mobile signer.
//
// The signer arrives here from an email link. The page:
//   1. Loads the signature_request to surface the document title +
//      signer position.
//   2. Lets the signer pick a signature TYPE (Simple / Advanced /
//      Qualified). The type choice was missing from the AdES-only
//      first cut; ADR 0070 makes it user-visible.
//   3. For Simple / Advanced: shows the touch-friendly SignaturePad
//      (ADR 0073). On submit, captures the SVG path + the SHA-256
//      of the document bytes the signer saw, then POSTs to the
//      per-signer record endpoint.
//   4. For Qualified: shows a TSP picker + a "Sign with <provider>"
//      button. Click → POST /qes/start, then window.location =
//      redirect_url. The QTSP calls our /qes/return on completion,
//      which 302s the browser to /sign/done.
//
// The layout is mobile-first (mx-auto px-4 py-6, type grid collapses
// to single column < sm) so the same route serves the desktop and
// the mobile signing modes. The blueprint §11.4 split between
// "remote" and "mobile" is a UI surface — the backend record path
// is the same.

function SignPage() {
  const { requestId, signerId } = Route.useParams()
  const navigate = useNavigate()
  const reqQ = useQuery({
    queryKey: ['signature-request', requestId],
    queryFn: () => getRequest(requestId),
  })
  const [type, setType] = useState<SignatureType>('advanced')
  const [provider, setProvider] = useState<QESProvider>('swisscom')
  const padRef = useRef<SignaturePadHandle>(null)

  const startMutation = useAppMutation({
    mutationFn: async () => {
      const signer = reqQ.data?.signers.find((s) => s.id === signerId)
      if (!signer) throw new Error('signer not found in request')
      const bytes = await fetchDocumentBytes(reqQ.data!.document_id)
      const b64 = await blobToBase64(bytes)
      return startQES({
        request_id: requestId,
        signer_id: signerId,
        provider,
        signer_email: signer.email,
        signer_name: signer.name,
        document_bytes_b64: b64,
        reason: 'Signed via SeDoc',
      })
    },
    onSuccess: (out) => {
      toast.success(`Redirecting to ${provider}…`)
      window.location.href = out.redirect_url
    },
    onError: (e: Error) => toast.error(e.message || 'Could not start QES'),
  })

  const recordMutation = useAppMutation({
    mutationFn: async (svgPath: string) => {
      // Capture the document bytes the signer is looking at, hash
      // them, and persist alongside the signature for ADR 0073
      // tamper-detection. If bytes can't be fetched (preview-only
      // permission etc.) we still record the signature, just with
      // an empty hash — the certificate of completion will note
      // the gap.
      let docHash = ''
      try {
        const bytes = await fetchDocumentBytes(reqQ.data!.document_id)
        docHash = await sha256Hex(bytes)
      } catch {
        // best-effort
      }
      await recordSignature({
        request_id: requestId,
        signer_id: signerId,
        svg_path: svgPath,
        device_kind: detectDeviceKind(),
        doc_hash_sha256: docHash,
      })
    },
    onSuccess: () => {
      toast.success('Signature recorded')
      navigate({ to: '/sign/done' })
    },
    onError: (e: Error) => toast.error(e.message || 'Could not record signature'),
  })

  if (reqQ.isLoading) return <Spinner />
  if (!reqQ.data) return <p className="p-6 text-sm text-destructive">Request not found</p>

  const signer = reqQ.data.signers.find((s) => s.id === signerId)
  if (!signer) return <p className="p-6 text-sm text-destructive">Signer not found</p>
  if (signer.status === 'signed') {
    return <p className="p-6 text-sm">You have already signed this document.</p>
  }

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6">
      <PageHeader title="Sign document" description={`Document ${reqQ.data.document_id}`} />

      <section className="mb-6 rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-base font-semibold sm:text-lg">Signature type</h2>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3" data-testid="sig-type-selector">
          {(['simple', 'advanced', 'qualified'] as SignatureType[]).map((t) => {
            const meta = SIGNATURE_TYPE_DESCRIPTIONS[t]
            const Icon = t === 'qualified' ? ShieldCheck : t === 'advanced' ? Shield : ShieldAlert
            return (
              <button
                key={t}
                onClick={() => setType(t)}
                className={`flex flex-col items-start gap-1 rounded-md border p-3 text-start transition-colors ${
                  type === t
                    ? 'border-primary bg-primary/10'
                    : 'border-border hover:border-primary'
                }`}
                data-testid={`sig-type-${t}`}
                aria-pressed={type === t}
              >
                <Icon className="h-5 w-5" />
                <span className="font-medium">{meta.label}</span>
                <span className="text-xs text-muted-foreground">{meta.help}</span>
              </button>
            )
          })}
        </div>
      </section>

      {type === 'qualified' && (
        <section className="mb-6 rounded-lg border border-border bg-card p-4" data-testid="qes-block">
          <h2 className="mb-3 text-base font-semibold sm:text-lg">Qualified Trust Service Provider</h2>
          <p className="mb-3 text-xs text-muted-foreground">
            You'll be redirected to the TSP to verify your identity. After authentication you'll return here automatically.
          </p>
          <select
            value={provider}
            onChange={(e) => setProvider(e.target.value as QESProvider)}
            className="mb-3 h-11 w-full rounded-md border border-border bg-background px-2 text-sm sm:h-9 sm:w-auto"
            data-testid="qes-provider-select"
          >
            <option value="swisscom">Swisscom (Switzerland)</option>
            <option value="intesi">Intesi Group (Italy / EU)</option>
            <option value="infocert">InfoCert (Italy / EU)</option>
            <option value="mock">SeDoc Mock (testing)</option>
          </select>
          <Button
            onClick={() => startMutation.mutate()}
            loading={startMutation.isPending}
            data-testid="qes-start"
            className="w-full sm:w-auto"
          >
            Continue to {provider}
          </Button>
        </section>
      )}

      {type !== 'qualified' && (
        <section className="rounded-lg border border-border bg-card p-4">
          <h2 className="mb-2 text-base font-semibold sm:text-lg">Draw your signature</h2>
          <p className="mb-3 text-xs text-muted-foreground">
            {type === 'simple'
              ? 'Sign with your finger or stylus. We capture it as vector data — no photo upload needed.'
              : 'Sign with your finger or stylus. The server-managed certificate will be applied on submit.'}
          </p>
          <SignaturePad
            ref={padRef}
            onSubmit={(svg) => recordMutation.mutate(svg)}
            disabled={recordMutation.isPending}
            submitLabel={recordMutation.isPending ? 'Recording…' : 'Apply signature'}
          />
        </section>
      )}
    </div>
  )
}

// fetchDocumentBytes resolves the to-be-signed PDF. Lives in this
// module rather than a dedicated client because the frontend doesn't
// otherwise need to download document content as bytes — the viewer
// streams via an <iframe>.
async function fetchDocumentBytes(documentId: string): Promise<Blob> {
  const res = await fetch(`/api/v1/documents/${documentId}/content`, {
    credentials: 'include',
  })
  if (!res.ok) throw new Error('Could not load document bytes for signing')
  return res.blob()
}

async function blobToBase64(b: Blob): Promise<string> {
  const buf = await b.arrayBuffer()
  let bin = ''
  const view = new Uint8Array(buf)
  for (let i = 0; i < view.byteLength; i++) bin += String.fromCharCode(view[i])
  return btoa(bin)
}
