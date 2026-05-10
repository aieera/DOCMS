import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Shield, ShieldCheck, ShieldAlert } from 'lucide-react'

import {
  getRequest,
  startQES,
  SIGNATURE_TYPE_DESCRIPTIONS,
  type SignatureType,
  type QESProvider,
} from '@/api/signatures'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'

export const Route = createFileRoute('/_authenticated/sign/$requestId/$signerId')({
  component: SignPage,
})

// SignPage — ADR 0070 entry point for a signer.
//
// The signer arrives here from an email link or the inbox. The page:
//   1. Loads the signature_request to surface the document title +
//      signer position.
//   2. Lets the signer pick a signature TYPE (Simple / Advanced /
//      Qualified). The type choice was missing from the AdES-only
//      first cut; ADR 0070 makes it user-visible.
//   3. For Qualified: shows a TSP picker + a"Sign with <provider>"
//      button. Click → POST /qes/start, then window.location =
//      redirect_url. The QTSP calls our /qes/return on completion,
//      which 302s the browser to /sign/done.
//   4. Simple/Advanced flows aren't expanded here — they hit the
//      pre-existing /signatures/requests/{id}/sign/{signerId} record
//      endpoint and stay on-page.

function SignPage() {
  const { requestId, signerId } = Route.useParams()
  const reqQ = useQuery({
    queryKey: ['signature-request', requestId],
    queryFn: () => getRequest(requestId),
  })
  const [type, setType] = useState<SignatureType>('advanced')
  const [provider, setProvider] = useState<QESProvider>('swisscom')

  const startMutation = useMutation({
    mutationFn: async () => {
      const signer = reqQ.data?.signers.find((s) => s.id === signerId)
      if (!signer) throw new Error('signer not found in request')
      // The signer page only knows the document_id from the request;
      // resolving bytes happens here. In real prod we'd stream from
      // /documents/{id}/content. For QES we need the bytes locally
      // anyway (to hand to the QTSP via document_bytes_b64).
      const bytes = await fetchDocumentBytes(reqQ.data!.document_id)
      const b64 = await blobToBase64(bytes)
      return startQES({
        request_id: requestId,
        signer_id: signerId,
        provider,
        signer_email: signer.email,
        signer_name: signer.name,
        document_bytes_b64: b64,
        reason: 'Signed via VaultDMS',
      })
    },
    onSuccess: (out) => {
      toast.success(`Redirecting to ${provider}…`)
      window.location.href = out.redirect_url
    },
    onError: (e: Error) => toast.error(e.message || 'Could not start QES'),
  })

  if (reqQ.isLoading) return <Spinner />
  if (!reqQ.data) return <p className="p-6 text-sm text-destructive">Request not found</p>

  const signer = reqQ.data.signers.find((s) => s.id === signerId)
  if (!signer) return <p className="p-6 text-sm text-destructive">Signer not found</p>
  if (signer.status === 'signed') {
    return <p className="p-6 text-sm">You have already signed this document.</p>
  }

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader title="Sign document" description={`Document ${reqQ.data.document_id}`} />

      <section className="mb-6 rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-lg font-semibold">Signature type</h2>
        <div className="grid gap-3 md:grid-cols-3" data-testid="sig-type-selector">
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
          <h2 className="mb-3 text-lg font-semibold">Qualified Trust Service Provider</h2>
          <p className="mb-3 text-xs text-muted-foreground">
            You'll be redirected to the TSP to verify your identity. After authentication you'll return here automatically.
          </p>
          <select
            value={provider}
            onChange={(e) => setProvider(e.target.value as QESProvider)}
            className="mb-3 h-9 rounded-md border border-border bg-background px-2 text-sm"
            data-testid="qes-provider-select"
          >
            <option value="swisscom">Swisscom (Switzerland)</option>
            <option value="intesi">Intesi Group (Italy / EU)</option>
            <option value="infocert">InfoCert (Italy / EU)</option>
            <option value="mock">VaultDMS Mock (testing)</option>
          </select>
          <Button
            onClick={() => startMutation.mutate()}
            loading={startMutation.isPending}
            data-testid="qes-start"
          >
            Continue to {provider}
          </Button>
        </section>
      )}

      {type !== 'qualified' && (
        <section className="rounded-lg border border-border bg-card p-4">
          <p className="text-sm text-muted-foreground">
            {type === 'simple'
              ? 'Click below to apply a simple signature image to the document.'
              : 'Click below to sign with the server-managed certificate.'}
          </p>
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
