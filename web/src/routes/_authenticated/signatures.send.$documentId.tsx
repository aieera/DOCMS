import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Send, Trash2, Plus } from 'lucide-react'

import {
  createRequest, sendViaESign,
  type ESignProvider, type SignatureProvider,
} from '@/api/signatures'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'

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
// "Internal" routing is the pre-existing in-app signature flow and
// shows up alongside DocuSign / Adobe Sign so the user has one
// place to dispatch any kind of envelope.

export const Route = createFileRoute('/_authenticated/signatures/send/$documentId')({
  component: SendForSignaturePage,
})

interface RecipientForm {
  email: string
  name: string
  role: 'signer' | 'approver' | 'cc'
  embedded: boolean
}

function SendForSignaturePage() {
  const { documentId } = Route.useParams()
  const navigate = useNavigate()
  const [provider, setProvider] = useState<SignatureProvider>('docusign')
  const [recipients, setRecipients] = useState<RecipientForm[]>([
    { email: '', name: '', role: 'signer', embedded: false },
  ])
  const [subject, setSubject] = useState('Please sign')
  const [message, setMessage] = useState('')
  const [versionId] = useState<string>(documentId) // version equals document for the simple case

  const sendMut = useMutation({
    mutationFn: async () => {
      // 1. Mint signature request.
      const req = await createRequest({
        document_id: documentId, version_id: versionId,
        provider,
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
    onSuccess: () => {
      toast.success('Sent for signature')
      navigate({ to: '/admin/integrations' })
    },
    onError: (e: Error) => toast.error(e.message || 'Send failed'),
  })

  const updateRecipient = (i: number, patch: Partial<RecipientForm>) => {
    setRecipients((rs) => rs.map((r, idx) => idx === i ? { ...r, ...patch } : r))
  }
  const addRecipient = () => setRecipients((rs) => [...rs, { email: '', name: '', role: 'signer', embedded: false }])
  const removeRecipient = (i: number) => setRecipients((rs) => rs.filter((_, idx) => idx !== i))

  const PROVIDER_OPTIONS: { id: SignatureProvider; label: string }[] = [
    { id: 'internal',    label: 'In-app signature (VaultDMS)' },
    { id: 'docusign',    label: 'Send via DocuSign' },
    { id: 'adobe_sign',  label: 'Send via Adobe Sign' },
  ]

  const canSend = recipients.length > 0 && recipients.every((r) => r.email && r.name)

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader title="Send for signature" description={`Document ${documentId}`} />

      <section className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
        <h2 className="mb-3 text-lg font-semibold">Provider</h2>
        <div className="grid gap-3 md:grid-cols-3" data-testid="esign-provider-selector">
          {PROVIDER_OPTIONS.map((p) => (
            <button
              key={p.id}
              onClick={() => setProvider(p.id)}
              className={`rounded-md border p-3 text-start transition-colors ${
                provider === p.id
                  ? 'border-[var(--color-primary)] bg-[var(--color-primary)]/10'
                  : 'border-[var(--color-border)] hover:border-[var(--color-primary)]'
              }`}
              data-testid={`esign-provider-${p.id}`}
              aria-pressed={provider === p.id}
            >
              <span className="text-sm font-medium">{p.label}</span>
            </button>
          ))}
        </div>
      </section>

      <section className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
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
                className="h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 text-sm"
                data-testid={`recipient-role-${i}`}
              >
                <option value="signer">Signer</option>
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

      <section className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
        <Input
          placeholder="Subject"
          value={subject} onChange={(e) => setSubject(e.target.value)}
          className="mb-2" data-testid="esign-subject"
        />
        <textarea
          placeholder="Message to recipients (optional)"
          value={message} onChange={(e) => setMessage(e.target.value)}
          className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-2 text-sm"
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
