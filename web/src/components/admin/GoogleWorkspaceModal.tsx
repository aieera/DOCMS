import { PasswordInput } from '@/components/ui/PasswordInput'
import { copyText } from '@/lib/clipboard'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
// Step text lives in the catalogue (adm.cred.g_step1…5); this is just
// the order.
const STEPS = ['g_step1', 'g_step2', 'g_step3', 'g_step4', 'g_step5'] as const

export function GoogleWorkspaceModal({ open, onOpenChange, redirectURI, onSaved }: Props) {
  const { t } = useTranslation('common')
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
      toast.success(t('adm.cred.google_redirecting'))
      qc.invalidateQueries({ queryKey: ['google-connector'] })
      qc.invalidateQueries({ queryKey: ['connectors-list'] })
      try {
        const url = await getGoogleAuthURL()
        window.location.href = url
      } catch (e) {
        const detail = (e as { response?: { data?: { error?: string } } }).response?.data?.error
        toast.error(detail || t('adm.cred.err_oauth'))
      }
      await onSaved()
    },
    onError: (e: Error) => toast.error(e.message || t('adm.cred.err_save')),
  })

  const canSubmit = clientID.trim() !== '' && clientSecret !== '' && !saveMut.isPending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('adm.cred.google_title')}</DialogTitle>
          <DialogDescription>
            {t('adm.cred.google_desc')}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <ol className="list-decimal space-y-1 ps-5 text-xs text-muted-foreground">
            {STEPS.map((k) => <li key={k}>{t(`adm.cred.${k}`)}</li>)}
          </ol>

          <CopyField label={t('adm.cred.google_redirect')} value={redirectURI} />

          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.cred.client_id')}</label>
            <Input
              value={clientID}
              onChange={(e) => setClientID(e.target.value)}
              placeholder="000000000000-abc123def456.apps.googleusercontent.com"
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.cred.client_secret')}</label>
            <PasswordInput
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
          <Button variant="ghost" onClick={() => onOpenChange(false)}>{t('adm.cred.cancel')}</Button>
          <Button onClick={() => saveMut.mutate()} disabled={!canSubmit}>
            {saveMut.isPending ? t('adm.cred.saving') : t('adm.cred.save_authorize')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function CopyField({ label, value }: { label: string; value: string }) {
  const { t } = useTranslation('common')
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
            copyText(value).then(() => {
              setCopied(true)
              setTimeout(() => setCopied(false), 1500)
            })
          }}
          aria-label={t('adm.cred.copy')}
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </Button>
      </div>
    </div>
  )
}
