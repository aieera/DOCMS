import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { AuthShell } from '@/components/layout/auth-shell'
import { register } from '@/api/auth'
import { readErrorMessage } from '@/api/client'

function RegisterPage() {
  const [tenantSlug, setTenantSlug] = useState('acme')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      await register(email, password, name, tenantSlug)
      toast.success('Account created — please log in')
      setTimeout(() => navigate({ to: '/login' }), 800)
    } catch (err: unknown) {
      // H-6: only surface what the response interceptor in api/client.ts
      // doesn't already toast. Today the interceptor handles:
      //   - 401 (logs out + redirects; no toast)
      //   - 403 (toasts "Access denied — {detail}")
      //   - 429 (toasts the rate-limit message)
      //   - 5xx (toasts "Server error: {detail}")
      //   - 400 with a parsable error body (toasts the field message)
      // Gaps we cover here: 409 (email conflict), 400 with no detail,
      // and the no-response / network case. Everything else falls
      // through silently — the interceptor already showed a toast and
      // a second one would just confuse users.
      const status = (err as { response?: { status?: number } } | null)?.response?.status
      const detail = readErrorMessage(err)
      if (status === 409) {
        toast.error(detail ?? 'That email is already registered. Try signing in instead.')
      } else if (status === 400 && !detail) {
        // Interceptor stays silent on 400 with no parsable body.
        toast.error('Check the form for invalid fields and try again.')
      } else if (status === undefined) {
        // No HTTP response at all — network down, CORS rejection,
        // proxy unreachable. Interceptor doesn't handle this either.
        toast.error("Couldn't reach the auth service. Check your connection and try again.")
      }
      // 401 / 403 / 429 / 5xx / 400-with-detail: interceptor already
      // toasted. Stay quiet here so the user sees one message, not two.
    } finally {
      setLoading(false)
    }
  }

  return (
    <AuthShell
      title="Create your account"
      description="Spin up a workspace in seconds. We'll set up tenant isolation, encryption keys, and an audit trail for you."
      footer={
        <>
          Already have one?{' '}
          <Link to="/login" className="font-medium text-foreground underline-offset-4 hover:underline">Sign in</Link>
        </>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <Input label="Tenant slug" value={tenantSlug} onChange={(e) => setTenantSlug(e.target.value)} required autoComplete="organization" />
        <Input label="Full name" value={name} onChange={(e) => setName(e.target.value)} required autoFocus autoComplete="name" />
        <Input label="Work email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoComplete="email" />
        <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoComplete="new-password" />
        <p className="text-xs text-muted-foreground">
          By creating an account you agree to the Terms and Privacy Policy.
        </p>
        <Button type="submit" className="w-full" loading={loading}>Create account</Button>
      </form>
    </AuthShell>
  )
}

export const Route = createFileRoute('/register')({ component: RegisterPage })
