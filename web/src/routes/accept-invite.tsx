import { createFileRoute, Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { AlertCircle, Check, X } from 'lucide-react'
import { toast } from 'sonner'

import { acceptInvite } from '@/api/auth'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  Form, FormField, FormItem, FormLabel, FormControl,
} from '@/components/ui/form'
import { AuthShell } from '@/components/layout/auth-shell'
import { cn } from '@/lib/cn'

interface InviteSearch {
  tenant?: string
  token?: string
}

interface PolicyCheck {
  label: string
  ok: boolean
}

function evaluatePolicy(password: string, confirm: string): PolicyCheck[] {
  return [
    { label: 'At least 12 characters', ok: password.length >= 12 },
    { label: 'Mix of upper and lowercase', ok: /[a-z]/.test(password) && /[A-Z]/.test(password) },
    { label: 'At least one digit', ok: /\d/.test(password) },
    { label: 'At least one symbol', ok: /[^A-Za-z0-9]/.test(password) },
    { label: 'Both fields match', ok: password.length > 0 && password === confirm },
  ]
}

// L-5 — useForm + zodResolver via the shadcn form.tsx primitive. The Zod
// schema mirrors the existing evaluatePolicy() password rules (12+ chars,
// upper+lower, digit, symbol, fields-match) so the policy checklist below
// and the submit-gating validation share one source of truth.
const acceptInviteSchema = z
  .object({
    password: z
      .string()
      .min(12, 'At least 12 characters')
      .refine((p) => /[a-z]/.test(p) && /[A-Z]/.test(p), 'Mix of upper and lowercase')
      .refine((p) => /\d/.test(p), 'At least one digit')
      .refine((p) => /[^A-Za-z0-9]/.test(p), 'At least one symbol'),
    confirm: z.string(),
  })
  .refine((v) => v.password === v.confirm, {
    message: 'Both fields must match',
    path: ['confirm'],
  })

type AcceptInviteValues = z.infer<typeof acceptInviteSchema>

function AcceptInvitePage() {
  const search = useSearch({ from: '/accept-invite' }) as InviteSearch
  const navigate = useNavigate()
  const tenantSlug = (search.tenant ?? '').trim()
  const token = (search.token ?? '').trim()

  const [done, setDone] = useState(false)

  const form = useForm<AcceptInviteValues>({
    resolver: zodResolver(acceptInviteSchema),
    defaultValues: { password: '', confirm: '' },
    mode: 'onChange',
  })

  const linkOK = tenantSlug !== '' && token !== ''
  // Live policy checklist mirrors react-hook-form's watched values so the
  // green-tick UI updates on every keystroke, same as the old useState
  // version. useWatch (not form.watch) is the memoization-safe subscription.
  const password = useWatch({ control: form.control, name: 'password' })
  const confirm = useWatch({ control: form.control, name: 'confirm' })
  const checks = useMemo(() => evaluatePolicy(password, confirm), [password, confirm])

  const onSubmit = async (values: AcceptInviteValues) => {
    if (!linkOK) return
    try {
      await acceptInvite(tenantSlug, token, values.password)
      setDone(true)
      toast.success('Password set — sign in to continue')
      setTimeout(() => navigate({ to: '/login' }), 1500)
    } catch (err) {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Activation failed — link may be expired or already used')
    }
  }

  if (!linkOK) {
    return (
      <AuthShell
        title="This activation link is invalid"
        description="The link is missing a tenant or token. Ask your administrator to resend the invitation."
        footer={
          <Link to="/login" className="font-medium text-foreground underline-offset-4 hover:underline">Back to sign in</Link>
        }
      >
        <div className="flex flex-col items-center gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-6 text-center">
          <span className="flex h-10 w-10 items-center justify-center rounded-full bg-destructive/10 text-destructive">
            <AlertCircle className="h-5 w-5" />
          </span>
          <p className="text-sm text-muted-foreground">
            Activation links expire after 7 days. If yours is older, request a new one from the admin who invited you.
          </p>
        </div>
      </AuthShell>
    )
  }

  if (done) {
    return (
      <AuthShell title="Account activated" description="Sending you to the sign-in page…">
        <div className="flex flex-col items-center gap-3 rounded-lg border border-success/30 bg-success/10 p-6 text-center">
          <span className="flex h-10 w-10 items-center justify-center rounded-full bg-success/20 text-success">
            <Check className="h-5 w-5" />
          </span>
          <p className="text-sm text-muted-foreground">Your password has been set. You'll be redirected in a moment.</p>
        </div>
      </AuthShell>
    )
  }

  return (
    <AuthShell
      title="Set your password"
      description={
        <>
          You're activating an account on tenant{' '}
          <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">{tenantSlug}</code>.
        </>
      }
      footer={
        <>
          Already activated?{' '}
          <Link to="/login" className="font-medium text-foreground underline-offset-4 hover:underline">Sign in</Link>
        </>
      }
    >
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
          <FormField
            control={form.control}
            name="password"
            render={({ field }) => (
              <FormItem>
                <FormLabel>New password</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type="password"
                    placeholder="Pick something strong"
                    autoFocus
                    autoComplete="new-password"
                  />
                </FormControl>
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="confirm"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Confirm password</FormLabel>
                <FormControl>
                  <Input {...field} type="password" autoComplete="new-password" />
                </FormControl>
              </FormItem>
            )}
          />

          <ul className="space-y-1.5 rounded-md border border-border bg-muted/30 p-3">
            {checks.map((c) => (
              <li key={c.label} className="flex items-center gap-2 text-xs">
                <span
                  className={cn(
                    'flex h-4 w-4 shrink-0 items-center justify-center rounded-full',
                    c.ok ? 'bg-success/20 text-success' : 'bg-muted text-muted-foreground',
                  )}
                  aria-hidden
                >
                  {c.ok ? <Check className="h-3 w-3" /> : <X className="h-3 w-3" />}
                </span>
                <span className={cn(c.ok ? 'text-foreground' : 'text-muted-foreground')}>{c.label}</span>
              </li>
            ))}
          </ul>

          <Button
            type="submit"
            className="w-full"
            loading={form.formState.isSubmitting}
            disabled={!form.formState.isValid}
          >
            Activate account
          </Button>
        </form>
      </Form>
    </AuthShell>
  )
}

export const Route = createFileRoute('/accept-invite')({
  component: AcceptInvitePage,
  validateSearch: (s: Record<string, unknown>): InviteSearch => ({
    tenant: typeof s.tenant === 'string' ? s.tenant : undefined,
    token: typeof s.token === 'string' ? s.token : undefined,
  }),
})
