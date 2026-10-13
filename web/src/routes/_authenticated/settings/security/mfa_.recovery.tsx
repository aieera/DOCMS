// ADR 0063 — recovery codes display + regenerate. The codes
import { Trans, useTranslation } from 'react-i18next'
// themselves are issued by /auth/mfa/setup (existing TOTP setup
// flow) and on regenerate. Server returns plaintext codes EXACTLY
// ONCE; we render them and prompt the user to copy them.
import { copyText } from '@/lib/clipboard'
import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Copy, RotateCcw, AlertTriangle } from 'lucide-react'
import { api, readErrorMessage } from '@/api/client'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

export const Route = createFileRoute('/_authenticated/settings/security/mfa_/recovery')({
  component: RecoveryCodesPage,
})

interface RegenerateResponse {
  recovery_codes: string[]
}

async function regenerateRecoveryCodes(): Promise<string[]> {
  // Reuses the existing /auth/mfa/setup flow for now — calling
  // setup again rotates the codes when MFA is already enabled.
  const { data } = await api.post<RegenerateResponse>('/auth/mfa/setup')
  return data.recovery_codes ?? []
}

function RecoveryCodesPage() {
  const { t } = useTranslation('common')
  const [codes, setCodes] = useState<string[] | null>(null)
  const regen = useAppMutation({
    mutationFn: regenerateRecoveryCodes,
    onSuccess: (cs) => {
      setCodes(cs)
      toast.success(t('settings.recovery.generated'))
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not regenerate recovery codes'),
  })

  const copyAll = () => {
    if (!codes) return
    copyText(codes.join('\n'))
    toast.success(t('settings.recovery.copied'))
  }

  return (
    <div className="mx-auto max-w-2xl space-y-4">
      <Link to="/settings/security/mfa" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:underline">
        <DirectionalIcon name="ArrowLeft" className="h-3 w-3" /> {t('settings.recovery.back')}
      </Link>
      <PageHeader
        title={t('settings.recovery.title')}
        description={t('settings.recovery.description')}
      />

      <div className="flex items-start gap-2 rounded border border-warning/40 bg-warning/10 p-3 text-sm text-warning-strong">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
        <div>
          {/* Trans, not string concatenation: the emphasised clause sits
              mid-sentence and Arabic puts it in a different position, so
              the <strong> has to travel with the translation. */}
          <Trans
            i18nKey="settings.recovery.warning"
            ns="common"
            components={{ 1: <strong /> }}
          />
        </div>
      </div>

      <div className="rounded-lg border border-border bg-card p-6">
        {!codes && !regen.isPending && (
          <Button onClick={() => regen.mutate()}>
            <RotateCcw className="h-4 w-4" /> {t('settings.recovery.generate')}
          </Button>
        )}

        {regen.isPending && (
          <div className="flex items-center gap-2 text-sm">
            <Spinner /> {t('settings.recovery.generating')}
          </div>
        )}

        {codes && (
          <>
            <p className="mb-3 text-sm">
              {t('settings.recovery.save_now')}
            </p>
            <ul
              data-testid="recovery-codes-list"
              className="grid grid-cols-2 gap-2 rounded border border-border bg-muted p-3 font-mono text-sm"
            >
              {codes.map((c) => (
                <li key={c} className="select-all">{c}</li>
              ))}
            </ul>
            <div className="mt-4 flex gap-2">
              <Button variant="default" onClick={copyAll}>
                <Copy className="h-4 w-4" /> {t('settings.recovery.copy_all')}
              </Button>
              <Button variant="ghost" onClick={() => regen.mutate()}>
                <RotateCcw className="h-4 w-4" /> {t('settings.recovery.regenerate')}
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
