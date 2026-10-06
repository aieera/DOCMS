import { PasswordInput } from '@/components/ui/PasswordInput'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'

import { getSMTPConfig, saveSMTPConfig, testSMTP } from '@/api/notif-providers'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void | Promise<void>
}

export function SMTPCredentialsModal({ open, onOpenChange, onSaved }: Props) {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const existingQ = useQuery({
    queryKey: ['smtp-config'],
    queryFn: getSMTPConfig,
    enabled: open,
  })

  const [host, setHost] = useState('')
  const [port, setPort] = useState(587)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [fromAddr, setFromAddr] = useState('')
  const [startTLS, setStartTLS] = useState(true)
  const [testEmail, setTestEmail] = useState('')

  useEffect(() => {
    if (existingQ.data) {
      setHost(existingQ.data.host)
      setPort(existingQ.data.port)
      setUsername(existingQ.data.username)
      setFromAddr(existingQ.data.from_addr)
      setStartTLS(existingQ.data.starttls)
    }
  }, [existingQ.data])

  const hasExistingPassword = !!existingQ.data?.has_password

  const saveMut = useAppMutation({
    mutationFn: () => saveSMTPConfig({
      host: host.trim(),
      port,
      username: username.trim(),
      password,
      from_addr: fromAddr.trim(),
      starttls: startTLS,
    }),
    onSuccess: async () => {
      toast.success(t('adm.cred.smtp_saved'))
      qc.invalidateQueries({ queryKey: ['smtp-config'] })
      await onSaved()
    },
    onError: (e: Error) => toast.error(e.message || t('adm.cred.err_save')),
  })

  const testMut = useAppMutation({
    mutationFn: () => testSMTP(testEmail),
    onSuccess: () => toast.success(t('adm.cred.test_email_sent', { to: testEmail })),
    onError: (e: Error) => toast.error(e.message || t('adm.cred.err_test')),
  })

  const canSubmit = host.trim() !== ''
    && fromAddr.trim() !== ''
    && port > 0
    && !saveMut.isPending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('adm.cred.smtp_title')}</DialogTitle>
          <DialogDescription>
            {t('adm.cred.smtp_desc')}
            {hasExistingPassword && t('adm.cred.smtp_keep_password')}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
            <div className="sm:col-span-2">
              <label className="mb-1 block text-sm font-medium">{t('adm.cred.host')}</label>
              <Input
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="sandbox.smtp.mailtrap.io"
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            <div>
              <label className="mb-1 block text-sm font-medium">{t('adm.cred.port')}</label>
              <Input
                type="number"
                value={port}
                onChange={(e) => setPort(Number(e.target.value))}
                placeholder="587"
              />
            </div>
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.cred.username')}</label>
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium">
              {t('adm.cred.password')}
              {hasExistingPassword && <span className="ms-2 text-xs text-muted-foreground">{t('adm.cred.stored_hint')}</span>}
            </label>
            <PasswordInput
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={hasExistingPassword ? t('adm.cred.masked') : t('adm.cred.paste_once')}
              autoComplete="new-password"
            />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.cred.from_addr')}</label>
            <Input
              value={fromAddr}
              onChange={(e) => setFromAddr(e.target.value)}
              placeholder="noreply@vaultdms.local"
              autoComplete="off"
              spellCheck={false}
            />
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={startTLS}
              onChange={(e) => setStartTLS(e.target.checked)}
            />
            {t('adm.cred.starttls')}
          </label>

          {hasExistingPassword && (
            <div className="rounded-md border border-border bg-muted/30 p-3">
              <p className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">{t('adm.cred.send_test_email')}</p>
              <div className="flex gap-2">
                <Input
                  type="email"
                  value={testEmail}
                  onChange={(e) => setTestEmail(e.target.value)}
                  placeholder="you@example.com"
                />
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => testMut.mutate()}
                  disabled={!testEmail.includes('@') || testMut.isPending}
                >
                  {testMut.isPending ? t('adm.cred.sending') : t('adm.cred.send_test')}
                </Button>
              </div>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>{t('adm.cred.cancel')}</Button>
          <Button onClick={() => saveMut.mutate()} disabled={!canSubmit}>
            {saveMut.isPending ? t('adm.cred.saving') : t('adm.cred.saved')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
