// ADR 0070 / 0071 / 0072 — document-detail signatures panel.
//
// Surfaces the signature surface to the user inside the document
// viewer's sidebar:
//
//   1. Validity badge — Tier-1 PAdES verifier verdict + LTV-age,
//      with a Re-validate button.
//   2. Pending requests — any signature_request still in
//      pending/in_progress for this doc, with the provider tag.
//   3. CTA: "Send for signature" → /signatures/send/{documentId}.
//
// Self-hides nothing — the panel always renders so users have a
// predictable place to find the action even when no signature has
// been requested yet.
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Send, Activity } from 'lucide-react'

import { SignatureValidityBadge } from '@/components/shared/SignatureValidityBadge'
import { api } from '@/api/client'
import type { SignatureRequest } from '@/api/signatures'

interface Props {
  documentId: string
}

// listForDocument calls GET /signatures/document/{id}. Co-located
// here rather than added to api/signatures.ts because it's the only
// call site; the broader signatures client already has the typed
// shapes.
async function listForDocument(documentId: string): Promise<SignatureRequest[]> {
  const { data } = await api.get<SignatureRequest[]>(`/signatures/document/${documentId}`)
  return Array.isArray(data) ? data : []
}

export function SignaturesPanel({ documentId }: Props) {
  const reqsQ = useQuery({
    queryKey: ['signatures-for-doc', documentId],
    queryFn: () => listForDocument(documentId),
    refetchInterval: 30_000,
  })
  const pending = (reqsQ.data ?? []).filter((r) =>
    r.status === 'pending' || r.status === 'in_progress',
  )

  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4" data-testid="signatures-panel">
      <header className="mb-2 flex items-center justify-between">
        <h3 className="text-sm font-semibold">Signatures</h3>
        <Link
          to="/signatures/send/$documentId"
          params={{ documentId }}
          className="inline-flex items-center gap-1 text-xs text-[var(--color-primary)] hover:underline"
          data-testid="send-for-signature-link"
        >
          <Send className="h-3 w-3" /> Send
        </Link>
      </header>

      {/* Hide the validity badge + Re-validate action until at least
          one signature request has been created — re-validating a
          document with no signature requests just produces the same
          "Unsigned" result and confuses users (per QA feedback). */}
      {(reqsQ.data ?? []).length > 0 && (
        <div className="mb-3">
          <SignatureValidityBadge documentId={documentId} tier1={null} />
        </div>
      )}

      {pending.length > 0 && (
        <div data-testid="pending-signature-requests">
          <p className="mb-1 text-xs font-medium text-[var(--color-text-secondary)]">In progress</p>
          <ul className="space-y-1 text-xs">
            {pending.map((r) => (
              <li key={r.id} className="flex items-center justify-between" data-testid={`pending-req-${r.id}`}>
                <span className="inline-flex items-center gap-1">
                  <Activity className="h-3 w-3" /> {prettyProvider(r.provider)}
                </span>
                <span className="text-[var(--color-text-secondary)]">{r.signers?.length ?? 0} signer(s)</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {reqsQ.data && reqsQ.data.length === 0 && (
        <div className="flex flex-col items-start gap-2" data-testid="no-signature-requests">
          <p className="text-xs text-muted-foreground">No signature requests yet.</p>
          <Link
            to="/signatures/send/$documentId"
            params={{ documentId }}
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-background px-3 text-xs font-medium transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Send className="h-3 w-3" />
            Start a signature request
          </Link>
        </div>
      )}
    </div>
  )
}

function prettyProvider(p: string): string {
  switch (p) {
    case 'internal':     return 'In-app signature'
    case 'docusign':     return 'DocuSign'
    case 'adobe_sign':   return 'Adobe Sign'
    case 'qes_swisscom': return 'QES (Swisscom)'
    case 'qes_intesi':   return 'QES (Intesi)'
    case 'qes_infocert': return 'QES (InfoCert)'
    default:             return p
  }
}
