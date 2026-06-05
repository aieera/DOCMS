import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Fingerprint, Mail, MessageSquare, Bell, Smartphone, Loader2 } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  Form, FormField, FormItem, FormLabel, FormControl, FormMessage,
} from '@/components/ui/form'
import { AuthShell } from '@/components/layout/auth-shell'
import { useAuthStore } from '@/store/authStore'
import { login, verifyMFA } from '@/api/auth'
import { loginWithPasskey, isWebAuthnSupported } from '@/api/webauthn'
import type { User } from '@/types/api'
import { finalizeLoginResult, FINALIZE_TENANT_MISSING_MESSAGE } from '@/lib/finalizeLogin'
import { readErrorMessage } from '@/api/client'
import {
  startEmailOTP, verifyEmailOTP,
  startSMSOTP, verifySMSOTP,
  startPushChallenge, verifyPushChallenge,
  type MFAMethod,
} from '@/api/mfa'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

interface MethodOption { method: MFAMethod; strength: number; destination?: string }

// L-5 — the primary credential form (tenant, email, password) runs on
// useForm + zodResolver via the shadcn form.tsx primitive. The MFA
// picker/verify and passkey sub-flows keep their own useState because
// they're stateful flow-control, not a single validated form; the
// `loading` flag still gates every async path's spinner.
const loginSchema = z.object({
  tenantSlug: z.string().trim().min(1, 'Tenant is required'),
  email: z.string().trim().min(1, 'Email is required').email('Enter a valid email address'),
  password: z.string().min(1, 'Password is required'),
})

type LoginValues = z.infer<typeof loginSchema>

function LoginPage() {
  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { tenantSlug: 'acme', email: '', password: '' },
  })
  // Memoization-safe subscription (vs form.watch) — drives the passkey
  // button's disabled state, which gates on a non-empty email.
  const watchedEmail = useWatch({ control: form.control, name: 'email' })
  const [loading, setLoading] = useState(false)
  // ADR 0063 — when the password step lands `mfa_required: true`,
  // we hold the session token + the strongest-first method list and
  // pivot the form to the picker. `chosen` advances to the verify
  // sub-step.
  const [mfaToken, setMfaToken] = useState<string | null>(null)
  const [methods, setMethods] = useState<MethodOption[]>([])
  const [chosen, setChosen] = useState<MethodOption | null>(null)
  const [code, setCode] = useState('')
  const [pushChallengeID, setPushChallengeID] = useState<string | null>(null)
  const authLogin = useAuthStore((s) => s.login)
  const navigate = useNavigate()

  // Single chokepoint after a successful sign-in (password, MFA, OR
  // passkey). H-1: an empty tenant_id used to be passed straight into
  // the auth store, which then left X-Auth-Tenant-ID off every
  // follow-up request — server-side identity checks fail open with a
  // generic 401 and the user sees a broken session. The validation
  // lives in ./login.finalize so it's unit-testable in isolation.
  const finalizeLogin = (user: User | undefined): boolean => {
    const res = finalizeLoginResult(user)
    if (!res.ok) {
      toast.error(FINALIZE_TENANT_MISSING_MESSAGE)
      return false
    }
    authLogin(res.user, res.tenantId)
    navigate({ to: '/' })
    return true
  }

  const handleSubmit = async (values: LoginValues) => {
    setLoading(true)
    try {
      const data = await login(values.email, values.password, values.tenantSlug)
      if (data.mfa_required && data.mfa_session_token) {
        setMfaToken(data.mfa_session_token)
        setMethods(data.mfa_methods ?? [])
        return
      }
      finalizeLogin(data.user)
    } catch (err: unknown) {
      // Surface specific failure modes instead of always saying
      // "Invalid credentials" — particularly 429, which previously
      // left the spinner stuck since the client interceptor toasts
      // the rate-limit message but the page kept loading=true on
      // any non-axios-handled error path (BUG-D).
      const e = err as { response?: { status?: number; data?: { error?: string } } }
      const status = e?.response?.status
      if (status === 429) {
        // Interceptor already toasted; nothing to add.
      } else if (status === 401) {
        toast.error('Invalid credentials')
      } else if (e?.response?.data?.error) {
        toast.error(e.response.data.error)
      } else {
        toast.error('Sign in failed')
      }
    } finally {
      setLoading(false)
    }
  }

  const pickMethod = async (m: MethodOption) => {
    if (!mfaToken) return
    setChosen(m)
    setCode('')
    try {
      switch (m.method) {
        case 'email': await startEmailOTP(mfaToken); toast.success('Code sent to your email'); break
        case 'sms':   await startSMSOTP(mfaToken);   toast.success('Code sent by SMS'); break
        case 'push': {
          const { challenge_id } = await startPushChallenge(mfaToken)
          setPushChallengeID(challenge_id)
          toast.success('Approve from your mobile device')
          break
        }
        case 'totp':
          // No "start" step — user opens their authenticator app.
          break
        default:
          // passkey is handled by the existing handlePasskey flow.
          break
      }
    } catch (e: unknown) {
      // Preserve the 409-specific copy ("that method's not enabled
      // on this deploy") since it's more actionable than the
      // generic server message; readErrorMessage handles everything
      // else.
      const status = (e as { response?: { status?: number } } | null)?.response?.status
      if (status === 409) toast.error('That method is unavailable on this deploy.')
      else toast.error(readErrorMessage(e) ?? 'Failed to start verification')
      setChosen(null)
    }
  }

  const submitCode = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!mfaToken || !chosen) return
    setLoading(true)
    try {
      // All four MFA verify endpoints return the same shape:
      // { user, session_token, ... }. Type the local instead of
      // `any` so the finalizeLogin call below is checked.
      let data: { user: User }
      switch (chosen.method) {
        case 'totp':  data = await verifyMFA(mfaToken, code); break
        case 'email': data = await verifyEmailOTP(mfaToken, code); break
        case 'sms':   data = await verifySMSOTP(mfaToken, code); break
        case 'push':
          if (!pushChallengeID) throw new Error('no challenge')
          data = await verifyPushChallenge(mfaToken, pushChallengeID, code || 'ack')
          break
        default: throw new Error('unsupported method')
      }
      finalizeLogin(data.user)
    } catch (e: unknown) {
      toast.error(readErrorMessage(e) ?? 'Code rejected')
    } finally {
      setLoading(false)
    }
  }

  // ADR 0061 — passkey-as-primary path. Skips the password entirely
  // for users with a registered cred. Failure modes (no passkey,
  // user-cancelled, browser doesn't support) toast and stay on the
  // login page so the user can fall back to password.
  const handlePasskey = async () => {
    const { email, tenantSlug } = form.getValues()
    if (!email) {
      toast.error('Enter your email first — we use it to find your passkeys')
      return
    }
    setLoading(true)
    try {
      const data = await loginWithPasskey({ tenant_slug: tenantSlug, email })
      // Route through the same chokepoint as the password path so the
      // empty-tenant_id guard can never be forgotten on one of the
      // login flows again (H-1 invariant; see Wave 5 pattern 5).
      finalizeLogin(data.user as User | undefined)
    } catch (err: unknown) {
      const e = err as { response?: { status?: number; data?: { error?: string } }; message?: string }
      if (e.response?.status === 501) {
        toast.error('Passkeys not enabled on this deploy. Use your password.')
      } else if (e.response?.status === 404) {
        toast.error('No passkey for this account yet. Sign in with password, then add one in Settings → Security.', { duration: 6000 })
      } else if (e.response?.status === 401) {
        toast.error('Invalid credentials')
      } else {
        toast.error(e.message ?? 'Passkey sign-in failed')
      }
    } finally {
      setLoading(false)
    }
  }

  // --- view selection ---------------------------------------------------

  if (mfaToken && !chosen) {
    return (
      <AuthShell
        title="Two-step verification"
        description="Pick how you'd like to confirm it's you. Stronger methods are listed first."
      >
        <div className="space-y-2" data-testid="mfa-picker">
          {methods.length === 0 ? (
            <p className="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
              No methods enrolled — contact your administrator.
            </p>
          ) : (
            methods.map((m) => (
              <button
                key={m.method}
                type="button"
                onClick={() => pickMethod(m)}
                data-testid={`mfa-pick-${m.method}`}
                className="group flex w-full items-center gap-3 rounded-md border border-input bg-background px-3 py-2.5 text-start transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-muted text-foreground">
                  <MethodIcon m={m.method} />
                </span>
                <span className="flex-1">
                  <span className="block text-sm font-medium capitalize">{m.method}</span>
                  {m.destination && (
                    <span className="block text-xs text-muted-foreground">{m.destination}</span>
                  )}
                </span>
              </button>
            ))
          )}
          <Button
            type="button"
            variant="ghost"
            className="w-full"
            onClick={() => { setMfaToken(null); setMethods([]); form.resetField('password') }}
          >
            <DirectionalIcon name="ArrowLeft" className="me-1 h-4 w-4" /> Back
          </Button>
        </div>
      </AuthShell>
    )
  }

  if (mfaToken && chosen) {
    return (
      <AuthShell
        title={`Enter your ${chosen.method.toUpperCase()} code`}
        description={chosen.destination ? `We sent a code to ${chosen.destination}.` : 'Open your authenticator and enter the 6-digit code.'}
      >
        <form onSubmit={submitCode} className="space-y-4" data-testid="mfa-verify">
          {chosen.method === 'push' ? (
            <p className="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
              Waiting for approval on your device. Tap <strong className="text-foreground">Approve</strong> there to continue.
            </p>
          ) : (
            <Input
              label="Verification code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              placeholder="123456"
              inputMode="numeric"
              autoComplete="one-time-code"
              autoFocus
              required
              data-testid="mfa-code"
            />
          )}
          <Button type="submit" className="w-full" loading={loading}>Verify and continue</Button>
          <Button type="button" variant="ghost" className="w-full" onClick={() => setChosen(null)}>
            <DirectionalIcon name="ArrowLeft" className="me-1 h-4 w-4" /> Use a different method
          </Button>
        </form>
      </AuthShell>
    )
  }

  return (
    <AuthShell
      title="Welcome back"
      description="Sign in to continue to your SeDoc workspace."
      footer={
        <>
          New to SeDoc?{' '}
          <Link to="/register" className="font-medium text-foreground underline-offset-4 hover:underline">Create an account</Link>
        </>
      }
    >
      <Form {...form}>
        <form onSubmit={form.handleSubmit(handleSubmit)} className="space-y-4">
          <FormField
            control={form.control}
            name="tenantSlug"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Tenant</FormLabel>
                <FormControl>
                  <Input {...field} type="text" autoComplete="organization" />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="email"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Email</FormLabel>
                <FormControl>
                  {/* autoComplete="username" (not "email") — this is the
                      credential-form pattern. With "email", Chrome
                      aggressively offers any address ever typed in any
                      email field (BUG-C); "username" scopes suggestions
                      to saved credentials for this site only and stays
                      compatible with password managers. */}
                  <Input {...field} type="email" autoFocus autoComplete="username" name="login-email" />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          {/* ADR 0061 — passkey-as-primary. Above the password so a user
              with a registered passkey can skip the password entirely. */}
          {isWebAuthnSupported() && (
            <Button
              type="button"
              variant="outline"
              onClick={handlePasskey}
              disabled={loading || !watchedEmail}
              className="w-full"
              data-testid="login-passkey"
            >
              {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Fingerprint className="h-4 w-4" />}
              Sign in with passkey
            </Button>
          )}

          <div className="relative my-1 text-center text-xs uppercase tracking-wider text-muted-foreground">
            <span className="relative z-10 bg-background px-2">or with password</span>
            <span className="absolute inset-x-0 top-1/2 border-t border-border" aria-hidden />
          </div>

          <FormField
            control={form.control}
            name="password"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Password</FormLabel>
                <FormControl>
                  <Input {...field} type="password" autoComplete="current-password" />
                </FormControl>
                <FormMessage />
                <div className="mt-1.5 flex justify-end">
                  <Link to="/forgot-password" className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
                    Forgot password?
                  </Link>
                </div>
              </FormItem>
            )}
          />

          <Button type="submit" className="w-full" loading={loading}>Sign in</Button>
        </form>
      </Form>
    </AuthShell>
  )
}

function MethodIcon({ m }: { m: MFAMethod }) {
  switch (m) {
    case 'passkey': return <Fingerprint className="h-4 w-4" />
    case 'totp':    return <Smartphone className="h-4 w-4" />
    case 'push':    return <Bell className="h-4 w-4" />
    case 'email':   return <Mail className="h-4 w-4" />
    case 'sms':     return <MessageSquare className="h-4 w-4" />
  }
}

export const Route = createFileRoute('/login')({ component: LoginPage })
