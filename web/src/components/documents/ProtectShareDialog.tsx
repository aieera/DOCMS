import { copyText } from '@/lib/clipboard'
import { useState } from 'react'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Copy, Link2, Plus, ShieldCheck, X } from 'lucide-react'
import { toast } from 'sonner'
import { useAppMutation } from '@/hooks/useAppMutation'
import {
  exportProtected,
  type AllowedAction,
  type ExportProtectedResponse,
  type RecipientType,
} from '@/api/irm'

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  documentId: string
  documentTitle: string
}

interface RecipientRow {
  type: RecipientType
  ref: string
}

// Default expiry: 7 days out, formatted for a <input type="datetime-local">.
function defaultExpiryLocal(): string {
  const d = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000)
  // datetime-local wants `YYYY-MM-DDTHH:mm` in local time.
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}`
  )
}

export function ProtectShareDialog({ open, onOpenChange, documentId, documentTitle }: Props) {
  const [recipients, setRecipients] = useState<RecipientRow[]>([{ type: 'email', ref: '' }])
  const [expiresLocal, setExpiresLocal] = useState(defaultExpiryLocal)
  // View is always granted; print/download are opt-in.
  const [allowPrint, setAllowPrint] = useState(false)
  const [allowDownload, setAllowDownload] = useState(false)
  const [result, setResult] = useState<ExportProtectedResponse | null>(null)

  const reset = () => {
    setRecipients([{ type: 'email', ref: '' }])
    setExpiresLocal(defaultExpiryLocal())
    setAllowPrint(false)
    setAllowDownload(false)
    setResult(null)
  }

  const create = useAppMutation({
    mutationFn: () => {
      const cleaned = recipients
        .map((r) => ({ type: r.type, ref: r.ref.trim() }))
        .filter((r) => r.ref !== '')
      const actions: AllowedAction[] = ['view']
      if (allowPrint) actions.push('print')
      if (allowDownload) actions.push('download')
      return exportProtected({
        document_id: documentId,
        recipients: cleaned,
        // datetime-local has no timezone; new Date() interprets it as
        // local time and toISOString() normalizes to UTC for the API.
        expires_at: new Date(expiresLocal).toISOString(),
        allowed_actions: actions,
      })
    },
    onSuccess: (data) => {
      setResult(data)
      toast.success('Protected container created')
    },
    defaultErrorMessage: 'Could not create the protected container',
  })

  const handleSubmit = () => {
    const hasRecipient = recipients.some((r) => r.ref.trim() !== '')
    if (!hasRecipient) {
      toast.error('Add at least one recipient')
      return
    }
    if (!expiresLocal) {
      toast.error('Set an expiry')
      return
    }
    create.mutate()
  }

  const setRow = (i: number, patch: Partial<RecipientRow>) =>
    setRecipients((rows) => rows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
  const addRow = () => setRecipients((rows) => [...rows, { type: 'email', ref: '' }])
  const removeRow = (i: number) =>
    setRecipients((rows) => (rows.length <= 1 ? rows : rows.filter((_, idx) => idx !== i)))

  const copy = (text: string) => {
    copyText(text)
    toast.success('Link copied to clipboard')
  }

  const handleOpenChange = (o: boolean) => {
    if (!o) reset()
    onOpenChange(o)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={handleOpenChange}
      size="lg"
      title={`Protect & share "${documentTitle}"`}
    >
      {result ? (
        <div className="space-y-4">
          <div className="rounded-md border border-emerald-500/40 bg-emerald-50/60 p-3 text-xs dark:bg-emerald-950/20">
            <div className="flex items-start gap-2">
              <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-emerald-700 dark:text-emerald-300" />
              <div className="text-emerald-900 dark:text-emerald-100">
                <p className="font-semibold">Deliver these links to your recipients</p>
                <p className="mt-1 text-emerald-800 dark:text-emerald-200">
                  Each link is bound to one recipient. Opening a link requires an online
                  license check that validates the recipient, logs the open, and can be
                  revoked at any time from Protected exports — a revoked license blocks the
                  next open. This controls and audits access; it does not prevent
                  screenshots.
                </p>
              </div>
            </div>
          </div>

          <div className="space-y-2">
            {result.licenses.map((lic) => {
              const url = `${window.location.origin}${lic.open_url}`
              return (
                <div
                  key={lic.license_id}
                  data-testid="irm-license-result"
                  className="rounded-md border border-[var(--color-border)] bg-slate-50 p-2 dark:bg-slate-800"
                >
                  <div className="mb-1 text-xs text-muted-foreground">
                    {lic.recipient_type === 'user' ? 'User' : 'Email'}:{' '}
                    <span className="font-medium text-foreground">{lic.recipient_ref}</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <Link2 className="h-4 w-4 shrink-0 text-[var(--color-primary)]" />
                    <code className="flex-1 truncate text-xs">{url}</code>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => copy(url)}
                      aria-label="Copy protected link"
                    >
                      <Copy className="h-4 w-4" />
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>

          <Button variant="outline" className="w-full" onClick={reset}>
            Protect another share
          </Button>
        </div>
      ) : (
        <div className="space-y-4">
          <div className="rounded-md border border-blue-500/40 bg-blue-50/60 p-3 text-xs dark:bg-blue-950/20">
            <div className="flex items-start gap-2">
              <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-blue-600" />
              <div className="text-muted-foreground">
                <p className="font-semibold text-foreground">Protected container</p>
                <p className="mt-1">
                  Seals the document into an encrypted container and issues a per-recipient
                  license. Recipients open it in the browser; every open makes an online
                  license check that you can revoke. Access control and audit — not
                  screenshot prevention.
                </p>
              </div>
            </div>
          </div>

          <div>
            <div className="mb-1.5 text-sm font-medium">Recipients</div>
            <div className="space-y-2">
              {recipients.map((r, i) => (
                <div key={i} className="flex items-center gap-2">
                  <div className="w-28 shrink-0">
                    <Select
                      value={r.type}
                      onValueChange={(v) => setRow(i, { type: v as RecipientType })}
                      options={[
                        { value: 'email', label: 'Email' },
                        { value: 'user', label: 'User ID' },
                      ]}
                    />
                  </div>
                  <Input
                    className="flex-1"
                    value={r.ref}
                    onChange={(e) => setRow(i, { ref: e.target.value })}
                    placeholder={
                      r.type === 'email' ? 'someone@example.com' : 'user UUID'
                    }
                    type={r.type === 'email' ? 'email' : 'text'}
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => removeRow(i)}
                    disabled={recipients.length <= 1}
                    aria-label="Remove recipient"
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
            <Button variant="ghost" size="sm" className="mt-2 gap-1" onClick={addRow}>
              <Plus className="h-4 w-4" /> Add recipient
            </Button>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium" htmlFor="irm-expiry">
              Expires
            </label>
            <Input
              id="irm-expiry"
              type="datetime-local"
              value={expiresLocal}
              onChange={(e) => setExpiresLocal(e.target.value)}
            />
          </div>

          <div>
            <div className="mb-1.5 text-sm font-medium">Allowed actions</div>
            <label className="flex items-center gap-2 text-sm text-muted-foreground">
              <input type="checkbox" checked disabled />
              View (always granted)
            </label>
            <label className="mt-1.5 flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={allowPrint}
                onChange={(e) => setAllowPrint(e.target.checked)}
              />
              Print
            </label>
            <label className="mt-1.5 flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={allowDownload}
                onChange={(e) => setAllowDownload(e.target.checked)}
              />
              Download
            </label>
          </div>

          <Button onClick={handleSubmit} disabled={create.isPending} className="w-full">
            {create.isPending ? 'Protecting…' : 'Protect & share'}
          </Button>
        </div>
      )}
    </Dialog>
  )
}
