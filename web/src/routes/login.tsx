import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { useAuthStore } from '@/store/authStore'
import { login, verifyMFA, recoverMFA } from '@/api/auth'
import type { User } from '@/types/api'
import toast from 'react-hot-toast'

type Step = 'credentials' | 'totp' | 'recovery'

function LoginPage() {
  const [step, setStep] = useState<Step>('credentials')
  const [tenantSlug, setTenantSlug] = useState('acme')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [totp, setTotp] = useState('')
  const [recoveryCode, setRecoveryCode] = useState('')
  const [mfaSessionToken, setMfaSessionToken] = useState('')
  const [loading, setLoading] = useState(false)
  const authLogin = useAuthStore((s) => s.login)
  const navigate = useNavigate()

  const finish = (user: User) => {
    authLogin(user, user.tenant_id ?? '')
    navigate({ to: '/' })
  }

  const handleCredentials = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const data = await login(email, password, tenantSlug)
      if (data.require_password_change && data.one_time_change_token) {
        navigate({
          to: '/change-password',
          search: { token: data.one_time_change_token },
        })
        return
      }
      if (data.mfa_required && data.mfa_session_token) {
        setMfaSessionToken(data.mfa_session_token)
        setStep('totp')
        return
      }
      finish(data.user)
    } catch {
      toast.error('Invalid credentials')
    } finally {
      setLoading(false)
    }
  }

  const handleTotp = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const data = await verifyMFA(mfaSessionToken, totp)
      finish(data.user)
    } catch {
      toast.error('Invalid code')
    } finally {
      setLoading(false)
    }
  }

  const handleRecovery = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const data = await recoverMFA(mfaSessionToken, recoveryCode)
      // DoD requires that after a recovery-code login the user is forced
      // through a fresh MFA enrollment and remaining codes are revoked.
      // Backend does NOT yet do either — logged in out-of-scope.md. Until
      // then the best we can do client-side is surface the expectation.
      toast('Recovery code used. Re-enroll MFA from Settings soon.', {
        icon: '⚠',
        duration: 6000,
      })
      finish(data.user)
    } catch {
      toast.error('Invalid recovery code')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
      <div className="w-full max-w-sm space-y-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
            {step === 'credentials' && 'Sign in to your account'}
            {step === 'totp' && 'Enter your authenticator code'}
            {step === 'recovery' && 'Enter a recovery code'}
          </p>
        </div>

        {step === 'credentials' && (
          <form onSubmit={handleCredentials} className="space-y-4">
            <Input label="Tenant" type="text" value={tenantSlug} onChange={(e) => setTenantSlug(e.target.value)} required />
            <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
            <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
            <Button type="submit" className="w-full" loading={loading}>Sign In</Button>
          </form>
        )}

        {step === 'totp' && (
          <form onSubmit={handleTotp} className="space-y-4">
            <Input
              label="6-digit code"
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              value={totp}
              onChange={(e) => setTotp(e.target.value.replace(/\D/g, ''))}
              required
              autoFocus
            />
            <Button type="submit" className="w-full" loading={loading} disabled={totp.length < 6}>Verify</Button>
            <button
              type="button"
              onClick={() => { setTotp(''); setStep('recovery') }}
              className="block w-full text-center text-sm text-[var(--color-primary)] hover:underline"
            >
              Use a recovery code
            </button>
          </form>
        )}

        {step === 'recovery' && (
          <form onSubmit={handleRecovery} className="space-y-4">
            <Input
              label="Recovery code"
              type="text"
              autoComplete="off"
              value={recoveryCode}
              onChange={(e) => setRecoveryCode(e.target.value.trim())}
              required
              autoFocus
            />
            <Button type="submit" className="w-full" loading={loading} disabled={!recoveryCode}>Sign In</Button>
            <button
              type="button"
              onClick={() => { setRecoveryCode(''); setStep('totp') }}
              className="block w-full text-center text-sm text-[var(--color-primary)] hover:underline"
            >
              Back to authenticator code
            </button>
          </form>
        )}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/login')({ component: LoginPage })
