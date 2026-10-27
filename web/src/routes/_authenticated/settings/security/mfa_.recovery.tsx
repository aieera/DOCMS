// ADR 0063 — recovery codes display + regenerate. The server returns the
// plaintext codes EXACTLY ONCE; we render them and prompt the user to copy
// them.
//
// DEF-01: regenerating used to re-run POST /auth/mfa/setup, which replaced
// the TOTP secret of an ENABLED factor and locked the user out of their
// authenticator app. It now posts the current authenticator code to
// /auth/mfa/recovery-codes, which changes only the recovery codes. Never
// call /auth/mfa/setup from here.
import { Trans, useTranslation } from 'react-i18next'
import { copyText } from '@/lib/clipboard'
import { useId, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Copy, RotateCcw, AlertTriangle } from 'lucide-react'
import { readErrorMessage } from '@/api/client'
import { regenerateRecoveryCodes } from '@/api/security'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

export const Route = createFileRoute('/_authenticated/settings/security/mfa_/recovery')({
  component: RecoveryCodesPage,
})

const SIX_DIGITS = /^\d{6}$/

function errorFor(e: unknown, t: (key: string) => string): string {
  const res = (e as { response?: { status?: number; data?: { field?: string } } } | null)?.response
  if (res?.status === 400 && res.data?.field === 'totp_code') return t('settings.recovery.code_invalid')
  if (res?.status === 409) return t('settings.recovery.mfa_off')
  return readErrorMessage(e) ?? t('settings.recovery.failed')
}

function RecoveryCodesPage() {
  const { t } = useTranslation('common')
  const codeId = useId()
  const [codes, setCodes] = useState<string[] | null>(null)
  const [totpCode, setTotpCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const regen = useAppMutation({
    mutationFn: (code: string) => regenerateRecoveryCodes(code),
    onSuccess: (cs) => {
      setCodes(cs)
      setTotpCode('')
      setError(null)
      toast.success(t('settings.recovery.generated'))
    },
    onError: (e: unknown) => setError(errorFor(e, t)),
  })

  const copyAll = () => {
    if (!codes) return
    copyText(codes.join('\n'))
    toast.success(t('settings.recovery.copied'))
  }

  // Each regeneration needs a fresh authenticator code (codes are
  // single-use), so "Regenerate" goes back to the code step.
  const startOver = () => {
    setCodes(null)
    setTotpCode('')
    setError(null)
  }

  const canSubmit = SIX_DIGITS.test(totpCode) && !regen.isPending

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
        {!codes && (
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              if (canSubmit) regen.mutate(totpCode)
            }}
          >
            <div className="max-w-xs space-y-1">
              <label htmlFor={codeId} className="block text-sm font-medium">
                {t('settings.recovery.code_label')}
              </label>
              <Input
                id={codeId}
                value={totpCode}
                onChange={(e) => {
                  setTotpCode(e.target.value.replace(/\D/g, '').slice(0, 6))
                  setError(null)
                }}
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                autoFocus
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `${codeId}-hint ${codeId}-error` : `${codeId}-hint`}
                data-testid="recovery-totp-code"
              />
              <p id={`${codeId}-hint`} className="text-xs text-muted-foreground">
                {t('settings.recovery.code_hint')}
              </p>
              {error && (
                <p id={`${codeId}-error`} role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              )}
            </div>
            <Button type="submit" disabled={!canSubmit}>
              {regen.isPending ? <Spinner /> : <RotateCcw className="h-4 w-4" />}{' '}
              {regen.isPending ? t('settings.recovery.generating') : t('settings.recovery.generate')}
            </Button>
          </form>
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
              <Button variant="ghost" onClick={startOver}>
                <RotateCcw className="h-4 w-4" /> {t('settings.recovery.regenerate')}
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
