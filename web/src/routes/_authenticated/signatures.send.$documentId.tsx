import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Send, Trash2, Plus } from 'lucide-react'

import {
  createRequest, sendViaESign,
  type ESignProvider, type SignatureProvider, type SigningMode,
} from '@/api/signatures'
import { getVersions } from '@/api/documents'
import { readErrorMessage } from '@/api/client'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

// /signatures/send/$documentId — ADR 0071 sender flow.
//
// The user picks a provider and a recipient list, then hits Send.
// For DocuSign / Adobe Sign we:
//   1. POST /signatures/requests with { provider, signers, document_id }
//      → backend mints a signature_request row.
//   2. POST /signatures/esign/send with the document bytes + the
//      newly-minted request_id → backend talks to the vendor and
//      stamps provider_envelope_id on the request.
//
//"Internal" routing is the pre-existing in-app signature flow and
// shows up alongside DocuSign / Adobe Sign so the user has one
// place to dispatch any kind of envelope.

export const Route = createFileRoute('/_authenticated/signatures/send/$documentId')({
  component: SendForSignaturePage,
})

interface RecipientForm {
  email: string
  name: string
  role: 'signer' | 'witness' | 'approver' | 'cc'
  embedded: boolean
}

function SendForSignaturePage() {
  const { documentId } = Route.useParams()
  const navigate = useNavigate()
  const [provider, setProvider] = useState<SignatureProvider>('docusign')
  // ADR 0073 — signing mode picker. Defaults to 'remote' (the
  // legacy magic-link-per-signer behavior). 'mobile' is the same
  // backend path with a mobile-optimized UI hint; 'in_person' is
  // the single-device tablet ceremony.
  const [signingMode, setSigningMode] = useState<SigningMode>('remote')
  const [recipients, setRecipients] = useState<RecipientForm[]>([
    { email: '', name: '', role: 'signer', embedded: false },
  ])
  const [subject, setSubject] = useState('Please sign')
  const [message, setMessage] = useState('')

  // Pick the document's current (highest version_number) version. The
  // signature_requests FK is on (tenant_id, version_id) against the
  // versions table — sending document_id as version_id (the prior
  // shortcut) fails as 23503 because it isn't a real versions.id.
  const versionsQ = useQuery({
    queryKey: ['document-versions', documentId],
    queryFn: () => getVersions(documentId),
  })
  const versionId = versionsQ.data && versionsQ.data.length > 0
    ? [...versionsQ.data].sort((a, b) => b.version_number - a.version_number)[0].id
    : ''

  const sendMut = useMutation({
    mutationFn: async () => {
      if (!versionId) {
        throw new Error('This document has no versions yet — upload a file before sending for signature')
      }
      // 1. Mint signature request.
      const req = await createRequest({
        document_id: documentId, version_id: versionId,
        provider,
        signing_mode: signingMode,
        signers: recipients.map((r, i) => ({
          email: r.email, name: r.name, role: r.role, order: i + 1,
        })),
      })
      // 2. Internal provider stops here — the in-app signing path
      //    takes over via /sign/<request>/<signer> links emailed by
      //    the existing service.
      if (provider === 'internal' || provider.startsWith('qes_')) return req
      // 3. Otherwise hand off to the third-party connector.
      const docBytes = await fetchDocumentBytes(documentId)
      const b64 = await blobToBase64(docBytes)
      const esignProvider: ESignProvider = provider === 'docusign' ? 'docusign' : 'adobe_sign'
      await sendViaESign({
        request_id: req.id,
        provider: esignProvider,
        document_name: 'document.pdf',
        document_bytes_b64: b64,
        recipients: recipients.map((r, i) => ({
          email: r.email, name: r.name, order: i + 1, role: r.role, embedded: r.embedded,
        })),
        subject, message,
        return_url: window.location.origin + '/sign/done',
      })
      return req
    },
    onSuccess: (req) => {
      toast.success('Sent for signature')
      // For in_person ceremonies the operator stays on the device
      // and walks through the signers via the tablet route, rather
      // than emailing magic-links.
      if (signingMode === 'in_person' && (provider === 'internal' || provider.startsWith('qes_'))) {
        navigate({ to: '/sign/in-person/$requestId', params: { requestId: req.id } })
        return
      }
      navigate({ to: '/admin/integrations' })
    },
    // Turn-1 follow-up: the old `e.message || 'Send failed'` only
    // surfaced thrown JS-Error messages and missed axios envelopes
    // entirely. readErrorMessage handles both — backend's
    // `{ error: "..." }` envelope wins; the local thrown-Error
    // fallback ('no versions yet — upload first…') still surfaces
    // via the Error.message branch readErrorMessage doesn't touch
    // (we keep e.message as the secondary fallback).
    onError: (e: unknown) =>
      toast.error(
        readErrorMessage(e) ??
        (e instanceof Error ? e.message : null) ??
        'Send failed',
      ),
  })

  const updateRecipient = (i: number, patch: Partial<RecipientForm>) => {
    setRecipients((rs) => rs.map((r, idx) => idx === i ? { ...r, ...patch } : r))
  }
  const addRecipient = () => setRecipients((rs) => [...rs, { email: '', name: '', role: 'signer', embedded: false }])
  const removeRecipient = (i: number) => setRecipients((rs) => rs.filter((_, idx) => idx !== i))

  const PROVIDER_OPTIONS: { id: SignatureProvider; label: string }[] = [
    { id: 'internal',    label: 'In-app signature (SeDoc)' },
    { id: 'docusign',    label: 'Send via DocuSign' },
    { id: 'adobe_sign',  label: 'Send via Adobe Sign' },
  ]

  const canSend = recipients.length > 0
    && recipients.every((r) => r.email && r.name)
    && !!versionId
    && !versionsQ.isLoading

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader title="Send for signature" description={`Document ${documentId}`} />

      <section className="mb-6 rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-lg font-semibold">Signing mode</h2>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3" data-testid="signing-mode-selector">
          {([
            { id: 'remote', label: 'Remote', help: 'Per-signer email link, separate devices.' },
            { id: 'mobile', label: 'Mobile', help: 'Same email link, mobile-optimized capture.' },
            { id: 'in_person', label: 'In-person', help: 'Single tablet, sequential signer + witness.' },
          ] as { id: SigningMode; label: string; help: string }[]).map((m) => (
            <button
              key={m.id}
              onClick={() => setSigningMode(m.id)}
              className={`rounded-md border p-3 text-start transition-colors ${
                signingMode === m.id
                  ? 'border-primary bg-primary/10'
                  : 'border-border hover:border-primary'
              }`}
              data-testid={`signing-mode-${m.id}`}
              aria-pressed={signingMode === m.id}
            >
              <span className="block text-sm font-medium">{m.label}</span>
              <span className="block text-xs text-muted-foreground">{m.help}</span>
            </button>
          ))}
        </div>
        {signingMode === 'in_person' && (
          <p className="mt-3 text-xs text-muted-foreground" data-testid="in-person-help">
            In-person mode launches the tablet ceremony immediately after sending. Add a <strong>witness</strong> recipient if your workflow requires one.
          </p>
        )}
      </section>

      <section className="mb-6 rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-lg font-semibold">Provider</h2>
        <div className="grid gap-3 md:grid-cols-3" data-testid="esign-provider-selector">
          {PROVIDER_OPTIONS.map((p) => (
            <button
              key={p.id}
              onClick={() => setProvider(p.id)}
              className={`rounded-md border p-3 text-start transition-colors ${
                provider === p.id
                  ? 'border-primary bg-primary/10'
                  : 'border-border hover:border-primary'
              }`}
              data-testid={`esign-provider-${p.id}`}
              aria-pressed={provider === p.id}
            >
              <span className="text-sm font-medium">{p.label}</span>
            </button>
          ))}
        </div>
      </section>

      <section className="mb-6 rounded-lg border border-border bg-card p-4">
        <header className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">Recipients</h2>
          <Button size="sm" variant="outline" onClick={addRecipient} data-testid="add-recipient">
            <Plus className="me-1 h-4 w-4" /> Add
          </Button>
        </header>
        <ul className="space-y-2" data-testid="recipient-list">
          {recipients.map((r, i) => (
            <li key={i} className="flex items-center gap-2" data-testid={`recipient-row-${i}`}>
              <Input
                type="email" placeholder="email@example.com"
                value={r.email}
                onChange={(e) => updateRecipient(i, { email: e.target.value })}
                className="flex-1"
                data-testid={`recipient-email-${i}`}
              />
              <Input
                placeholder="Full name"
                value={r.name}
                onChange={(e) => updateRecipient(i, { name: e.target.value })}
                className="flex-1"
                data-testid={`recipient-name-${i}`}
              />
              <select
                value={r.role}
                onChange={(e) => updateRecipient(i, { role: e.target.value as RecipientForm['role'] })}
                className="h-9 rounded-md border border-border bg-background px-2 text-sm"
                data-testid={`recipient-role-${i}`}
              >
                <option value="signer">Signer</option>
                <option value="witness">Witness</option>
                <option value="approver">Approver</option>
                <option value="cc">CC</option>
              </select>
              <Button size="sm" variant="ghost" onClick={() => removeRecipient(i)} aria-label="Remove" data-testid={`recipient-remove-${i}`}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      </section>

      <section className="mb-6 rounded-lg border border-border bg-card p-4">
        <Input
          placeholder="Subject"
          value={subject} onChange={(e) => setSubject(e.target.value)}
          className="mb-2" data-testid="esign-subject"
        />
        <textarea
          placeholder="Message to recipients (optional)"
          value={message} onChange={(e) => setMessage(e.target.value)}
          className="w-full rounded-md border border-border bg-background p-2 text-sm"
          rows={3}
          data-testid="esign-message"
        />
      </section>

      <Button
        onClick={() => sendMut.mutate()}
        loading={sendMut.isPending}
        disabled={!canSend}
        data-testid="esign-send"
      >
        <Send className="me-1 h-4 w-4" /> Send
      </Button>
    </div>
  )
}

async function fetchDocumentBytes(documentId: string): Promise<Blob> {
  const res = await fetch(`/api/v1/documents/${documentId}/content`, { credentials: 'include' })
  if (!res.ok) throw new Error('Could not load document bytes')
  return res.blob()
}

async function blobToBase64(b: Blob): Promise<string> {
  const buf = await b.arrayBuffer()
  let bin = ''
  const view = new Uint8Array(buf)
  for (let i = 0; i < view.byteLength; i++) bin += String.fromCharCode(view[i])
  return btoa(bin)
}
