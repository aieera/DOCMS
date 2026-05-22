// ADR 0063 — MFA settings page. Lets the user enroll, verify, and
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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  ShieldCheck, Smartphone, Mail, MessageSquare, Bell,
  Trash2, Plus, AlertTriangle,
} from 'lucide-react'

import {
  listMyEnrolledMethods,
  enrollEmailMFA,
  enrollSMSMFA,
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
  const qc = useQueryClient()
  const { data: methods, isLoading } = useQuery({
    queryKey: ['mfa', 'methods'],
    queryFn: listMyEnrolledMethods,
  })

  const remove = useMutation({
    mutationFn: (m: MFAMethod) => disableMFAMethod(m),
    onSuccess: () => {
      toast.success('Method disabled')
      qc.invalidateQueries({ queryKey: ['mfa', 'methods'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not disable MFA'),
  })

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <PageHeader
        title="Multi-factor authentication"
        description="Add a second factor to your sign-in. Stronger methods are listed first."
      />

      <Section icon={<ShieldCheck className="h-5 w-5" />} title="Enrolled methods">
        {isLoading && <Spinner />}
        {!isLoading && (methods?.length ?? 0) === 0 && (
          <p className="text-sm text-muted-foreground">
            No methods enrolled yet. Add one below.
          </p>
        )}
        <div className="space-y-2">
          {(methods ?? []).map((m) => (
            <Row key={m.method} m={m} onRemove={() => remove.mutate(m.method)} />
          ))}
        </div>

        <p className="mt-4 text-xs text-muted-foreground">
          Need a passkey? <Link to="/settings/security" className="underline">Manage passkeys here</Link>.
        </p>
        <p className="mt-2 text-xs text-muted-foreground">
          <Link to="/settings/security/mfa/recovery" className="underline">Generate / view recovery codes</Link>{' '}
          — keep them somewhere safe in case you lose your device.
        </p>
      </Section>

      <EmailEnrollSection onChanged={() => qc.invalidateQueries({ queryKey: ['mfa', 'methods'] })} />
      <SMSEnrollSection   onChanged={() => qc.invalidateQueries({ queryKey: ['mfa', 'methods'] })} />
      <PushSection />
    </div>
  )
}

function Row({ m, onRemove }: { m: EnrolledMethod; onRemove: () => void }) {
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
          title="Disable this method"
        >
          <Trash2 className="h-4 w-4" aria-hidden="true" />
        </Button>
      )}
    </div>
  )
}

function EmailEnrollSection({ onChanged }: { onChanged: () => void }) {
  const me = useAuthStore((s) => s.user)
  const [email, setEmail] = useState(me?.email ?? '')
  const enroll = useMutation({
    mutationFn: (v: string) => enrollEmailMFA(v),
    onSuccess: () => {
      toast.success('Email enrolled — verify by signing out and signing in.')
      onChanged()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not enroll email factor'),
  })
  return (
    <Section icon={<Mail className="h-5 w-5" />} title="Email one-time codes"
      hint="Receive a 6-digit code by email at sign-in. Useful as a backup factor.">
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-[16rem] flex-1">
          <Input
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@example.com"
            type="email"
          />
        </div>
        <Button onClick={() => enroll.mutate(email)} disabled={!email || enroll.isPending}>
          {enroll.isPending ? <Spinner /> : <Plus className="h-4 w-4" />}
          Enroll email
        </Button>
      </div>
    </Section>
  )
}

function SMSEnrollSection({ onChanged }: { onChanged: () => void }) {
  const [phone, setPhone] = useState('+')
  const enroll = useMutation({
    mutationFn: (v: string) => enrollSMSMFA(v),
    onSuccess: () => {
      toast.success('SMS enrolled — verify by signing out and signing in.')
      onChanged()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not enroll SMS factor'),
  })
  return (
    <Section icon={<MessageSquare className="h-5 w-5" />} title="SMS one-time codes"
      hint="Discouraged — SMS is the weakest factor. Provided for compatibility with enterprise policies that require it.">
      <div className="mb-3 flex items-start gap-2 rounded border border-warning/40 bg-warning/10 p-2 text-xs text-warning">
        <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
        <span>
          SMS is vulnerable to SIM-swap and SS7 attacks. Prefer a passkey, TOTP app, or push instead.
        </span>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-[16rem] flex-1">
          <Input
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            placeholder="+14155552671"
          />
        </div>
        <Button onClick={() => enroll.mutate(phone)} disabled={!phone || phone.length < 8 || enroll.isPending}>
          {enroll.isPending ? <Spinner /> : <Plus className="h-4 w-4" />}
          Enroll phone
        </Button>
      </div>
    </Section>
  )
}

function PushSection() {
  return (
    <Section icon={<Bell className="h-5 w-5" />} title="Push notifications"
      hint="Approve sign-in from the VaultDMS mobile app. The mobile app ships in Phase 11.3.">
      <p className="text-sm text-muted-foreground">
        Once the mobile app is installed and you've signed in there, the device
        will register itself automatically and appear here.
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
