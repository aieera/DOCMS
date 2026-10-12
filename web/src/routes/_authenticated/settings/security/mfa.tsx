// ADR 0063 — MFA settings page. Lets the user enroll, verify, and
import { useTranslation } from 'react-i18next'
// disable each non-passkey method (passkeys land on /settings/security
// which already exists). Sections shown:
//   - Enrolled methods, strongest-first
//   - Email OTP enrollment + activation
//   - SMS enrollment + activation
//   - Push device list (mobile app needs to be installed first)
//
// Recovery codes display lives at /settings/security/mfa/recovery
// (separate route so the regenerate flow can hard-reload the codes
// without losing form state on this page).
import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import {
  ShieldCheck, Smartphone, Mail, MessageSquare, Bell,
  Trash2, Plus, AlertTriangle,
} from 'lucide-react'

import {
  listMyEnrolledMethods,
  enrollEmailMFA,
  enrollSMSMFA,
  verifyEmailEnrollment,
  verifySMSEnrollment,
  disableMFAMethod,
  type EnrolledMethod,
  type MFAMethod,
} from '@/api/mfa'
import { readErrorMessage } from '@/api/client'
import { useAuthStore } from '@/store/authStore'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import { Badge } from '@/components/ui/shadcn/badge'

export const Route = createFileRoute('/_authenticated/settings/security/mfa')({
  component: MFAPage,
})

const STRENGTH_LABEL: Record<number, string> = {
  5: 'Strongest',
  4: 'Strong',
  3: 'Strong',
  2: 'Medium',
  1: 'Weak',
}

const ICONS: Record<MFAMethod, React.ReactNode> = {
  passkey: <ShieldCheck className="h-5 w-5" />,
  totp:    <Smartphone  className="h-5 w-5" />,
  push:    <Bell        className="h-5 w-5" />,
  email:   <Mail        className="h-5 w-5" />,
  sms:     <MessageSquare className="h-5 w-5" />,
}

function MFAPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data: methods, isLoading } = useQuery({
    queryKey: ['mfa', 'methods'],
    queryFn: listMyEnrolledMethods,
  })

  const remove = useAppMutation({
    mutationFn: (m: MFAMethod) => disableMFAMethod(m),
    onSuccess: () => {
      toast.success(t('settings.mfa.disabled'))
      qc.invalidateQueries({ queryKey: ['mfa', 'methods'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not disable MFA'),
  })

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <PageHeader
        title={t('settings.mfa.title')}
        description={t('settings.mfa.description')}
      />

      <Section icon={<ShieldCheck className="h-5 w-5" />} title={t('settings.mfa.enrolled')}>
        {isLoading && <Spinner />}
        {!isLoading && (methods?.length ?? 0) === 0 && (
          <p className="text-sm text-muted-foreground">
            {t('settings.mfa.none_enrolled')}
          </p>
        )}
        <div className="space-y-2">
          {(methods ?? []).map((m) => (
            <Row key={m.method} m={m} onRemove={() => remove.mutate(m.method)} />
          ))}
        </div>

        <p className="mt-4 text-xs text-muted-foreground">
          {t('settings.mfa.need_passkey')} <Link to="/settings/security" className="underline">{t('settings.mfa.manage_passkeys')}</Link>.
        </p>
        <p className="mt-2 text-xs text-muted-foreground">
          <Link to="/settings/security/mfa/recovery" className="underline">{t('settings.mfa.recovery_link')}</Link>{' '}
          {t('settings.mfa.recovery_hint')}
        </p>
      </Section>

      <EmailEnrollSection onChanged={() => qc.invalidateQueries({ queryKey: ['mfa', 'methods'] })} />
      <SMSEnrollSection   onChanged={() => qc.invalidateQueries({ queryKey: ['mfa', 'methods'] })} />
      <PushSection />
    </div>
  )
}

function Row({ m, onRemove }: { m: EnrolledMethod; onRemove: () => void }) {
  const { t } = useTranslation('common')
  return (
    <div className="flex items-center gap-3 rounded-md border border-border bg-card p-3">
      <div className="text-muted-foreground">{ICONS[m.method]}</div>
      <div className="flex-1">
        <div className="flex items-center gap-2 text-sm font-medium capitalize">
          {m.method}
          <Badge variant={m.strength >= 4 ? 'active' : m.strength === 3 ? 'in_review' : 'default'}>
            {STRENGTH_LABEL[m.strength] ?? '—'}
          </Badge>
        </div>
        {m.destination && (
          <p className="text-xs text-muted-foreground">
            <code>{m.destination}</code>
          </p>
        )}
      </div>
      {m.method !== 'passkey' && (
        <Button
          variant="ghost"
          size="sm"
          onClick={onRemove}
          aria-label={`Disable ${m.method} as an MFA method`}
          title={t('settings.mfa.disable')}
        >
          <Trash2 className="h-4 w-4" aria-hidden="true" />
        </Button>
      )}
    </div>
  )
}

// enrollErrorMessage maps the enrol failure; 409 METHOD_UNAVAILABLE
// means the tenant has no sender for this channel.
function enrollErrorMessage(e: unknown, fallback: string, unavailable: string): string {
  const status = (e as { response?: { status?: number } } | null)?.response?.status
  if (status === 409) return unavailable
  return readErrorMessage(e) ?? fallback
}

// VerifyCodeStep — AUTH CB-4: enrolling only sends a code; the factor
// stays pending until that code is posted to enroll/verify.
function VerifyCodeStep({ kind, onVerify, pending, onCancel }: {
  kind: 'email' | 'sms'
  onVerify: (code: string) => void
  pending: boolean
  onCancel: () => void
}) {
  const { t } = useTranslation('common')
  const [code, setCode] = useState('')
  return (
    <form
      className="mt-3 flex flex-wrap items-end gap-2"
      onSubmit={(e) => { e.preventDefault(); if (code.trim()) onVerify(code.trim()) }}
    >
      <div className="min-w-[10rem]">
        <label className="mb-1 block text-xs text-muted-foreground" htmlFor={`mfa-code-${kind}`}>
          {t('settings.mfa.verify_code_label')}
        </label>
        <Input
          id={`mfa-code-${kind}`}
          value={code}
          onChange={(e) => setCode(e.target.value)}
          inputMode="numeric"
          autoComplete="one-time-code"
          data-testid={`mfa-verify-code-${kind}`}
        />
      </div>
      <Button type="submit" disabled={!code.trim() || pending} data-testid={`mfa-verify-${kind}`}>
        {pending ? <Spinner /> : <ShieldCheck className="h-4 w-4" />}
        {t('settings.mfa.verify_activate')}
      </Button>
      <Button type="button" variant="ghost" onClick={onCancel} disabled={pending}>
        {t('actions.cancel')}
      </Button>
    </form>
  )
}

function EmailEnrollSection({ onChanged }: { onChanged: () => void }) {
  const { t } = useTranslation('common')
  const me = useAuthStore((s) => s.user)
  const [email, setEmail] = useState(me?.email ?? '')
  const [awaitingCode, setAwaitingCode] = useState(false)
  const enroll = useAppMutation({
    mutationFn: (v: string) => enrollEmailMFA(v),
    onSuccess: () => {
      setAwaitingCode(true)
      toast.success(t('settings.mfa.code_sent_email'))
    },
    onError: (e: unknown) =>
      toast.error(enrollErrorMessage(e, 'Could not enroll email factor', t('settings.mfa.method_unavailable'))),
  })
  const verify = useAppMutation({
    mutationFn: (code: string) => verifyEmailEnrollment(code),
    onSuccess: () => {
      setAwaitingCode(false)
      toast.success(t('settings.mfa.email_enrolled'))
      onChanged()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('settings.mfa.code_rejected')),
  })
  return (
    <Section icon={<Mail className="h-5 w-5" />} title={t('settings.mfa.email_codes')}
      hint={t('settings.mfa.email_hint')}>
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-[16rem] flex-1">
          <Input
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder={t('settings.mfa.email_placeholder')}
            type="email"
          />
        </div>
        <Button
          onClick={() => enroll.mutate(email)}
          disabled={!email || enroll.isPending}
          data-testid="mfa-enroll-email"
        >
          {enroll.isPending ? <Spinner /> : <Plus className="h-4 w-4" />}
          {t('settings.mfa.enroll_email')}
        </Button>
      </div>
      {awaitingCode && (
        <VerifyCodeStep
          kind="email"
          pending={verify.isPending}
          onVerify={(c) => verify.mutate(c)}
          onCancel={() => setAwaitingCode(false)}
        />
      )}
    </Section>
  )
}

function SMSEnrollSection({ onChanged }: { onChanged: () => void }) {
  const { t } = useTranslation('common')
  const [phone, setPhone] = useState('+')
  const [awaitingCode, setAwaitingCode] = useState(false)
  const enroll = useAppMutation({
    mutationFn: (v: string) => enrollSMSMFA(v),
    onSuccess: () => {
      setAwaitingCode(true)
      toast.success(t('settings.mfa.code_sent_sms'))
    },
    onError: (e: unknown) =>
      toast.error(enrollErrorMessage(e, 'Could not enroll SMS factor', t('settings.mfa.method_unavailable'))),
  })
  const verify = useAppMutation({
    mutationFn: (code: string) => verifySMSEnrollment(code),
    onSuccess: () => {
      setAwaitingCode(false)
      toast.success(t('settings.mfa.sms_enrolled'))
      onChanged()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('settings.mfa.code_rejected')),
  })
  return (
    <Section icon={<MessageSquare className="h-5 w-5" />} title={t('settings.mfa.sms_codes')}
      hint={t('settings.mfa.sms_hint')}>
      <div className="mb-3 flex items-start gap-2 rounded border border-warning/40 bg-warning/10 p-2 text-xs text-warning-strong">
        <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
        <span>
          {t('settings.mfa.sms_warning')}
        </span>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-[16rem] flex-1">
          <Input
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            placeholder={t('settings.mfa.sms_placeholder')}
            data-testid="mfa-sms-phone"
          />
        </div>
        <Button
          onClick={() => enroll.mutate(phone)}
          disabled={!phone || phone.length < 8 || enroll.isPending}
          data-testid="mfa-enroll-sms"
        >
          {enroll.isPending ? <Spinner /> : <Plus className="h-4 w-4" />}
          {t('settings.mfa.enroll_phone')}
        </Button>
      </div>
      {awaitingCode && (
        <VerifyCodeStep
          kind="sms"
          pending={verify.isPending}
          onVerify={(c) => verify.mutate(c)}
          onCancel={() => setAwaitingCode(false)}
        />
      )}
    </Section>
  )
}

function PushSection() {
  const { t } = useTranslation('common')
  return (
    <Section icon={<Bell className="h-5 w-5" />} title={t('settings.mfa.push')}
      hint={t('settings.mfa.push_hint')}>
      <p className="text-sm text-muted-foreground">
        {t('settings.mfa.push_body')}
      </p>
    </Section>
  )
}

function Section({ icon, title, hint, children }: { icon: React.ReactNode; title: string; hint?: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-border bg-card p-6">
      <div className="mb-4">
        <h2 className="flex items-center gap-2 text-lg font-semibold">{icon}{title}</h2>
        {hint && <p className="mt-1 text-sm text-muted-foreground">{hint}</p>}
      </div>
      {children}
    </section>
  )
}
