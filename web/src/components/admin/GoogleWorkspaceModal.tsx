import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Copy, Check } from 'lucide-react'

import {
  getGoogleConnector, saveGoogleConfig, getGoogleAuthURL,
} from '@/api/connectors'
import { DriveImportPanel } from './DriveImportPanel'
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

// Setup walkthrough on the modal so the user doesn't have to leave the
// app to look up the exact button names in Google Cloud Console.
const STEPS = [
  'Go to console.cloud.google.com → APIs & Services → Credentials.',
  'Click "Create Credentials" → "OAuth client ID" → Application type: Web application.',
  'Under "Authorized redirect URIs", add the value shown below.',
  'Enable Gmail API + Google Drive API under "Enabled APIs & services".',
  'Copy the Client ID and Client Secret here, then Save & Authorize.',
]

export function GoogleWorkspaceModal({ open, onOpenChange, redirectURI, onSaved }: Props) {
  const qc = useQueryClient()

  // Reuses /connectors/google rather than a dedicated GET for the public
  // config — the list-shape carries authorized + sync_status which we
  // need to render. client_id is not in this view (we only show the
  // user's saved client_id when they re-open after a save).
  const existingQ = useQuery({
    queryKey: ['google-connector'],
    queryFn: getGoogleConnector,
    enabled: open,
  })

  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')

  useEffect(() => {
    // If a row exists we just show "Connected"; client_id stays blank
    // because the list endpoint doesn't return secrets.
    if (existingQ.data) {
      setClientID('')
    }
  }, [existingQ.data])

  const saveMut = useAppMutation({
    mutationFn: () => saveGoogleConfig({
      client_id: clientID.trim(),
      client_secret: clientSecret,
    }),
    onSuccess: async () => {
      toast.success('Credentials saved — redirecting to Google…')
      qc.invalidateQueries({ queryKey: ['google-connector'] })
      qc.invalidateQueries({ queryKey: ['connectors-list'] })
      try {
        const url = await getGoogleAuthURL()
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
          <DialogTitle>Connect Google Workspace</DialogTitle>
          <DialogDescription>
            Create an OAuth client in Google Cloud Console, then paste the credentials below.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <ol className="list-decimal space-y-1 ps-5 text-xs text-muted-foreground">
            {STEPS.map((s) => <li key={s}>{s}</li>)}
          </ol>

          <CopyField label="Authorized redirect URI (paste into Google Cloud Console)" value={redirectURI} />

          <div>
            <label className="mb-1 block text-sm font-medium">Client ID</label>
            <Input
              value={clientID}
              onChange={(e) => setClientID(e.target.value)}
              placeholder="000000000000-abc123def456.apps.googleusercontent.com"
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Client Secret</label>
            <Input
              type="password"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder="GOCSPX-…"
              autoComplete="new-password"
            />
          </div>

          {/* Import surface — appears once a connector row exists (i.e. the
              tenant has saved credentials + connected). The import call
              409s if tokens are missing, so showing it on a configured-
              but-not-yet-authorized connector degrades gracefully. */}
          {existingQ.data && <DriveImportPanel />}
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
