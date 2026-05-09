import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { Fingerprint, Mail, MessageSquare, Bell, Smartphone, ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { useAuthStore } from '@/store/authStore'
import { login } from '@/api/auth'
import { verifyMFA } from '@/api/auth'
import { loginWithPasskey, isWebAuthnSupported } from '@/api/webauthn'
import {
  startEmailOTP, verifyEmailOTP,
  startSMSOTP, verifySMSOTP,
  startPushChallenge, verifyPushChallenge,
  type MFAMethod,
} from '@/api/mfa'
import toast from 'react-hot-toast'

interface MethodOption { method: MFAMethod; strength: number; destination?: string }

function LoginPage() {
  const [tenantSlug, setTenantSlug] = useState('acme')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
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

  const finalizeLogin = (user: any) => {
    authLogin(user, user.tenant_id ?? '')
    navigate({ to: '/' })
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const data = await login(email, password, tenantSlug)
      if (data.mfa_required && data.mfa_session_token) {
        setMfaToken(data.mfa_session_token)
        setMethods(data.mfa_methods ?? [])
        return
      }
      finalizeLogin(data.user)
    } catch {
      toast.error('Invalid credentials')
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
        case 'email':
          await startEmailOTP(mfaToken)
          toast.success('Code sent to your email')
          break
        case 'sms':
          await startSMSOTP(mfaToken)
          toast.success('Code sent by SMS')
          break
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
    } catch (e: any) {
      const status = e?.response?.status
      if (status === 409) {
        toast.error('That method is unavailable on this deploy.')
      } else {
        toast.error(e?.response?.data?.error ?? 'Failed to start verification')
      }
      setChosen(null)
    }
  }

  const submitCode = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!mfaToken || !chosen) return
    setLoading(true)
    try {
      let data: any
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
    } catch (e: any) {
      toast.error(e?.response?.data?.error ?? 'Code rejected')
    } finally {
      setLoading(false)
    }
  }

  // ADR 0061 — passkey-as-primary path. Skips the password entirely
  // for users with a registered cred. Failure modes (no passkey,
  // user-cancelled, browser doesn't support) toast and stay on the
  // login page so the user can fall back to password.
  const handlePasskey = async () => {
    if (!email) {
      toast.error('Enter your email first — we use it to find your passkeys')
      return
    }
    setLoading(true)
    try {
      const data = await loginWithPasskey({ tenant_slug: tenantSlug, email })
      const u = data.user as { tenant_id?: string }
      authLogin(data.user as Parameters<typeof authLogin>[0], u.tenant_id ?? '')
      navigate({ to: '/' })
    } catch (err: unknown) {
      const e = err as { response?: { status?: number; data?: { error?: string } }; message?: string }
      if (e.response?.status === 501) {
        toast.error('Passkeys not enabled on this deploy. Use your password.')
      } else if (e.response?.status === 404) {
        // Backend signals "this account exists but has no passkey
        // yet" with 404 + the hint copy. Toast a friendly version
        // pointing at the registration path.
        toast.error('No passkey for this account yet. Sign in with password, then add one in Settings → Security.', {
          duration: 6000,
        })
      } else if (e.response?.status === 401) {
        toast.error('Invalid credentials')
      } else {
        toast.error(e.message ?? 'Passkey sign-in failed')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
      <div className="w-full max-w-sm space-y-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">Sign in to your account</p>
        </div>

        {/* ADR 0063 — MFA picker. Replaces the password form once the
            backend says mfa_required. Shows methods strongest-first. */}
        {mfaToken && !chosen && (
          <div className="space-y-2" data-testid="mfa-picker">
            <p className="text-sm">Choose a sign-in method:</p>
            {methods.length === 0 && (
              <p className="text-xs text-[var(--color-text-secondary)]">
                No methods enrolled — contact your admin.
              </p>
            )}
            {methods.map((m) => (
              <Button
                key={m.method}
                type="button"
                variant="outline"
                className="w-full justify-start"
                onClick={() => pickMethod(m)}
                data-testid={`mfa-pick-${m.method}`}
              >
                <MethodIcon m={m.method} />
                <span className="ml-2 capitalize">{m.method}</span>
                {m.destination && (
                  <span className="ml-auto text-xs text-[var(--color-text-secondary)]">{m.destination}</span>
                )}
              </Button>
            ))}
            <Button type="button" variant="ghost" className="w-full" onClick={() => { setMfaToken(null); setMethods([]); setPassword('') }}>
              <ArrowLeft className="h-4 w-4" /> Cancel
            </Button>
          </div>
        )}

        {mfaToken && chosen && (
          <form onSubmit={submitCode} className="space-y-3" data-testid="mfa-verify">
            <p className="text-sm">
              <strong className="capitalize">{chosen.method}</strong>
              {chosen.destination && <> — sent to <code>{chosen.destination}</code></>}
            </p>
            {chosen.method === 'push' ? (
              <p className="text-xs text-[var(--color-text-secondary)]">
                Waiting for approval on your device. Tap "Approve" there to continue.
              </p>
            ) : (
              <Input
                label="Code"
                value={code}
                onChange={(e) => setCode(e.target.value)}
                placeholder="123456"
                inputMode="numeric"
                autoFocus
                required
                data-testid="mfa-code"
              />
            )}
            <Button type="submit" className="w-full" loading={loading}>Verify</Button>
            <Button type="button" variant="ghost" className="w-full" onClick={() => setChosen(null)}>
              <ArrowLeft className="h-4 w-4" /> Pick a different method
            </Button>
          </form>
        )}

        {!mfaToken && (
        <form onSubmit={handleSubmit} className="space-y-4">
          <Input label="Tenant" type="text" value={tenantSlug} onChange={(e) => setTenantSlug(e.target.value)} required />
          <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />

          {/* ADR 0061 — passkey-as-primary. Shown above password so a
              user with a registered passkey can skip the password entirely. */}
          {isWebAuthnSupported() && (
            <Button
              type="button"
              variant="ghost"
              onClick={handlePasskey}
              disabled={loading || !email}
              className="w-full"
              data-testid="login-passkey"
            >
              <Fingerprint className="h-4 w-4" /> Sign in with passkey
            </Button>
          )}

          <div className="relative my-2 text-center text-xs text-[var(--color-text-secondary)]">
            <span className="relative z-10 bg-[var(--color-bg-secondary)] px-2">or</span>
            <span className="absolute left-0 right-0 top-1/2 border-t border-[var(--color-border)]" />
          </div>

          <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          <Button type="submit" className="w-full" loading={loading}>Sign In</Button>
        </form>
        )}
      </div>
    </div>
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
