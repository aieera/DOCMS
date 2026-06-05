// M365ConnectorModal — ADR 0111.
//
// Sibling of GoogleWorkspaceModal. The flow is intentionally identical
// except for two M365-specific knobs:
//
//   * Authorized redirect URI — the customer pastes this into the
//     Entra app's "Authentication → Web → Redirect URIs" list. Same
//     value as Google's (single shared /connectors/oauth/callback).
//
//   * Entra directory tenant — leave blank to use the multi-tenant
//     `common` endpoint (default for SaaS); paste a directory GUID
//     when the customer hosts their own single-tenant Entra app.
//
// Why a separate component and not a generic ConnectorModal? The
// fields, instruction copy, and the Entra-tenant control diverge
// enough that the abstraction noise would outweigh the dedup win.
// We'll revisit when Salesforce / ServiceNow / Workday land and
// the 4th copy of the same wrapper proves the pattern is real.
import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Copy, Check } from 'lucide-react'

import {
  getM365Connector, saveM365Config, getM365AuthURL,
} from '@/api/connectors'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  redirectURI: string
  onSaved: () => void | Promise<void>
}

// Step list mirrors the howto in docs/howto/connectors-m365.md so an
// admin who follows the modal AND the runbook gets a consistent
// experience. Update both when the Entra UI changes its labels.
const STEPS = [
  'In Azure Portal → Microsoft Entra ID → App registrations → New registration.',
  'Application type: Web. Account types: "Accounts in any organizational directory (multitenant)" for SaaS, otherwise single tenant.',
  'Under "Authentication", add the redirect URI shown below.',
  'Under "API permissions", add the Graph delegated scopes: User.Read, Files.ReadWrite, Mail.ReadWrite, Mail.Send, Sites.ReadWrite.All, Group.Read.All, ChannelMessage.Send. Grant admin consent.',
  'Under "Certificates & secrets", create a client secret and copy its VALUE (not the secret ID).',
  'Copy the Application (client) ID and the secret value here. Optional: paste the Directory (tenant) ID for single-tenant apps.',
]

export function M365ConnectorModal({ open, onOpenChange, redirectURI, onSaved }: Props) {
  const qc = useQueryClient()

  const existingQ = useQuery({
    queryKey: ['m365-connector'],
    queryFn: getM365Connector,
    enabled: open,
  })

  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [entraTenant, setEntraTenant] = useState('')

  useEffect(() => {
    // Reset the form whenever the modal re-opens against a (possibly
    // newly-loaded) existing config row. Secrets never round-trip.
    if (existingQ.data) {
      setClientID('')
      setClientSecret('')
      setEntraTenant('')
    }
  }, [existingQ.data])

  const saveMut = useAppMutation({
    mutationFn: () => saveM365Config({
      client_id:     clientID.trim(),
      client_secret: clientSecret,
      entra_tenant:  entraTenant.trim() || undefined,
    }),
    onSuccess: async () => {
      toast.success('Credentials saved — redirecting to Microsoft…')
      qc.invalidateQueries({ queryKey: ['m365-connector'] })
      qc.invalidateQueries({ queryKey: ['connectors-list'] })
      try {
        const url = await getM365AuthURL()
        window.location.href = url
      } catch (e) {
        const detail = (e as { response?: { data?: { error?: string } } }).response?.data?.error
        toast.error(detail || 'Could not start OAuth')
      }
      await onSaved()
    },
    onError: (e: Error) => toast.error(e.message || 'Save failed'),
  })

  const canSubmit = clientID.trim() !== '' && clientSecret !== '' && !saveMut.isPending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Connect Microsoft 365</DialogTitle>
          <DialogDescription>
            Register an Entra ID app, then paste the credentials below. Full instructions live in
            <code className="mx-1 rounded bg-muted px-1.5 py-0.5 text-xs">docs/howto/connectors-m365.md</code>.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <ol className="list-decimal space-y-1 ps-5 text-xs text-muted-foreground">
            {STEPS.map((s) => <li key={s}>{s}</li>)}
          </ol>

          <CopyField label="Authorized redirect URI (paste into Entra → Authentication)" value={redirectURI} />

          <div>
            <label className="mb-1 block text-sm font-medium">Application (client) ID</label>
            <Input
              value={clientID}
              onChange={(e) => setClientID(e.target.value)}
              placeholder="00000000-0000-0000-0000-000000000000"
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Client secret VALUE</label>
            <Input
              type="password"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder="abc~1234567890…"
              autoComplete="new-password"
            />
            <p className="mt-1 text-xs text-muted-foreground">
              Copy the <strong>value</strong>, not the secret ID. Entra only shows the value once at creation.
            </p>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Directory (tenant) ID <span className="text-muted-foreground">— optional</span>
            </label>
            <Input
              value={entraTenant}
              onChange={(e) => setEntraTenant(e.target.value)}
              placeholder="leave blank to use the multi-tenant 'common' endpoint"
              autoComplete="off"
              spellCheck={false}
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={() => saveMut.mutate()} disabled={!canSubmit}>
            {saveMut.isPending ? 'Saving…' : 'Save & Authorize'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function CopyField({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div>
      <label className="mb-1 block text-xs uppercase tracking-wide text-muted-foreground">{label}</label>
      <div className="flex items-center gap-2">
        <code className="flex-1 truncate rounded border border-border bg-muted/40 px-2 py-1 text-xs">{value}</code>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            navigator.clipboard.writeText(value).then(() => {
              setCopied(true)
              setTimeout(() => setCopied(false), 1500)
            })
          }}
          aria-label="Copy"
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </Button>
      </div>
    </div>
  )
}
