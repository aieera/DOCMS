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
import { PasswordInput } from '@/components/ui/PasswordInput'
import { copyText } from '@/lib/clipboard'
import { useEffect, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
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
// Step text lives in the catalogue (adm.cred.m_step1…6); this is just
// the order.
const STEPS = ['m_step1', 'm_step2', 'm_step3', 'm_step4', 'm_step5', 'm_step6'] as const

export function M365ConnectorModal({ open, onOpenChange, redirectURI, onSaved }: Props) {
  const { t } = useTranslation('common')
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
      toast.success(t('adm.cred.m365_redirecting'))
      qc.invalidateQueries({ queryKey: ['m365-connector'] })
      qc.invalidateQueries({ queryKey: ['connectors-list'] })
      try {
        const url = await getM365AuthURL()
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
          <DialogTitle>{t('adm.cred.m365_title')}</DialogTitle>
          <DialogDescription>
            <Trans
              i18nKey="adm.cred.m365_desc"
              ns="common"
              components={[<code key="p" dir="ltr" className="mx-1 rounded bg-muted px-1.5 py-0.5 text-xs" />]}
            />
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <ol className="list-decimal space-y-1 ps-5 text-xs text-muted-foreground">
            {STEPS.map((k) => <li key={k}>{t(`adm.cred.${k}`)}</li>)}
          </ol>

          <CopyField label={t('adm.cred.m365_redirect')} value={redirectURI} />

          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.cred.m365_app_id')}</label>
            <Input
              value={clientID}
              onChange={(e) => setClientID(e.target.value)}
              placeholder="00000000-0000-0000-0000-000000000000"
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.cred.m365_secret')}</label>
            <PasswordInput
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder="abc~1234567890…"
              autoComplete="new-password"
            />
            <p className="mt-1 text-xs text-muted-foreground">
              <Trans i18nKey="adm.cred.m365_secret_hint" ns="common" components={[<strong key="v" />]} />
            </p>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              {t('adm.cred.m365_tenant')} <span className="text-muted-foreground">{t('adm.cred.optional')}</span>
            </label>
            <Input
              value={entraTenant}
              onChange={(e) => setEntraTenant(e.target.value)}
              placeholder={t('adm.cred.m365_tenant_ph')}
              autoComplete="off"
              spellCheck={false}
            />
          </div>
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
