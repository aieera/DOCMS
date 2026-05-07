import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { Fingerprint } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { useAuthStore } from '@/store/authStore'
import { login } from '@/api/auth'
import { loginWithPasskey, isWebAuthnSupported } from '@/api/webauthn'
import toast from 'react-hot-toast'

function LoginPage() {
  const [tenantSlug, setTenantSlug] = useState('acme')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const authLogin = useAuthStore((s) => s.login)
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const data = await login(email, password, tenantSlug)
      authLogin(data.user, data.user.tenant_id ?? '')
      navigate({ to: '/' })
    } catch {
      toast.error('Invalid credentials')
    } finally {
      setLoading(false)
    }
  }

  // ADR 0070 — passkey-as-primary path. Skips the password entirely
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
        <form onSubmit={handleSubmit} className="space-y-4">
          <Input label="Tenant" type="text" value={tenantSlug} onChange={(e) => setTenantSlug(e.target.value)} required />
          <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />

          {/* ADR 0070 — passkey-as-primary. Shown above password so a
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
      </div>
    </div>
  )
}

export const Route = createFileRoute('/login')({ component: LoginPage })
