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

      <div className="mb-3">
        {/* Tier-1 validator runs on demand (Re-validate button).
            We pass null as the cached report — the server doesn't
            attach one to the document API today, so the badge
            renders "Unsigned" until the user clicks Re-validate. */}
        <SignatureValidityBadge documentId={documentId} tier1={null} />
      </div>

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
        <p className="text-xs text-[var(--color-text-secondary)]" data-testid="no-signature-requests">
          No signature requests yet.{' '}
          <Link to="/signatures/send/$documentId" params={{ documentId }} className="text-[var(--color-primary)] hover:underline">
            Start one
          </Link>.
        </p>
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
