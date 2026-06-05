import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Copy, Check } from 'lucide-react'

import {
  getESignProviderConfig,
  saveESignProviderConfig,
  type ESignProvider,
} from '@/api/signatures'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

// Renders the credentials modal for one provider. Triggered from the
// /admin/integrations "Connect" / "Edit credentials" buttons.
//
// On save we POST credentials, then (if the parent asked) kick off the
// OAuth start so the user lands directly on the vendor consent page —
// one click feels like one continuous flow instead of save → connect.
interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  provider: ESignProvider
  // Called after credentials are saved successfully. Parent uses this
  // to fire off startESignOAuth so the browser redirects to DocuSign.
  onSaved: () => void | Promise<void>
  // Hardcoded redirect URI + webhook URL strings the user pastes into
  // the vendor portal. Parent computes them from window.location.
  redirectURI: string
  webhookURL: string
}

const REGION_OPTIONS: { value: string; label: string }[] = [
  { value: 'na1', label: 'NA (North America)' },
  { value: 'eu1', label: 'EU (Europe)' },
  { value: 'jp1', label: 'JP (Japan)' },
]

export function ESignCredentialsModal({ open, onOpenChange, provider, onSaved, redirectURI, webhookURL }: Props) {
  const qc = useQueryClient()
  const isAdobe = provider === 'adobe_sign'
  const label = provider === 'docusign' ? 'DocuSign' : 'Adobe Sign'

  // Pre-fill from any previously saved config — except the secret,
  // which never travels back from the server.
  const existingQ = useQuery({
    queryKey: ['esign-provider-config', provider],
    queryFn: () => getESignProviderConfig(provider),
    enabled: open,
  })

  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [environment, setEnvironment] = useState<'sandbox' | 'production'>('sandbox')
  const [region, setRegion] = useState('na1')

  useEffect(() => {
    if (existingQ.data) {
      setClientId(existingQ.data.client_id)
      setEnvironment(existingQ.data.environment)
      if (existingQ.data.region) setRegion(existingQ.data.region)
    }
  }, [existingQ.data])

  const saveMut = useAppMutation({
    mutationFn: () => saveESignProviderConfig(provider, {
      client_id: clientId.trim(),
      client_secret: clientSecret,
      environment,
      region: isAdobe ? region : undefined,
    }),
    onSuccess: async () => {
      toast.success('Credentials saved — redirecting to ' + label + '…')
      qc.invalidateQueries({ queryKey: ['esign-provider-config', provider] })
      qc.invalidateQueries({ queryKey: ['esign-connections'] })
      await onSaved()
    },
    onError: (e: Error) => toast.error(e.message || 'Save failed'),
  })

  const hasExistingSecret = !!existingQ.data?.has_secret
  const canSubmit = clientId.trim() !== '' && (hasExistingSecret || clientSecret !== '') && !saveMut.isPending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Connect {label}</DialogTitle>
          <DialogDescription>
            Create an integration app in your {label} {environment} account, then paste the credentials here.
            {hasExistingSecret && ' Leave Secret Key blank to keep the existing one.'}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <CopyField label="Redirect URI (paste into the vendor portal)" value={redirectURI} />
          <CopyField label="Webhook URL (Connect/Webhooks setting)" value={webhookURL} />

          <div>
            <label className="mb-1 block text-sm font-medium">
              {provider === 'docusign' ? 'Integration Key' : 'Client ID'}
            </label>
            <Input
              value={clientId}
              onChange={(e) => setClientId(e.target.value)}
              placeholder={provider === 'docusign' ? '00000000-0000-0000-0000-000000000000' : 'your-client-id'}
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              {provider === 'docusign' ? 'Secret Key' : 'Client Secret'}
              {hasExistingSecret && <span className="ms-2 text-xs text-muted-foreground">(stored — paste a new one to replace)</span>}
            </label>
            <Input
              type="password"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder={hasExistingSecret ? '••••••••' : 'paste once — never re-shown'}
              autoComplete="new-password"
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Environment</label>
            <div className="flex gap-4 text-sm">
              <label className="flex cursor-pointer items-center gap-2">
                <input
                  type="radio"
                  name="esign-env"
                  value="sandbox"
                  checked={environment === 'sandbox'}
                  onChange={() => setEnvironment('sandbox')}
                />
                Sandbox / Demo
              </label>
              <label className="flex cursor-pointer items-center gap-2">
                <input
                  type="radio"
                  name="esign-env"
                  value="production"
                  checked={environment === 'production'}
                  onChange={() => setEnvironment('production')}
                />
                Production
              </label>
            </div>
          </div>

          {isAdobe && (
            <div>
              <label className="mb-1 block text-sm font-medium">Adobe Region</label>
              <select
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                className="h-9 w-full rounded-md border border-border bg-background px-3 text-sm"
              >
                {REGION_OPTIONS.map((opt) => (
                  <option key={opt.value} value={opt.value}>{opt.label}</option>
                ))}
              </select>
            </div>
          )}
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
